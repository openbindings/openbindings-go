package openbindings

import (
	"fmt"
	"sort"

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

// CheckCompatibilityOptions configures the compatibility check.
type CheckCompatibilityOptions struct {
	// RequiredInterfaceID is the role key identifying the required interface
	// (e.g., "openbindings.workspace-manager"). When set, enables satisfies-based
	// matching: a provided operation can satisfy a required operation via its
	// Satisfies entry { Role: requiredInterfaceID, Operation: opKey }.
	RequiredInterfaceID string
}

// CheckInterfaceCompatibility checks whether a provided interface satisfies
// the requirements of a required interface. For each operation the required
// interface declares, the algorithm searches the provided interface using
// three strategies (first match wins):
//
//  1. Direct key match — provided.Operations[opKey] exists
//  2. Satisfies match — any provided operation has a Satisfies entry with
//     { Role: requiredInterfaceID, Operation: opKey }
//     (requires opts.RequiredInterfaceID)
//  3. Aliases match — any provided operation has Aliases containing opKey
//
// For each matched pair, schemas are checked:
//   - Output schemas must be compatible (provided output satisfies required output)
//   - Input schemas must be compatible (required input satisfies provided input)
//
// Returns an empty slice when the provided interface is fully compatible.
func CheckInterfaceCompatibility(required, provided *Interface, opts ...CheckCompatibilityOptions) []CompatibilityIssue {
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

	var interfaceID string
	if len(opts) > 0 {
		interfaceID = opts[0].RequiredInterfaceID
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
		provOp, ok := findMatchingOperation(provided, opKey, interfaceID)
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

// findMatchingOperation searches provided for an operation matching opKey
// using three strategies: direct key, satisfies, aliases.
func findMatchingOperation(provided *Interface, opKey, requiredInterfaceID string) (Operation, bool) {
	if op, ok := provided.Operations[opKey]; ok {
		return op, true
	}

	if requiredInterfaceID != "" {
		for _, op := range provided.Operations {
			for _, s := range op.Satisfies {
				if s.Role == requiredInterfaceID && s.Operation == opKey {
					return op, true
				}
			}
		}
	}

	for _, op := range provided.Operations {
		for _, alias := range op.Aliases {
			if alias == opKey {
				return op, true
			}
		}
	}

	return Operation{}, false
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
