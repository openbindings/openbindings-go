package openapi

// Integration tests for the load-path confinement (block 8d-2). Every case
// runs through the shipped synthesis entry, so it measures the seam and not
// the pass in isolation.
//
// These tests are deliberately written to bite in BOTH directions and without
// any dependence on the shared 66-cell case table, which block 8d-1 proved
// cannot redden under an over-firing mutation (record 80, FIX 7):
//
//   - UNDER-fire: disable the pass and the four cases that require it to fire
//     go red, because the loader refuses the whole artifact again.
//   - OVER-fire: remove the ladder-attribution rail and
//     TestConfinement_UnattributedDefectRefusesWithTheOriginalError goes red,
//     because an unrostered defect would then be silently neutralised --
//     exactly the salvage the ruling forbids.

import (
	"context"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func synthesizeRaw(content string) (*openbindings.SynthesizeResult, error) {
	return NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(content)}},
	})
}

// The Kong shape: an HTTP-method member that is an empty ARRAY. kin-openapi
// refuses the whole document at unmarshal; the ladder owns the position (D2b,
// P3), so the pass neutralises it and the intact sibling survives.
const confinementD2bDocument = `{
  "openapi": "3.0.3",
  "info": {"title": "T", "version": "1"},
  "paths": {
    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
    "/bad": {"get": []}
  }
}`

func TestConfinement_MethodMemberArrayConfinesAndSiblingSurvives(t *testing.T) {
	result, err := synthesizeRaw(confinementD2bDocument)
	if err != nil {
		t.Fatalf("confinement must let the intact sibling load: %v", err)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("sibling operation must synthesize: %v", result.Interface.Operations)
	}
	if len(result.Interface.Operations) != 1 {
		t.Fatalf("the confined target must not synthesize: %v", result.Interface.Operations)
	}
	var invalid *openbindings.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Status == openbindings.SynthesisInvalid {
			invalid = &result.Coverage.Entries[i]
		}
	}
	if invalid == nil {
		t.Fatalf("the confined position must be entried, never silently dropped: %+v", result.Coverage.Entries)
	}
	if invalid.SourceRef != "#/paths/~1bad/get" || invalid.ReasonCode != invalidUnitReasonCode {
		t.Errorf("entry %q/%q, want #/paths/~1bad/get / %s", invalid.SourceRef, invalid.ReasonCode, invalidUnitReasonCode)
	}
	if result.Coverage.FullyRepresented {
		t.Errorf("fullyRepresented must be cleared by the invalid entry")
	}
}

// The rail that makes this a confinement and not salvage. A `components.schemas`
// member that is a bare STRING is one of the shape table's `hits: []` cells
// (`C2-component-value-string`): the position is a defective one no shipped
// class names, so nothing attributes it and the load must refuse with the
// loader's own error rather than neutralise the member.
func TestConfinement_UnattributedDefectRefusesWithTheOriginalError(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Thing": "not a schema"}},
	  "paths": {"/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}}}
	}`
	_, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a defect no shipped class attributes must never be confined")
	}
	if !strings.Contains(err.Error(), "failed to unmarshal") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// Block 8d-3: the registry-scoped class D15 -- a Schema Object keyword whose
// value violates the governing dialect's declared JSON type. This is the Kong
// shape at the schema rung: `properties/member` holds an ARRAY, which kin
// refuses at unmarshal. The ladder now owns the position, so the pass
// neutralises it, the operation whose closure REACHES it becomes an invalid
// target, and the operation that does not reach it survives.
func TestConfinement_D15SchemaKeywordConfinesAndReachIsAttributed(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Thing": {"type": "object", "properties": {"member": []}}}},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/reaching": {"get": {"operationId": "getReaching", "responses": {"200": {"description": "ok",
	      "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Thing"}}}}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("a D15 position the ladder owns must confine: %v", err)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("the non-reaching sibling must synthesize: %v", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["getReaching"]; ok {
		t.Fatalf("the reaching operation must not synthesize: %v", result.Interface.Operations)
	}
	var invalid *openbindings.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Status == openbindings.SynthesisInvalid {
			invalid = &result.Coverage.Entries[i]
		}
	}
	if invalid == nil {
		t.Fatalf("the reaching unit must be entried, never silently dropped: %+v", result.Coverage.Entries)
	}
	if invalid.SourceRef != "#/paths/~1reaching/get" || invalid.ReasonCode != invalidUnitReasonCode {
		t.Errorf("entry %q/%q, want #/paths/~1reaching/get / %s", invalid.SourceRef, invalid.ReasonCode, invalidUnitReasonCode)
	}
	if result.Coverage.FullyRepresented {
		t.Errorf("fullyRepresented must be cleared by the invalid entry")
	}
}

// Seam C, schema position: the C4 shape. `#/components/responses/ThingResponse`
// is referenced from a response SCHEMA position. kin reports a
// reference-target-kind defect naming the target; the referencing site is
// inlined with the value the pointer denotes, which on the 3.0 line is
// admitted because the target is a D6 hit AND is Schema-shaped: it carries
// Schema Object keywords (`type`, `properties`) and no Response Object fixed
// field. Both halves are load-bearing -- see the companion case below.
func TestConfinement_SeamCSchemaPositionInlinesTheDenotedValue(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"responses": {"ThingResponse": {"type": "object", "properties": {"id": {"type": "string"}}}}},
	  "paths": {
	    "/things": {
	      "get": {
	        "operationId": "listThings",
	        "responses": {
	          "200": {
	            "description": "ok",
	            "content": {"application/json": {"schema": {"$ref": "#/components/responses/ThingResponse"}}}
	          }
	        }
	      }
	    }
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("seam C must close this load: %v", err)
	}
	operation, ok := result.Interface.Operations["listThings"]
	if !ok {
		t.Fatalf("operation must synthesize: %v", result.Interface.Operations)
	}
	if operation.Output == nil {
		t.Fatalf("the inlined value must reach the emitted output schema")
	}
	// D6 attaches one projection entry to the reaching unit.
	projections := 0
	for _, e := range result.Coverage.Entries {
		if e.Scope == openbindings.SynthesisCoverageProjection && e.Status == openbindings.SynthesisInvalid {
			projections++
		}
	}
	if projections != 1 {
		t.Errorf("want one D6 projection entry on the reaching unit, got %d: %+v", projections, result.Coverage.Entries)
	}
}

