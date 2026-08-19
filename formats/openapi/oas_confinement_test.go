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

// ---- block 8g: the URef round ---------------------------------------------
//
// Mechanism (c). A reference RESOLUTION failure reaches neither earlier
// mechanism: kin accepts `{"$ref": "#/x"}` at the unmarshal oracle and fails
// later while resolving it, with a report that matches no seam-C pattern. The
// ladder already classifies the position (URef at the referencing site), so
// the round neutralises exactly the sites whose verdict CLIMBS.
//
// The round AUTHORS a value, so it alone is gated on EMISSION: the confined
// tree is loaded twice, differing only at the authored positions, and this
// engine's own emitter decides whether the difference reaches emitted content.
//
// The cases bite in both directions, and the mutation each one answers is
// named so a later reader does not have to guess:
//
//   - UNDER-fire: delete the (c) block and
//     URefClimbingSchemaPositionConfines,
//     URefWithSiblingsConfinesAndKeepsTheSiblings,
//     URefExcludedRequestMediaUnitStillConfines,
//     URefProjectingExcludedUnitStillConfines and
//     URefNonSuccessResponseRouteStillConfines go red, because the loader
//     refuses the whole artifact again.
//   - OVER-fire: populate ClimbingURefSites from the PROJECTING sink as well
//     and URefProjectingPositionIsNeverConfined goes red; populate it from the
//     whole raw tree rather than the ladder's own closure walk and
//     URefUnreachedByAnyUnitIsNeverConfined goes red; neutralise the EMISSION
//     GATE (admit without asking the emitter) and both
//     URefEmissionThroughAnUnwalkedChannelIsNeverConfined and
//     URefDualRolePositionIsNeverConfined go red. Every one of those turns a
//     confinement into salvage; and restore the deleted subtraction over
//     `Projections` guarded on `Disposition == "invalid"` and
//     URefProjectingExcludedUnitStillConfines goes red, because that guard
//     abandons a pass over a unit that emits nothing.
//
// URefEmissionThroughAnUnwalkedChannelIsNeverConfined is the case no rail
// keyed on the ladder's own traversal can pass. Its three channels -- a
// Parameter Object's `content` form, a success response that is a Reference
// Object, and a `requestBody` that is a Reference Object -- are all ordinary
// OAS, all reach the SAME authored position from a surviving unit, and none of
// them is visited by `closureDefects`, so the position appears in no
// `Projections` and no subtraction over that map can see it. It is red under
// exactly the mutation a `Projections`-route case cannot be red under, and
// that asymmetry is the reason it exists.

// The elmasy shape: a success response's only media alternative carries a
// schema `$ref` that identifies no location. The defect climbs, so the
// operation is an invalid target and the intact sibling survives.
func TestConfinement_URefClimbingSchemaPositionConfines(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/reaching": {"get": {"operationId": "getReaching", "responses": {"200": {"description": "ok",
	      "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Missing"}}}}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("a climbing URef position must confine: %v", err)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("the intact sibling must synthesize: %v", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["getReaching"]; ok {
		t.Fatalf("the operation whose closure holds the dangling reference must not synthesize: %v", result.Interface.Operations)
	}
	var invalid *openbindings.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Status == openbindings.SynthesisInvalid {
			invalid = &result.Coverage.Entries[i]
		}
	}
	if invalid == nil {
		t.Fatalf("the invalid unit must be entried, never silently dropped: %+v", result.Coverage.Entries)
	}
	if invalid.SourceRef != "#/paths/~1reaching/get" || invalid.ReasonCode != invalidUnitReasonCode {
		t.Errorf("entry %q/%q, want #/paths/~1reaching/get / %s", invalid.SourceRef, invalid.ReasonCode, invalidUnitReasonCode)
	}
}

// The ensi-platform shape: a dangling reference AT a success response member.
// The ladder reads that as an invalid declaration that loses no representation
// -- it PROJECTS, and the operation survives -- so neutralising it would put an
// authored value into shipped content. The pass must decline and the loader's
// own error must stand.
func TestConfinement_URefProjectingPositionIsNeverConfined(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/proj": {"get": {"operationId": "getProj", "responses": {"200": {"$ref": "#/components/responses/Missing"}}}}
	  }
	}`
	_, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a URef position whose unit SURVIVES must never be confined")
	}
	if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// A dangling reference inside a component no unit's closure walk reaches. The
// ladder classifies nothing there, so there is no attribution to confine
// under, and the pass must decline. This is the algorand shape.
func TestConfinement_URefUnreachedByAnyUnitIsNeverConfined(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Orphan": {"type": "object", "properties": {"p": {"$ref": "#/components/schemas/Missing"}}}}},
	  "paths": {"/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}}}
	}`
	_, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a URef position no unit reaches must never be confined")
	}
	if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// The spiceai shape: the dangling `$ref` carries a sibling. Seam C's
