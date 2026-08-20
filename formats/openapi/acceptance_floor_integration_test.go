package openapi

// Integration tests for the acceptance floor's evidenced surfaces (block
// 8d-1): Scenario B confinement — the affected operation is entried as
// `invalid` under `openapi.invalid_unit`, siblings synthesize — and the §3
// part-2 whole-source refusal, on both synthesis surfaces.

import (
	"context"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

func synthesizeWithCoverage(t *testing.T, content string) *synthesize.SynthesizeResult {
	t.Helper()
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(content)}},
	})
	if err != nil {
		t.Fatalf("SynthesizeInterfaceWithCoverage: %v", err)
	}
	return result
}

// Scenario B (the ratified acceptance-floor ruling §8a): a ladder-invalid
// operation confines to itself — an `invalid` target entry with the owning
// unit as sourceRef and its defects in details — while its siblings
// synthesize.
func TestAcceptanceFloor_InvalidTargetEntriedSiblingsSynthesize(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {
	      "get": {
	        "operationId": "getGood",
	        "responses": {"200": {"description": "ok"}}
	      }
	    },
	    "/bad": {
	      "get": {
	        "operationId": "getBad",
	        "responses": {
	          "200": {
	            "description": "ok",
	            "content": {"application/json": {"schema": {"type": "int"}}}
	          }
	        }
	      }
	    }
	  }
	}`
	result := synthesizeWithCoverage(t, content)
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("sibling operation must synthesize: %v", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["getBad"]; ok {
		t.Fatalf("ladder-invalid operation must not synthesize")
	}
	var invalid *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		e := &result.Coverage.Entries[i]
		if e.Status == synthesize.SynthesisInvalid {
			invalid = e
		}
	}
	if invalid == nil {
		t.Fatalf("expected an invalid coverage entry; entries: %+v", result.Coverage.Entries)
	}
	if invalid.SourceRef != "#/paths/~1bad/get" {
		t.Errorf("invalid entry sourceRef %q, want the owning unit #/paths/~1bad/get", invalid.SourceRef)
	}
	if invalid.Scope != synthesize.SynthesisCoverageTarget {
		t.Errorf("invalid entry scope %q, want target", invalid.Scope)
	}
	if invalid.ReasonCode != "openapi.invalid_unit" {
		t.Errorf("invalid entry reasonCode %q, want openapi.invalid_unit", invalid.ReasonCode)
	}
	defects, _ := invalid.Details["defects"].([]any)
	if len(defects) != 1 {
		t.Fatalf("invalid entry defects: %#v", invalid.Details)
	}
	defect, _ := defects[0].(map[string]any)
	if defect["position"] != "#/paths/~1bad/get/responses/200/content/application~1json/schema/type" {
		t.Errorf("defect position %q", defect["position"])
	}
	if authority, _ := defect["authority"].(string); !strings.Contains(authority, "OAS 3.0 line") {
		t.Errorf("defect authority %q", authority)
	}
	if result.Coverage.FullyRepresented {
		t.Error("an invalid entry must clear fullyRepresented")
	}
}

// The §3 part-2 derived rule: when every declared target is invalid, the
// whole source refuses — on the tolerant surface too.
func TestAcceptanceFloor_WholeSourceRefusalZeroSurvivors(t *testing.T) {
	content := `{
	  "openapi": "3.1.0",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/only": {
	      "get": {
	        "responses": {
	          "200": {
	            "content": {"application/json": {"schema": {"type": "object"}}}
	          }
	        }
	      }
	    }
	  }
	}`
	_, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(content)}},
	})
	if err == nil {
		t.Fatal("expected the §3 part-2 whole-source refusal: the sole declared target's success response omits REQUIRED description and declares a body")
	}
	if !strings.Contains(err.Error(), "whole-source refusal") {
		t.Errorf("refusal should cite §3 part 2, got: %v", err)
	}
}

// The emptiness carve-out: `paths: {}` is conforming in all eight editions
// and yields an empty interface, never a refusal.
func TestAcceptanceFloor_EmptyPathsAccepted(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {}
	}`
	result := synthesizeWithCoverage(t, content)
	if len(result.Interface.Operations) != 0 {
		t.Fatalf("expected an empty interface, got %v", result.Interface.Operations)
	}
	if !result.Coverage.FullyRepresented {
		t.Error("an empty conforming inventory is fully represented")
	}
}

