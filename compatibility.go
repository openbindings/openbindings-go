package openbindings

import (
	"fmt"
	"sort"

	"github.com/openbindings/openbindings-go/schemaprofile"
)

// CompatibilityIssueKind classifies a compatibility issue.
type CompatibilityIssueKind string

const (
	CompatibilityMissing            CompatibilityIssueKind = "missing"
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
// the requirements of a required interface. This is a tooling convention,
// not a spec requirement: the spec leaves matching, comparison, and
// selection to tools (see openbindings.md §2 Scope principle and the
// schemaprofile package docstring). The algorithm below is the openbindings
// reference tooling's matching convention; third-party tools may use
// different strategies.
//
// For each operation the required interface declares by key, the provided
// interface is searched by that name against its flat key+aliases namespace
// (OBI-T-12): a provided operation matches if its key equals the required key
// or one of its aliases does. Carrying the required contract's operation name
// as an alias is exactly how an implementation claims to fulfill that contract.
//
// For each matched pair, schemas are checked:
//   - Output schemas must be compatible (provided output satisfies required output).
//   - Input schemas must be compatible (required input satisfies provided input).
//
// Returns an empty slice when the provided interface is fully compatible.
func CheckInterfaceCompatibility(required, provided *Interface) []CompatibilityIssue {
	if required == nil {
		return nil
	}
	if provided == nil {
		var issues []CompatibilityIssue
		for opKey := range required.Operations {
			issues = append(issues, CompatibilityIssue{Operation: opKey, Kind: CompatibilityMissing})
		}
		return issues
	}

	var issues []CompatibilityIssue
	norm := &schemaprofile.Normalizer{}

	opKeys := make([]string, 0, len(required.Operations))
	for k := range required.Operations {
		opKeys = append(opKeys, k)
	}
	sort.Strings(opKeys)

	for _, opKey := range opKeys {
		reqOp := required.Operations[opKey]
		_, provOp, ok := ResolveOperation(provided, opKey)
		if !ok {
			issues = append(issues, CompatibilityIssue{
				Operation: opKey,
				Kind:      CompatibilityMissing,
			})
			continue
		}

		// Per spec: absent/null schemas are "unspecified" (skip in compatibility).
		// Empty {} schemas are "accepts anything" (must be checked).
		// Use != nil to distinguish: nil = unspecified, non-nil (including empty) = specified.
		if reqOp.Output != nil && provOp.Output != nil {
			compatible, reason, err := norm.OutputCompatible(
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
				detail := "provided output does not satisfy the required output schema"
				if reason != "" {
					detail = "provided output does not satisfy the required output schema: " + reason
				}
				issues = append(issues, CompatibilityIssue{
					Operation: opKey,
					Kind:      CompatibilityOutputIncompatible,
					Detail:    detail,
				})
			}
		}

		if reqOp.Input != nil && provOp.Input != nil {
			compatible, reason, err := norm.InputCompatible(
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
				detail := "provided input is not compatible with the required input schema"
				if reason != "" {
					detail = "provided input is not compatible with the required input schema: " + reason
				}
				issues = append(issues, CompatibilityIssue{
					Operation: opKey,
					Kind:      CompatibilityInputIncompatible,
					Detail:    detail,
				})
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
