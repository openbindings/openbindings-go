package openbindings

import (
	"fmt"

	"github.com/openbindings/openbindings-go/schemaprofile"
)

// CompatibilityIssueKind classifies a compatibility issue.
type CompatibilityIssueKind string

const (
	CompatibilityMissing           CompatibilityIssueKind = "missing"
	CompatibilityOutputIncompatible CompatibilityIssueKind = "output_incompatible"
	CompatibilityInputIncompatible  CompatibilityIssueKind = "input_incompatible"
)

// CompatibilityIssue describes a single incompatibility between a required
// and provided interface.
type CompatibilityIssue struct {
	Operation string
	Kind      CompatibilityIssueKind
	Detail    string
}

// CheckInterfaceCompatibility checks whether a provided interface satisfies
// the requirements of a required interface. For each operation the required
// interface declares:
//
//  1. The operation must exist in the provided interface
//  2. Output schemas must be compatible (provider output satisfies required output)
//  3. Input schemas must be compatible (required input satisfies provider input)
//
// Returns an empty slice when the provided interface is fully compatible.
func CheckInterfaceCompatibility(required, provided *Interface) []CompatibilityIssue {
	var issues []CompatibilityIssue
	norm := &schemaprofile.Normalizer{}

	for opKey, reqOp := range required.Operations {
		provOp, ok := provided.Operations[opKey]
		if !ok {
			issues = append(issues, CompatibilityIssue{
				Operation: opKey,
				Kind:      CompatibilityMissing,
			})
			continue
		}

		if len(reqOp.Output) > 0 {
			if len(provOp.Output) == 0 {
				issues = append(issues, CompatibilityIssue{
					Operation: opKey,
					Kind:      CompatibilityOutputIncompatible,
					Detail:    "required interface expects output but provider declares none",
				})
			} else {
				compatible, err := norm.OutputCompatible(
					map[string]any(reqOp.Output),
					map[string]any(provOp.Output),
				)
				if err != nil {
					issues = append(issues, CompatibilityIssue{
						Operation: opKey,
						Kind:      CompatibilityOutputIncompatible,
						Detail:    fmt.Sprintf("output schema check failed: %v", err),
					})
				} else if !compatible {
					issues = append(issues, CompatibilityIssue{
						Operation: opKey,
						Kind:      CompatibilityOutputIncompatible,
						Detail:    "provider output does not satisfy the required output schema",
					})
				}
			}
		}

		if len(reqOp.Input) > 0 {
			if len(provOp.Input) == 0 {
				issues = append(issues, CompatibilityIssue{
					Operation: opKey,
					Kind:      CompatibilityInputIncompatible,
					Detail:    "required interface declares input but provider accepts none",
				})
			} else {
				compatible, err := norm.InputCompatible(
					map[string]any(reqOp.Input),
					map[string]any(provOp.Input),
				)
				if err != nil {
					issues = append(issues, CompatibilityIssue{
						Operation: opKey,
						Kind:      CompatibilityInputIncompatible,
						Detail:    fmt.Sprintf("input schema check failed: %v", err),
					})
				} else if !compatible {
					issues = append(issues, CompatibilityIssue{
						Operation: opKey,
						Kind:      CompatibilityInputIncompatible,
						Detail:    "provider input is not compatible with the required input schema",
					})
				}
			}
		}
	}

	return issues
}

// IsOBInterface returns true if the given map looks like a valid OpenBindings
// interface document (has "openbindings" string and "operations" map).
func IsOBInterface(v map[string]any) bool {
	if v == nil {
		return false
	}
	_, hasOB := v["openbindings"].(string)
	_, hasOps := v["operations"].(map[string]any)
	return hasOB && hasOps
}
