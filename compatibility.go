package openbindings

import (
	"encoding/json"
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

	// Each side's schemas resolve $ref pointers (e.g. "#/schemas/Foo")
	// against their OWN containing document, so a required and a provided
	// interface that both use the schemas section compare correctly. One
	// shared normalizer would resolve both sides' refs against a single
	// root — wrong document for one side, RefError for both when unrooted.
	reqNorm := &schemaprofile.Normalizer{Root: interfaceDocView(required)}
	provNorm := &schemaprofile.Normalizer{Root: interfaceDocView(provided)}

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
		// Boolean schemas take their equivalent object spellings (true = {},
		// false = {"not": {}}) so the profile normalizer sees one form.
		if reqOp.Output != nil && provOp.Output != nil {
			reqOut, reqOK := SchemaObjectForm(reqOp.Output)
			provOut, provOK := SchemaObjectForm(provOp.Output)
			if !reqOK || !provOK {
				issues = append(issues, CompatibilityIssue{
					Operation: opKey,
					Kind:      CompatibilityOutputIncompatible,
					Detail:    "output schema check failed: schema is not a JSON Schema object or boolean",
				})
			} else if compatible, reason, err := normalizedCompatible(reqNorm, provNorm, reqOut, provOut, false); err != nil {
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
			reqIn, reqOK := SchemaObjectForm(reqOp.Input)
			provIn, provOK := SchemaObjectForm(provOp.Input)
			if !reqOK || !provOK {
				issues = append(issues, CompatibilityIssue{
					Operation: opKey,
					Kind:      CompatibilityInputIncompatible,
					Detail:    "input schema check failed: schema is not a JSON Schema object or boolean",
				})
			} else if compatible, reason, err := normalizedCompatible(reqNorm, provNorm, reqIn, provIn, true); err != nil {
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

// normalizedCompatible normalizes each schema against its own side's rooted
// normalizer, then runs the directional check on the results (the TypeScript
// SDK's checkInterfaceCompatibility dance). Normalization errors surface as
// the check's error.
func normalizedCompatible(reqNorm, provNorm *schemaprofile.Normalizer, req, prov map[string]any, isInput bool) (bool, string, error) {
	reqN, err := reqNorm.Normalize(req)
	if err != nil {
		return false, "", err
	}
	provN, err := provNorm.Normalize(prov)
	if err != nil {
		return false, "", err
	}
	if isInput {
		return schemaprofile.InputCompatible(reqN, provN)
	}
	return schemaprofile.OutputCompatible(reqN, provN)
}

// interfaceDocView renders an Interface as its plain decoded-JSON document
// view (the shape JSON Pointer fragments resolve against). Returns nil when
// the interface cannot round-trip, leaving fragment refs unresolvable —
// which then surfaces as the schema check's RefError.
func interfaceDocView(i *Interface) any {
	data, err := json.Marshal(i)
	if err != nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	return v
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