// bare-Reference-Object restriction does not carry over -- the target does not
// exist, so there is no composition to discard -- and the sibling is left where
// it is rather than the position being rewritten.
func TestConfinement_URefWithSiblingsConfinesAndKeepsTheSiblings(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/sib": {"get": {"operationId": "getSib",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Missing", "description": "kept"}}],
	      "responses": {"200": {"description": "ok"}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("a climbing URef position carrying siblings must confine: %v", err)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("the intact sibling must synthesize: %v", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["getSib"]; ok {
		t.Fatalf("the operation carrying the dangling reference must not synthesize: %v", result.Interface.Operations)
	}
}

// A position with TWO ROLES. `ClimbingURefSites` is keyed by POSITION while the
// ladder's verdict is per UNIT, so one position inside a shared component can
// climb for one unit and PROJECT on another unit that survives and is emitted.
//
// The three single-role cases above are structurally blind to this: each
// perturbs a position that has exactly one role, so each can be satisfied by a
// set that ignores the per-unit split entirely. Ungated, this document
// synthesizes `getSurvive` with `output.anyOf[0].properties.x = {}` -- a value
// the pass authored by deleting a `$ref` member inside SHIPPED content, and a
// divergence from TypeScript, which refuses the same document under OBI-D-16.
//
// The nocodb shape: the corpus reported this class as an unpredicted refusal
// text move, and record 95 read the signal as a prediction miss.
//
// This case reaches the surviving unit through a channel the closure walk DOES
// visit, which is why it could be closed by a subtraction over `Projections`
// and why that subtraction looked complete. Its sibling below reaches the same
// position through channels the walk does not visit, and no such subtraction
// can be red for it. Both are red under the emission gate's removal, which is
// the point: one rail answers both, because the rail no longer depends on how
// the position was reached.
func TestConfinement_URefDualRolePositionIsNeverConfined(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {
	    "Shared": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Missing"}}}
	  }},
	  "paths": {
	    "/climb": {"get": {"operationId": "getClimb",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
	      "responses": {"200": {"description": "ok"}}}},
	    "/survive": {"get": {"operationId": "getSurvive",
	      "responses": {"200": {"description": "ok", "content": {
	        "application/json": {"schema": {"$ref": "#/components/schemas/Shared"}},
	        "text/plain": {"schema": {"type": "string"}}
	      }}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a URef position a SURVIVING unit still emits must never be confined; synthesized %v",
			result.Interface.Operations)
	}
	if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's original error must stand, got %q", err)
	}
}

// THE EMISSION-EXERCISING RED PROOF.
//
// The same authored position as the dual-role case -- one dangling reference
// inside a shared component, climbing on `/climb` -- reached by a SURVIVING
// unit through three channels `closureDefects` never walks:
//
//	P1 reads `parameters[i]["schema"]` only, so a Parameter Object's `content`
//	form is never handed to the closure walk (both the operation's own
//	parameters and the Path Item's).
//
//	A success response's media map is read only when the response value is not
//	a Reference Object, so a RESOLVABLE `components/responses` reference falls
//	straight through and its content is never walked.
//
//	The request-alternative loop is likewise guarded on the requestBody not
//	being a Reference Object, so a `components/requestBodies` reference is
//	never walked either.
//
// In all three the loader resolves the reference and the synthesizer emits
// through it, so the authored `{}` lands in a surviving, emitted, represented
// unit -- while the position appears in no unit's `Projections` at all. That
// is what makes this the case a rail keyed on the ladder's walk cannot pass and
// a rail keyed on EMISSION passes without knowing any of the above.
//
// Measured at all three engines before the gate existed: every one of these
// documents is REFUSED by openbindings-go at the landed head and by
// openbindings-ts under OBI-D-16, and every one SYNTHESIZES the authored `{}`
// under a subtraction over `Projections`.
func TestConfinement_URefEmissionThroughAnUnwalkedChannelIsNeverConfined(t *testing.T) {
	const climbing = `"/climb": {"get": {"operationId": "getClimb",
		  "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
		  "responses": {"200": {"description": "ok"}}}}`
	const sharedSchemas = `"Shared": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Missing"}}}`

	for _, tc := range []struct {
		name       string
		components string
		surviving  string
	}{
		{
			name:       "parameter content form, operation level",
			components: `"schemas": {` + sharedSchemas + `}`,
			surviving: `"/survive": {"get": {"operationId": "getSurvive",
			  "parameters": [{"name": "q", "in": "query", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}],
			  "responses": {"200": {"description": "ok"}}}}`,
		},
		{
			name:       "parameter content form, path item level",
			components: `"schemas": {` + sharedSchemas + `}`,
			surviving: `"/survive": {
			  "parameters": [{"name": "q", "in": "query", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}],
			  "get": {"operationId": "getSurvive", "responses": {"200": {"description": "ok"}}}}`,
		},
		{
			name: "success response is a Reference Object",
			components: `"schemas": {` + sharedSchemas + `},
			  "responses": {"OK": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}}`,
			surviving: `"/survive": {"get": {"operationId": "getSurvive",
			  "responses": {"200": {"$ref": "#/components/responses/OK"}}}}`,
		},
		{
			name: "request body is a Reference Object",
			components: `"schemas": {` + sharedSchemas + `},
			  "requestBodies": {"Body": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}}`,
			surviving: `"/survive": {"post": {"operationId": "postSurvive",
			  "requestBody": {"$ref": "#/components/requestBodies/Body"},
			  "responses": {"200": {"description": "ok"}}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := `{
			  "openapi": "3.0.3",
			  "info": {"title": "T", "version": "1"},
			  "components": {` + tc.components + `},
			  "paths": {` + climbing + `,` + tc.surviving + `}
			}`
			result, err := synthesizeRaw(content)
			if err == nil {
				t.Fatalf("a position a SURVIVING unit EMITS must never be confined, whatever channel carries it; synthesized %v",
					result.Interface.Operations)
			}
			if !strings.Contains(err.Error(), "Missing") {
				t.Errorf("the loader's original error must stand, got %q", err)
			}
		})
	}
}

// The converse obligation, and the reason the gate asks the emitter rather than
// the ladder's dispositions. A unit whose every declared request media
// alternative is invalidated is ADDRESSED -- it is not `invalid` -- but it
// emits no operation, so a position it merely PROJECTS costs shipped content
// nothing and the confinement must still be admitted.
//
// Here `/excluded` is excluded for a reason of its own (its only request media
// alternative carries a D15 keyword), while its success response declares one
// dead alternative and one that survives, so the shared position lands in that
// unit's `Projections` with the unit NOT `invalid`. A subtraction over
// `Projections` guarded on `Disposition == "invalid"` therefore removes the
// position, abandons the whole pass, and refuses a source TypeScript converts.
// Measured at the parked engine: REFUSED. Measured here and in TypeScript:
// `{"clean.get": {"output": {"type": "string"}}}`.
//
// The emission gate reaches the right answer without consulting a disposition
// at all: asked, the emitter emits nothing from that unit.
func TestConfinement_URefProjectingExcludedUnitStillConfines(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Shared": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Missing"}}}}},
	  "paths": {
	    "/climb": {"get": {"operationId": "getClimb",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
	      "responses": {"200": {"description": "ok"}}}},
	    "/excluded": {"post": {"operationId": "postExcluded",
	      "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"m": []}}}}},
	      "responses": {"200": {"description": "ok", "content": {
	        "application/json": {"schema": {"$ref": "#/components/schemas/Shared"}},
	        "text/plain": {"schema": {"type": "string"}}
	      }}}}},
	    "/clean": {"get": {"operationId": "getClean", "responses": {"200": {"description": "ok",
	      "content": {"text/plain": {"schema": {"type": "string"}}}}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("a position only an ADDRESSED unit that emits nothing projects must not block the confinement: %v", err)
	}
	if _, ok := result.Interface.Operations["getClean"]; !ok {
		t.Fatalf("the intact sibling must synthesize: %v", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["postExcluded"]; ok {
		t.Fatalf("the request-media-excluded unit must not synthesize: %v", result.Interface.Operations)
	}
}

// The simpler excluded shape, kept because it holds the (c) block itself: the
// position enters `ClimbingURefSites` through the invalid-alternative sink and
// no unit projects it at all, so this one is red only when the round is
// disabled.
func TestConfinement_URefExcludedRequestMediaUnitStillConfines(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Shared": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Missing"}}}}},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/excluded": {"post": {"operationId": "postExcluded",
	      "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}},
	      "responses": {"200": {"description": "ok"}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("an ADDRESSED unit that emits nothing must not block the confinement: %v", err)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("the intact sibling must synthesize: %v", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["postExcluded"]; ok {
		t.Fatalf("the request-media-excluded unit must not synthesize: %v", result.Interface.Operations)
	}
}

// The same obligation on the other route the ladder does not walk. A non-success
// response contributes nothing to the emitted contract, so a position that lives
// only there costs shipped content nothing even though the unit survives and is
// emitted. Measured at parity with openbindings-ts, which synthesizes this
// document too.
func TestConfinement_URefNonSuccessResponseRouteStillConfines(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Shared": {"type": "object", "properties": {"x": {"$ref": "#/components/schemas/Missing"}}}}},
	  "paths": {
	    "/climb": {"get": {"operationId": "getClimb",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
	      "responses": {"200": {"description": "ok"}}}},
	    "/survive": {"get": {"operationId": "getSurvive", "responses": {
	      "200": {"description": "ok", "content": {"text/plain": {"schema": {"type": "string"}}}},
	      "500": {"description": "err", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}
	    }}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("a position only a non-success response reaches must not block the confinement: %v", err)
	}
	if _, ok := result.Interface.Operations["getSurvive"]; !ok {
		t.Fatalf("the surviving unit must synthesize: %v", result.Interface.Operations)
	}
}
