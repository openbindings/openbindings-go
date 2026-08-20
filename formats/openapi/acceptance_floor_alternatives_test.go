package openapi

// Block 8d-3: a ladder-invalid request media ALTERNATIVE is a unit that is
// malformed under its upstream authority, so the operation must not carry it.
// It never climbs, which is what the two cases here separate:
//
//   - an OPTIONAL body whose only alternative is invalid: the operation is
//     still represented, and the invalid alternative is simply not in its
//     input.
//   - a REQUIRED body whose only alternative is invalid: no candidate carriage
//     remains, so the operation falls to the existing OAPI-P-04 exclusion,
//     under `openapi.unresolvable_request_body` and not misreported as a
//     flattening collision.
//
// Both bite in both directions: dropping the filter reddens the first (the
// defective member reappears in the emitted input) and the second (the
// operation is emitted rather than excluded).

import (
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"
)

func alternativesDocument(required bool) string {
	requiredText := "false"
	if required {
		requiredText = "true"
	}
	return `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {"/a": {"post": {
	    "operationId": "postA",
	    "requestBody": {"required": ` + requiredText + `, "content": {"application/json": {
	      "schema": {"type": "object", "properties": {"f": {"type": "string", "required": true}}}}}},
	    "responses": {"200": {"description": "ok"}}
	  }}}
	}`
}

func TestAcceptanceFloor_InvalidAlternativeIsNotCarried(t *testing.T) {
	result := synthesizeWithCoverage(t, alternativesDocument(false))
	op, ok := result.Interface.Operations["postA"]
	if !ok {
		t.Fatalf("an OPTIONAL body's invalid alternative must not cost the operation: %v", result.Interface.Operations)
	}
	if input, isMap := any(op.Input).(map[string]any); isMap {
		if properties, has := input["properties"].(map[string]any); has {
			if _, carried := properties["f"]; carried {
				t.Errorf("the invalid alternative must not reach the emitted input: %v", op.Input)
			}
		}
	}
	var alternative *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Scope == synthesize.SynthesisCoverageAlternative {
			alternative = &result.Coverage.Entries[i]
		}
	}
	if alternative == nil || alternative.Status != synthesize.SynthesisInvalid || alternative.ReasonCode != invalidUnitReasonCode {
		t.Fatalf("the alternative must still be accounted as invalid: %+v", result.Coverage.Entries)
	}
	if alternative.SourceRef != "#/paths/~1a/post/requestBody/content/application~1json" {
		t.Errorf("alternative sourceRef %q", alternative.SourceRef)
	}
}

func TestAcceptanceFloor_RequiredBodyWithOnlyInvalidAlternativesIsExcluded(t *testing.T) {
	result := synthesizeWithCoverage(t, alternativesDocument(true))
	if _, ok := result.Interface.Operations["postA"]; ok {
		t.Fatalf("a REQUIRED body with no surviving alternative must exclude the operation")
	}
	var target *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		entry := &result.Coverage.Entries[i]
		if entry.Scope == synthesize.SynthesisCoverageTarget && entry.SourceRef == "#/paths/~1a/post" {
			target = entry
		}
	}
	if target == nil {
		t.Fatalf("the excluded target must be entried: %+v", result.Coverage.Entries)
	}
	if target.Status != synthesize.SynthesisExcluded || target.ReasonCode != "openapi.unresolvable_request_body" {
		t.Errorf("target entry %q/%q, want excluded / openapi.unresolvable_request_body", target.Status, target.ReasonCode)
	}
	if result.Coverage.FullyRepresented {
		t.Errorf("fullyRepresented must be cleared")
	}
}