// The excluded-is-addressed carve-out (the go-fuego shape): a REQUIRED body
// whose every declared alternative is ladder-invalid keeps the existing
// openapi.unresolvable_request_body exclusion — an ADDRESSED target that
// never triggers part 2 — and the alternatives now carry `invalid` entries
// with their defects.
func TestAcceptanceFloor_ExcludedTargetCountsAsAddressed(t *testing.T) {
	content := `{
	  "openapi": "3.1.0",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/pets": {
	      "post": {
	        "operationId": "createPet",
	        "requestBody": {
	          "required": true,
	          "content": {"and-another": {"schema": {"type": "string"}}}
	        },
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`
	result := synthesizeWithCoverage(t, content)
	if len(result.Interface.Operations) != 0 {
		t.Fatalf("the operation's required body has no faithful carriage; expected exclusion, got %v", result.Interface.Operations)
	}
	var excluded, invalidAlt *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		e := &result.Coverage.Entries[i]
		switch {
		case e.Status == synthesize.SynthesisExcluded && e.Scope == synthesize.SynthesisCoverageTarget:
			excluded = e
		case e.Status == synthesize.SynthesisInvalid && e.Scope == synthesize.SynthesisCoverageAlternative:
			invalidAlt = e
		}
	}
	if excluded == nil {
		t.Fatalf("expected the excluded target entry; entries: %+v", result.Coverage.Entries)
	}
	if excluded.ReasonCode != "openapi.unresolvable_request_body" {
		t.Errorf("excluded target reasonCode %q", excluded.ReasonCode)
	}
	if invalidAlt == nil {
		t.Fatalf("expected an invalid alternative entry; entries: %+v", result.Coverage.Entries)
	}
	if invalidAlt.SourceRef != "#/paths/~1pets/post/requestBody/content/and-another" {
		t.Errorf("invalid alternative sourceRef %q", invalidAlt.SourceRef)
	}
	if invalidAlt.ReasonCode != "openapi.invalid_unit" {
		t.Errorf("invalid alternative reasonCode %q", invalidAlt.ReasonCode)
	}
}

// F-O1-13, the ruled outcome: a boolean-literal part schema on the 3.0 line is
// not a Schema Object there (that line's Wright Draft 00 subset grants a
// boolean only at `additionalProperties`), so it is a defect that confines to
// the request media alternative owning it — an `invalid` alternative entry
// under openapi.invalid_unit — while the operation stays represented on its
// healthy sibling alternative and the SOURCE IS ACCEPTED. The Go engines used
// to refuse the whole source here, because kin-openapi cannot hold a boolean
// SchemaRef and the boolean-schema lift was gated to the 3.1 line; the lift is
// now scoped to exactly the positions this instrument classifies.
func TestAcceptanceFloor_BooleanPartSchemaOn30ConfinesToItsAlternative(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/uploads": {
	      "post": {
	        "operationId": "upload",
	        "requestBody": {
	          "required": true,
	          "content": {
	            "multipart/form-data": {"schema": {"type": "object", "properties": {"note": true, "label": {"type": "string"}}}},
	            "application/json": {"schema": {"type": "object"}}
	          }
	        },
	        "responses": {"204": {"description": "stored"}}
	      }
	    }
	  }
	}`
	result := synthesizeWithCoverage(t, content)
	if _, ok := result.Interface.Operations["upload"]; !ok {
		t.Fatalf("the operation must survive on its sibling alternative: %v", result.Interface.Operations)
	}
	var invalidAlt *synthesize.SynthesisCoverageEntry
	represented := 0
	for i := range result.Coverage.Entries {
		e := &result.Coverage.Entries[i]
		if e.Status == synthesize.SynthesisInvalid && e.Scope == synthesize.SynthesisCoverageAlternative {
			invalidAlt = e
		}
		if e.Status == synthesize.SynthesisRepresented && e.Scope == synthesize.SynthesisCoverageAlternative {
			represented++
		}
	}
	if invalidAlt == nil {
		t.Fatalf("expected an invalid alternative entry; entries: %+v", result.Coverage.Entries)
	}
	if invalidAlt.SourceRef != "#/paths/~1uploads/post/requestBody/content/multipart~1form-data" {
		t.Errorf("invalid alternative sourceRef %q", invalidAlt.SourceRef)
	}
	if invalidAlt.ReasonCode != "openapi.invalid_unit" {
		t.Errorf("invalid alternative reasonCode %q, want openapi.invalid_unit", invalidAlt.ReasonCode)
	}
	if represented != 1 {
		t.Errorf("represented alternatives = %d, want 1 (the healthy JSON sibling)", represented)
	}
}

// A projection-class defect (D11: a component key violating the grammar)
// reached by a surviving unit's closure yields a projection-scope invalid
// entry on that unit, and the unit survives. (The D6 shape exercises the
// same tier through the shared case table; its carrier documents refuse at
// today's Go load and flip under 8d-2's confinement.)
func TestAcceptanceFloor_ReachingUnitProjectionEntry(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Bad Key": {"type": "object"}}},
	  "paths": {
	    "/a": {
	      "get": {
	        "operationId": "getA",
	        "responses": {
	          "200": {
	            "description": "ok",
	            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Bad Key"}}}
	          }
	        }
	      }
	    }
	  }
	}`
	result := synthesizeWithCoverage(t, content)
	if _, ok := result.Interface.Operations["getA"]; !ok {
		t.Fatalf("the reaching unit survives: %v", result.Interface.Operations)
	}
	var projection *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		e := &result.Coverage.Entries[i]
		if e.Scope == synthesize.SynthesisCoverageProjection && e.Status == synthesize.SynthesisInvalid {
			projection = e
		}
	}
	if projection == nil {
		t.Fatalf("expected a projection entry; entries: %+v", result.Coverage.Entries)
	}
	if projection.SourceRef != "#/paths/~1a/get" {
		t.Errorf("projection entry sourceRef %q, want the reaching unit", projection.SourceRef)
	}
	if projection.ReasonCode != "openapi.invalid_unit" {
		t.Errorf("projection reasonCode %q", projection.ReasonCode)
	}
	if result.Coverage.FullyRepresented {
		t.Error("a projection entry must clear fullyRepresented")
	}
}