// Seam C, response position: the tsuru shape. A Responses Object member is a
// Reference Object resolving to a Schema Object, which the ladder already
// records as D7. The member is removed and the operation keeps its explicit
// 2xx; the D7 entry at that position is what accounts for it.
func TestConfinement_SeamCResponsePositionRemovesTheMemberAndEntriesIt(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Error": {"type": "object"}}},
	  "paths": {
	    "/info": {
	      "get": {
	        "operationId": "getInfo",
	        "responses": {
	          "200": {"description": "ok", "content": {"application/json": {"schema": {"type": "string"}}}},
	          "default": {"$ref": "#/components/schemas/Error"}
	        }
	      }
	    }
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("seam C must close this load: %v", err)
	}
	if _, ok := result.Interface.Operations["getInfo"]; !ok {
		t.Fatalf("the operation keeps its explicit 2xx and must synthesize: %v", result.Interface.Operations)
	}
	var projection *openbindings.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		e := &result.Coverage.Entries[i]
		if e.Scope == openbindings.SynthesisCoverageProjection && e.Status == openbindings.SynthesisInvalid {
			projection = e
		}
	}
	if projection == nil {
		t.Fatalf("the removed response member must be entried: %+v", result.Coverage.Entries)
	}
	if projection.ReasonCode != invalidUnitReasonCode {
		t.Errorf("projection reasonCode %q, want %s", projection.ReasonCode, invalidUnitReasonCode)
	}
}

// The whole-source case: `paths` is a Reference Object (D5), so after
// confinement no target can be addressed and §3 part 2 refuses -- with the
// floor's reason, not kin's unmarshal diagnostic.
func TestConfinement_PathsReferenceObjectRefusesWithThePart2Reason(t *testing.T) {
	content := `{
	  "openapi": "3.1.0",
	  "info": {"title": "T", "version": "1"},
	  "paths": {"$ref": "./routes.yml"}
	}`
	_, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a source with no addressable target must refuse")
	}
	if !strings.Contains(err.Error(), "whole-source refusal (OAPI-P-01") {
		t.Errorf("want the part-2 refusal, got %q", err)
	}
}

// The fast path is untouched: a conforming document never reaches the pass,
// and the pass never changes what a conforming document produces.
func TestConfinement_FastPathUnchanged(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {"/x": {"get": {"operationId": "getX", "responses": {"200": {"description": "ok"}}}}}
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("conforming document must load: %v", err)
	}
	if len(result.Interface.Operations) != 1 || !result.Coverage.FullyRepresented {
		t.Errorf("conforming document must be fully represented with one operation: %+v", result.Coverage)
	}
}

// Seam C, schema position, the 3.0 gate's OTHER half. The target here is a
// description-less RESPONSE Object -- it carries `content`. It is a D6 hit
// (D6's test is only the absence of a string `description`), but it is not
// Schema-shaped, so the 3.0 line must NOT inline it: a Response Object body
// standing where an operation's output SCHEMA belongs, on a line whose dialect
// has no `content` keyword, is not something D6 licenses. The load keeps the
// loader's original error.
//
// This is the case the gate admitted before record 84's F2 narrowing; it
// reddens if `isFloorSchemaShaped` is widened back to "is an object".
func TestConfinement_SeamCSchemaPositionRefusesAResponseShapedTarget(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"responses": {"ThingResponse": {"content": {"application/json": {"schema": {"type": "object"}}}}}},
	  "paths": {
	    "/things": {
	      "get": {
	        "operationId": "listThings",
	        "responses": {
	          "200": {
	            "description": "ok",
	            "content": {"application/json": {"schema": {"$ref": "#/components/responses/ThingResponse"}}}
	          }
	        }
	      }
	    }
	  }
	}`
	_, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a Response-shaped target must not be inlined into a schema position on the 3.0 line")
	}
	if !strings.Contains(err.Error(), "expecting ref to schema object") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}
