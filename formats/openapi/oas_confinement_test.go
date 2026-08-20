package openapi

// Integration tests for the load-path confinement (block 8d-2). Every case
// runs through the shipped synthesis entry, so it measures the seam and not
// the pass in isolation.
//
// These tests are deliberately written to bite in BOTH directions and without
// any dependence on the shared 66-cell case table, which block 8d-1 proved
// cannot redden under an over-firing mutation (record 80, FIX 7):
//
//   - UNDER-fire: disable the pass and the cases that require it to fire go
//     red, because the loader refuses the whole artifact again.
//   - OVER-fire: remove the ladder-attribution rail and
//     TestConfinement_UnattributedDefectRefusesWithTheOriginalError goes red,
//     because an unrostered defect would then be silently neutralised --
//     exactly the salvage the ruling forbids.
//
// Block 8h adds a third direction, and it is the one the two 8g parks were
// about. The pair
// TestConfinement_OracleWalkIntoAnEmittedSchemaIsRefused /
// TestConfinement_OracleWalkOutsideEmittedContentIsAdmitted are the same
// document differing ONLY in whether a SURVIVING unit reaches the authored
// component, and they exercise EMISSION rather than the ladder's walk:
//
//   - stop the ledger recording mechanism (a) and the first goes red, because
//     a component a surviving unit emits ships with a declared member gone.
//   - make the gate refuse unconditionally and the second goes red, because
//     the pass would refuse a confinement nothing emitted can read.
//
// Neither can be satisfied by narrowing or widening a traversal, which is why
// they are written as a pair over one document.
//
// Block 8h's re-run adds three more, all of which exercise EMISSION and none of
// which reads the shared 66-cell table:
//
//   - TestConfinement_ExcludedOperationPositionNothingReadsIsAdmitted /
//     TestConfinement_OperationPositionASurvivingPathItemReadsIsRefused are a
//     second such pair, at a position that has no Schema Object above it at all.
//     The first was DECLINED while the ledger recorded a markable ancestor
//     instead of the authored position; the second goes red if the gate stops
//     asking about the position.
//   - TestConfinement_RequestBodyReferenceObjectInputRootIsRefused is the case
//     the ancestor rule got wrong, and goes red the moment an ancestor is
//     recorded again.
//   - TestConfinement_EveryAcceptedCandidateMustAgree goes red under a ladder
//     that stops at the first candidate the oracle accepts.
//
// And TestConfinement_UnledgeredAuthoringIsNotAccounted exercises the accounting
// obligation directly, because the mechanism it guards against is one that by
// definition cannot be shipped in order to be tested.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/getkin/kin-openapi/openapi3"

	openbindings "github.com/openbindings/openbindings-go"
)

func synthesizeRaw(content string) (*synthesize.SynthesizeResult, error) {
	return NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(content)}},
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

// An Operation Object member of a Path Item, and NOTHING READS IT: the floor
// has already made `#/paths/~1bad/get` an invalid target, so the emitter skips
// it in both images and the removal takes nothing out of shipped content.
//
// The gate says so and the confinement is admitted. Its companion below is the
// same position with a surviving unit reaching it, which is refused -- the pair
// is what distinguishes "the emitter does not read this" from "the gate cannot
// see this position at all", and only the pair can.
//
// This case DECLINED while the ledger recorded a markable Schema Object ANCESTOR
// rather than the position, because an Operation Object has no Schema Object
// above it. Nothing about emission had changed; the pass simply had no
// instrument. That cost `Kong/kong`, `n8n-io/n8n`, `gameap/gameap`,
// `tsuru/tsuru` and `inngest/inngest`, 318 operations, and record 105 measures
// how much of it was the instrument.
func TestConfinement_ExcludedOperationPositionNothingReadsIsAdmitted(t *testing.T) {
	result, err := synthesizeRaw(confinementD2bDocument)
	if err != nil {
		t.Fatalf("nothing emitted reads an excluded operation position: %v", err)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("the intact sibling must synthesize: %v", result.Interface.Operations)
	}
	if len(result.Interface.Operations) != 1 {
		t.Fatalf("the authored position must contribute no operation: %v", result.Interface.Operations)
	}
	var invalid *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Status == synthesize.SynthesisInvalid {
			invalid = &result.Coverage.Entries[i]
		}
	}
	if invalid == nil || invalid.SourceRef != "#/paths/~1bad/get" {
		t.Fatalf("the authored unit must be entried, never silently dropped: %+v", result.Coverage.Entries)
	}
}

// RED PROOF at an Operation position, emission side. The same removal as above,
// with a second Path Item that is a Reference Object denoting the FIRST one --
// so a surviving, emitted unit reads the position the pass authored at, and the
// declared `get` would be gone from what that unit ships.
//
// The engine's own emitter says so: with a candidate at `#/paths/~1bad/get` the
// two images do not emit identically, and the confinement is refused with the
// loader's original error. At the landed head this document SYNTHESIZES with the
// member gone.
//
// Note which channel this is. A Path Item that is a Reference Object is not one
// of the four channels the two 8g parks named, and nothing in the pass names it
// here either. It is caught because the gate asks the emitter about the position
// rather than about the channel.
func TestConfinement_OperationPositionASurvivingPathItemReadsIsRefused(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/bad": {
	      "get": [],
	      "post": {"operationId": "postBad", "responses": {"200": {"description": "ok"}}}
	    },
	    "/other": {"$ref": "#/paths/~1bad"}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a position a surviving unit reads must not ship an authored value: %+v", result.Interface.Operations)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("the loader's ORIGINAL error must stand, got %q", err)
	}
}

// RED PROOF for the whole-body input route, and the case that made the ANCHOR
// rule unsound.
//
// A component schema is reached only through a `requestBody` that is a Reference
// Object. Mechanism (a) removes a member of that schema's `properties`. The
// whole-body input route rebuilds the operation's input from the schema's
// MEMBERS, so the schema's own ROOT keys never reach `input` -- and the anchor
// rule's mark went to exactly that root. The gate compared, reported identical,
// and ADMITTED a document shipping `postSurvive` with a declared member gone,
// the surviving unit `represented` and no limitation.
//
// Marking AT `#/components/schemas/Shared/properties/m` puts the difference
// where the route reads, the images differ, and the confinement is refused. This
// case goes red if the ledger records an ancestor again.
//
// Stored as `corpus-lab/data/oas-mechanism-a-reproducers/n10-*.json`.
func TestConfinement_RequestBodyReferenceObjectInputRootIsRefused(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {
	    "schemas": {"Shared": {"type": "object", "properties": {"x": {"type": "string"}, "m": []}}},
	    "requestBodies": {"B": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}}
	  },
	  "paths": {
	    "/climb": {"get": {"operationId": "getClimb",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
	      "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}}}},
	    "/survive": {"post": {"operationId": "postSurvive",
	      "requestBody": {"$ref": "#/components/requestBodies/B"},
	      "responses": {"200": {"description": "ok"}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("the whole-body input route reads the authored member: %+v", result.Interface.Operations)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("the loader's ORIGINAL error must stand, got %q", err)
	}
}

// RED PROOF for the AND rule. A Path Item member of `#/paths` whose value is an
// array: mechanism (a) removes the whole path.
//
// Three of the four object candidates report NO emitted difference at this
// position -- a Schema Object, a Parameter Object and a Response Object all
// decode there as a Path Item carrying nothing an emitter emits. Only the Path
// Item candidate produces an operation, and it differs. A ladder that stopped at
// the first candidate the oracle accepted would admit this, which is the false
// negative the whole rail exists to prevent; every accepted candidate must
// agree.
//
// This is `Kong/kong`'s shape, and it is why 45 of that specimen's operations
// are a refusal this block does NOT recover.
func TestConfinement_EveryAcceptedCandidateMustAgree(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/targets": []
	  }
	}`
	result, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a Path Item position is read by the emitter and must not be admitted: %+v", result.Interface.Operations)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("the loader's ORIGINAL error must stand, got %q", err)
	}
}

// The accounting obligation, exercised directly because a mechanism that
// authors without a ledger cannot be shipped in order to test it. This is the
// property that replaced a claimed compile obligation the language does not
// supply: `confinementUnledgeredDifference` compares the tree about to be handed
// back against a second parse of the artifact's own bytes.
//
// It goes red if the walk stops descending, if prefix coverage is widened to
// "anything under the root", or if the check is skipped when the ledger is
// empty -- which is exactly the state a forgetful mechanism leaves behind.
func TestConfinement_UnledgeredAuthoringIsNotAccounted(t *testing.T) {
	entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},"paths":{"/x":{"get":{"operationId":"getX","responses":{"200":{"description":"ok"}}}}}}`)
	parse := func() map[string]any {
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("entry must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("entry must be an object")
		}
		return root
	}

	// Unchanged: nothing to account for, with an empty ledger.
	if where, unaccounted := confinementUnledgeredDifference(entry, parse(), newConfinementLedger()); unaccounted {
		t.Errorf("an unchanged tree has nothing unaccounted, got %q", where)
	}

	// A fifth mechanism's change, with no ledger entry anywhere.
	fifth := parse()
	fifth["info"].(map[string]any)["title"] = "AUTHORED BY A MECHANISM WITH NO LEDGER"
	where, unaccounted := confinementUnledgeredDifference(entry, fifth, newConfinementLedger())
	if !unaccounted {
		t.Fatalf("an unledgered change must not be accounted")
	}
	if where != "#/info/title" {
		t.Errorf("the decline must name the position, got %q", where)
	}

	// The same change, recorded. An entry accounts for the position and for
	// everything beneath it, and for nothing else.
	ledger := newConfinementLedger()
	ledger.author("#/info/title")
	if where, unaccounted := confinementUnledgeredDifference(entry, fifth, ledger); unaccounted {
		t.Errorf("a recorded position is accounted, got %q", where)
	}
	elsewhere := newConfinementLedger()
	elsewhere.author("#/info/version")
	if _, unaccounted := confinementUnledgeredDifference(entry, fifth, elsewhere); !unaccounted {
		t.Errorf("an entry at another position accounts for nothing here")
	}

	// A removed member is a difference AT the removed position.
	removed := parse()
	delete(removed["paths"].(map[string]any)["/x"].(map[string]any), "get")
	if where, _ := confinementUnledgeredDifference(entry, removed, newConfinementLedger()); where != "#/paths/~1x/get" {
		t.Errorf("a removal is a difference at the removed position, got %q", where)
	}
}

// RED PROOF FOR THE ACCOUNTING CHECK'S CALL SITE, and the reason it exists is a
// verification finding rather than a design one.
//
// The case above proves that `confinementUnledgeredDifference` COMPUTES
// differences. It calls the helper directly, four times, with hand-built trees,
// and it cannot notice if the pass stops calling it: with
// `confinementAdmit`'s call replaced by `if false`, the entire `formats/openapi`
// suite stayed GREEN (record 107 §6.2). That is exactly the failure block 8h's
// re-run reported about its own perturbation A -- "an unfaithful perturbation
// that passes is worth nothing" -- in the perturbation it did not re-check.
//
// This case exercises `confinementAdmit`, which is the pass's ONE admission
// point and the function the obligation is stated over. It is two-sided on
// purpose:
//
//   - an unledgered difference must DECLINE. Red if the call site is removed,
//     which is the regression the case above cannot see.
//   - the SAME difference, recorded, must ADMIT. Red if the check is widened to
//     decline whatever it is handed, which would make the first half pass for
//     the wrong reason.
//
// What it cannot do is drive the difference in through `confineEntryDocument`,
// because a mechanism that authors without a ledger is by definition one this
// engine does not ship. This is the tightest faithful proof available without
// shipping the defect in order to test it, and it is red under the exact
// perturbation that motivated it.
func TestConfinement_AdmissionCallsTheAccountingCheck(t *testing.T) {
	entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},"paths":{"/x":{"get":{"operationId":"getX","responses":{"200":{"description":"ok"}}}}}}`)
	authored := func() map[string]any {
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("entry must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("entry must be an object")
		}
		// A fifth mechanism's change, reaching into the raw tree the way Go will
		// always let one reach into it.
		root["info"].(map[string]any)["title"] = "AUTHORED BY A MECHANISM WITH NO LEDGER"
		return root
	}
	reload := func(data []byte) (*openapi3.T, error) {
		loader := openapi3.NewLoader()
		return loader.LoadFromData(data)
	}
	admittingGate := func(shipped, marked []byte, floor *acceptanceFloor) (bool, bool) { return true, true }

	tree := authored()
	floor := computeAcceptanceFloor(tree)
	if floor == nil {
		t.Fatal("the edition gate must read this artifact")
	}
	shipped, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	loaded, err := reload(shipped)
	if err != nil {
		t.Fatalf("the authored tree must load: %v", err)
	}

	if _, _, ok := confinementAdmit(entry, tree, newConfinementLedger(), floor, reload, admittingGate, loaded); ok {
		t.Fatalf("admission must consult the accounting check: an unledgered change was ADMITTED")
	}

	ledger := newConfinementLedger()
	ledger.author("#/info/title")
	doc, admitErr, ok := confinementAdmit(entry, tree, ledger, floor, reload, admittingGate, loaded)
	if !ok || admitErr != nil || doc == nil {
		t.Fatalf("a recorded position an admitting gate clears must be admitted, got ok=%v err=%v", ok, admitErr)
	}
}

// RED PROOF FOR THE DENOTATION EXEMPTION, at the half a signature cannot carry.
//
// `confinementInlineDenoted` takes a POINTER rather than a value, so no caller
// can MINT through it. That was claimed to make the exemption structural rather
// than declared, and it does not: nothing in the signature says `site` has
// anything to do with `target`, so any caller could RELOCATE the artifact's own
// value to a position the artifact never put it, past an admitting gate,
// accounted as denoted. A verifier shipped `info.title = "getSurvive"` that way
// in one line (record 107 §6.3).
//
// The site is now checked to BE a Reference Object whose `$ref` names `target`,
// which is what the exemption's justification always asserted. Two-sided:
// relocation must be refused and the shipped seam-C shape must still inline.
func TestConfinement_DenotationRequiresTheSiteToDenoteTheTarget(t *testing.T) {
	entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},
	  "components":{"schemas":{"S":{"type":"object"}}},
	  "paths":{"/survive":{"get":{"operationId":"getSurvive",
	    "responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/S"}}}}}}}}}`)
	parse := func() map[string]any {
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("entry must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("entry must be an object")
		}
		return root
	}

	// RELOCATION: the artifact's own value, at a position that denotes nothing.
	relocate := parse()
	ledger := newConfinementLedger()
	if confinementInlineDenoted(relocate, "#/info/title", "#/paths/~1survive/get/operationId", ledger) {
		t.Fatalf("a site that does not denote the target must never be inlined")
	}
	if title := relocate["info"].(map[string]any)["title"]; title != "T" {
		t.Fatalf("nothing may move at a site that denotes nothing, got %v", title)
	}
	if len(ledger.denoted) != 0 {
		t.Fatalf("a refused inline records nothing, got %v", ledger.denoted)
	}

	// The shipped shape: a bare Reference Object at the position, whose own
	// `$ref` names the target.
	inline := parse()
	site := "#/paths/~1survive/get/responses/200/content/application~1json/schema"
	if !confinementInlineDenoted(inline, site, "#/components/schemas/S", ledger) {
		t.Fatalf("a Reference Object must still be inlined with the value its own pointer names")
	}
	landed, ok := confinementResolveRaw(inline, site)
	if !ok {
		t.Fatalf("the site must still hold a value")
	}
	if landedMap, isMap := landed.(map[string]any); !isMap || landedMap["type"] != "object" {
		t.Fatalf("the target's own value must land, got %v", landed)
	}
	if !ledger.denoted[site] {
		t.Fatalf("an inlined position is recorded as denoted, got %v", ledger.denoted)
	}
	// Both spellings of the target name it.
	fragment := parse()
	if !confinementInlineDenoted(fragment, site, "/components/schemas/S", newConfinementLedger()) {
		t.Fatalf("the fragment spelling must name the same target")
	}
}

// The confinement's purpose, kept proven: the same shape one rung down, where
// the authored position DOES have a markable anchor and nothing emitted reads
// it. This is the under-fire proof -- disable the pass and it goes red.
func TestConfinement_SchemaMemberConfinesAndSiblingSurvives(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Owned": {"type": "object", "properties": {"m": []}}}},
	  "paths": {
	    "/good": {"get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}},
	    "/bad": {"get": {"operationId": "getBad",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Owned"}}],
	      "responses": {"200": {"description": "ok"}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err != nil {
		t.Fatalf("confinement must let the intact sibling load: %v", err)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("sibling operation must synthesize: %v", result.Interface.Operations)
	}
	if len(result.Interface.Operations) != 1 {
		t.Fatalf("the confined target must not synthesize: %v", result.Interface.Operations)
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
	var invalid *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Status == synthesize.SynthesisInvalid {
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
		if e.Scope == synthesize.SynthesisCoverageProjection && e.Status == synthesize.SynthesisInvalid {
			projections++
		}
	}
	if projections != 1 {
		t.Errorf("want one D6 projection entry on the reaching unit, got %d: %+v", projections, result.Coverage.Entries)
	}
}

// Seam C, response position: the tsuru shape. A Responses Object member is a
// Reference Object resolving to a Schema Object, which the ladder already
// records as D7, and seam C removes the member.
//
// That removal AUTHORS -- the Responses Object that remains is not what the
// artifact declared -- and it is REFUSED for the emitter's reason, not for want
// of an instrument: `default` is a success code here (the floor's `has2XX`
// tracks the literal `2XX` range key, and this document declares none), so a
// Response Object at the removed position contributes to the operation's
// `output` and the two images do not emit identically.
//
// Being ENTRIED is not the same as being admissible: 8g deleted the URef round's
// `Projections` subtraction for exactly this reason, and an accounted drop buys
// no more here than it does there. The measured cost is `tsuru/tsuru`, whose
// fifteen `default` members are all of this shape and whose fifteen operations
// do NOT climb -- each is a represented target carrying a projection entry.
func TestConfinement_SeamCResponsePositionIsReadByTheOutputLaneAndIsRefused(t *testing.T) {
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
	_, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("removing a Responses Object member is authoring with no markable anchor and must decline")
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
	var invalid *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Status == synthesize.SynthesisInvalid {
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

// RED PROOF FOR THE URef ROUND'S AUTHORED POSITION, and the case that made
// marking at `<site>/$ref` vacuous.
//
// An unresolvable `$ref` carrying siblings, at a PARAMETER position: the
// parameters lane adds the site to `ClimbingURefSites`, the round removes the
// `$ref` member, and what is left -- `{name, in, schema}` -- is a fully valid
// Parameter Object the artifact's own OAS 3.0 semantics say is not there, since
// `$ref` siblings are ignored on that line. A second unit reaches the position
// through a Reference-Object parameter, which the floor `continue`s past and the
// emitter resolves.
//
// Recording `site + "/$ref"` put every candidate where a `$ref` KEY's value goes.
// kin reads only a string there: five of the six candidates decoded into the
// containing object's `Extensions`, which a Parameter Object does not carry into
// emitted content, so five rounds reported IDENTICAL without reading the
// position at all; the sixth made the site a Reference Object denoting nothing,
// so its image would not load and the round was INCONCLUSIVE. The gate marked the
// position shown off vacuous evidence and ADMITTED -- shipping
// `getSurvive.input.properties.AUTHORED`, a name no reader of this artifact
// could obtain, with the unit `represented` and `exhaustive=true`, while the two
// heads it superseded both refused the same document and TypeScript emitted the
// operation with no input at all.
//
// Recording the SITE puts the candidate at the Reference Object's own position,
// where the images differ by the presence of a whole member in a decoding
// context the emitter reads. This case goes red the moment the `$ref` member is
// recorded again.
//
// Stored as `corpus-lab/data/oas-mechanism-a-reproducers/n12-*.json`. Its
// schema-position control is `n13`, which the vacuous gate already refused --
// a Schema Object's extensions ARE carried, so the discriminating power of a
// `$ref` position varies by container, silently, which is the finding this case
// pins down rather than the fix.
func TestConfinement_URefParameterPositionReadByAReferenceParameterIsRefused(t *testing.T) {
	content := `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"parameters": {"Present": {"name": "q", "in": "query", "schema": {"type": "string"}}}},
	  "paths": {
	    "/climb": {"get": {"operationId": "getClimb",
	      "parameters": [{"$ref": "#/components/parameters/Missing", "name": "AUTHORED", "in": "query", "schema": {"type": "string"}}],
	      "responses": {"200": {"description": "ok"}}}},
	    "/survive": {"get": {"operationId": "getSurvive",
	      "parameters": [{"$ref": "#/paths/~1climb/get/parameters/0"}],
	      "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"type": "string"}}}}}}}
	  }
	}`
	result, err := synthesizeRaw(content)
	if err == nil {
		t.Fatalf("a parameter a surviving unit reads must not ship an authored value: %+v", result.Interface.Operations)
	}
	if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("the loader's ORIGINAL error must stand, got %q", err)
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

// ---- block 8h: the mechanism-(a) emission pair -------------------------------

// oracleWalkDocument is one artifact in two versions. The only defect is a
// single `"m": []` inside a shared component -- a `properties` member whose
// value is an array, which kin-openapi's own typed unmarshal refuses, which is
// what puts it in mechanism (a)'s hands. `/climb` reaches the component through
// a query-parameter schema and CLIMBS. The two versions differ only in whether
// `/survive` reaches the same component.
//
// `reached` is record 102's `n2`, stored verbatim at
// `corpus-lab/data/oas-mechanism-a-reproducers/n2-oracle-unwalked-paramcontent.json`.
// Its channel -- a Parameter Object's `content` form -- is one the acceptance
// floor's closure walk does not visit, which is why two blocks keyed on that
// walk shipped it. Nothing in this pair names that channel.
func oracleWalkDocument(reached bool) string {
	survivingParameter := `{"name": "q", "in": "query", "schema": {"type": "string"}}`
	if reached {
		survivingParameter = `{"name": "q", "in": "query", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Shared"}}}}`
	}
	return `{
	  "openapi": "3.0.3",
	  "info": {"title": "T", "version": "1"},
	  "components": {"schemas": {"Shared": {"type": "object", "properties": {"x": {"type": "string"}, "m": []}}}},
	  "paths": {
	    "/climb": {"get": {"operationId": "getClimb",
	      "parameters": [{"name": "p", "in": "query", "schema": {"$ref": "#/components/schemas/Shared"}}],
	      "responses": {"200": {"description": "ok"}}}},
	    "/survive": {"get": {"operationId": "getSurvive",
	      "parameters": [` + survivingParameter + `],
	      "responses": {"200": {"description": "ok"}}}}
	  }
	}`
}

// RED PROOF, mechanism (a), emission side. A surviving unit reaches the
// component mechanism (a) authored into, so the member the artifact declared
// would be gone from shipped content. The engine's own emitter says so -- the
// marked and shipped images do not emit identically -- and the confinement is
// refused.
//
// Stop `confinementNeutralize` recording with the ledger and this goes red: the
// document synthesizes, `getSurvive` is reported `represented` with
// `exhaustive=true` and no projection entry, and its shipped schema is
// `{"properties":{"x":{"type":"string"}},"type":"object"}` -- the artifact's
// `m` silently absent. That was the behaviour at `5acb82a`.
func TestConfinement_OracleWalkIntoAnEmittedSchemaIsRefused(t *testing.T) {
	result, err := synthesizeRaw(oracleWalkDocument(true))
	if err == nil {
		t.Fatalf("a component a surviving unit emits must not ship an authored value: %+v", result.Interface.Operations)
	}
	if !strings.Contains(err.Error(), "cannot unmarshal") {
		t.Errorf("the loader's ORIGINAL error must stand, got %q", err)
	}
}

// RED PROOF, the other direction, over the SAME document. Nothing emitted
// reaches the authored component, the emitter says the two images are
// identical, and the confinement is admitted: the intact sibling survives and
// the defective unit is entried.
//
// Make the gate refuse unconditionally and this goes red. Without it the pair
// above is satisfiable by refusing everything, which is the failure mode a
// one-directional proof cannot see.
func TestConfinement_OracleWalkOutsideEmittedContentIsAdmitted(t *testing.T) {
	result, err := synthesizeRaw(oracleWalkDocument(false))
	if err != nil {
		t.Fatalf("nothing emitted reads the authored position; the confinement must be admitted: %v", err)
	}
	if _, ok := result.Interface.Operations["getSurvive"]; !ok {
		t.Fatalf("the surviving unit must synthesize: %v", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["getClimb"]; ok {
		t.Fatalf("the climbing unit must not synthesize: %v", result.Interface.Operations)
	}
	var invalid *synthesize.SynthesisCoverageEntry
	for i := range result.Coverage.Entries {
		if result.Coverage.Entries[i].Status == synthesize.SynthesisInvalid {
			invalid = &result.Coverage.Entries[i]
		}
	}
	if invalid == nil || invalid.SourceRef != "#/paths/~1climb/get" {
		t.Fatalf("the climbing unit must be entried: %+v", result.Coverage.Entries)
	}
}

// THE BORROW, IN ONE LINE, REFUSED BY THE FUNCTION.
//
// Record 112 §6 ground 3: the restriction that makes the denotation exemption
// safe -- only a BARE Reference Object is inlined -- lived in
// `confinementApplySeamC`, not in `confinementInlineDenoted`, while the
// function itself accepted any map carrying a string `$ref`. A safety property
// held by a call site rather than by a function has been defeated in this exact
// file twice, each time in one added line (record 104 §4, record 107 §6.3), and
// each time the block that shipped it believed the class was closed.
//
// This is that one line. `#/components/schemas/S` is a Reference Object WITH A
// SIBLING, so it is not a bare reference and inlining it would DISCARD the
// sibling -- an authored difference, produced by the one function in the pass
// that is exempt from being asked about it, and accounted as DENOTED.
//
// Two-sided, because a test that only shows the fixed head refuses proves the
// function is not broken and not that anything was closed:
//
//   - a SIBLING-BEARING reference must be refused, and nothing may move;
//   - a BARE reference at the same position must still be inlined, or the
//     refusal above would be a function that refuses everything.
func TestConfinement_DenotationRefusesASiblingBearingReferenceObject(t *testing.T) {
	build := func(siblings string) map[string]any {
		entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},
"components":{"schemas":{"S":{"type":"string"},"Borrower":{"$ref":"#/components/schemas/S"` + siblings + `}}},
"paths":{}}`)
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("the probe must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("the probe must be an object")
		}
		return root
	}
	const site = "#/components/schemas/Borrower"
	const target = "#/components/schemas/S"

	withSibling := build(`,"description":"a sibling the normalizer composes"`)
	ledger := newConfinementLedger()
	if confinementInlineDenoted(withSibling, site, target, ledger) {
		t.Fatalf("the exemption was BORROWED: a sibling-bearing reference was inlined by the function")
	}
	node := withSibling["components"].(map[string]any)["schemas"].(map[string]any)["Borrower"].(map[string]any)
	if _, stillRef := node["$ref"]; !stillRef || len(node) != 2 {
		t.Fatalf("a refused inline must leave the tree exactly as it was, got %v", node)
	}
	if ledger.records(site) {
		t.Fatalf("a refused inline must record nothing")
	}

	bare := build("")
	if !confinementInlineDenoted(bare, site, target, newConfinementLedger()) {
		t.Fatalf("a BARE reference must still be inlined; a function that refuses everything proves nothing")
	}
}

// THE PREFIX IS LOAD-BEARING, stated at the admission point.
//
// Record 112 §6 ground 2: `confinementUnledgeredDifference`'s godoc claimed the
// `records` prefix was "harmless … which is the case today". It was false at
// the head that shipped it -- reducing `records` to `pointer == site` costs the
// 260-artifact corpus five specimens and 133 operations -- and a vintage cannot
// be banked against an engine whose source misstates the safety property the
// measurement depends on.
//
// This is the corrected claim under a proof. It drives `confinementAdmit`, the
// pass's ONE admission point and the function the accounting obligation is
// stated over, with the shape the URef round actually produces: the SITE
// recorded, and the difference lying at `<site>/$ref` BENEATH it.
//
// Two-sided, so it cannot pass for the wrong reason:
//
//   - the difference beneath a RECORDED site must be admitted. Red the moment
//     `records` stops matching a prefix.
//   - the same difference with the site NOT recorded must decline, or the first
//     half would pass under a check that accounts for everything.
func TestConfinement_ARecordedSiteAccountsForTheDifferenceBeneathIt(t *testing.T) {
	entry := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},
"components":{"schemas":{"S":{"$ref":"#/components/schemas/Missing","description":"kept"}}},
"paths":{"/x":{"get":{"operationId":"getX","responses":{"200":{"description":"ok"}}}}}}`)
	const site = "#/components/schemas/S"

	authored := func() map[string]any {
		tree, ok := parseRawResource(entry)
		if !ok {
			t.Fatal("the probe must parse")
		}
		root, ok := tree.(map[string]any)
		if !ok {
			t.Fatal("the probe must be an object")
		}
		// Exactly what `confinementNeutralizeURef` does: remove the `$ref`
		// MEMBER, leave the siblings, and record the SITE. The difference is at
		// `<site>/$ref`, one token BENEATH the position on the ledger.
		delete(root["components"].(map[string]any)["schemas"].(map[string]any)["S"].(map[string]any), "$ref")
		return root
	}
	reload := func(data []byte) (*openapi3.T, error) {
		return openapi3.NewLoader().LoadFromData(data)
	}
	admittingGate := func(shipped, marked []byte, floor *acceptanceFloor) (bool, bool) { return true, true }

	tree := authored()
	floor := computeAcceptanceFloor(tree)
	if floor == nil {
		t.Fatal("the edition gate must read this artifact")
	}
	shipped, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	loaded, err := reload(shipped)
	if err != nil {
		t.Fatalf("the authored tree must load: %v", err)
	}

	ledger := newConfinementLedger()
	ledger.author(site)
	if _, _, ok := confinementAdmit(entry, tree, ledger, floor, reload, admittingGate, loaded); !ok {
		t.Fatalf("a difference BENEATH a recorded site must be accounted: the prefix in ledger.records is load-bearing, " +
			"and the URef round and the denotation inline both depend on it")
	}

	if _, _, ok := confinementAdmit(entry, tree, newConfinementLedger(), floor, reload, admittingGate, loaded); ok {
		t.Fatalf("the same difference with NOTHING recorded must decline; otherwise the half above proves nothing")
	}
}

// THE SEAM-C ERROR EXIT IS ROUTED THROUGH THE ACCOUNTING.
//
// Record 112 §6 ground 4. `confineEntryDocument`'s seam-C loop returns
// `(nil, loadErr, true)` when the load error stops matching
// `confinementBadData`. It hands back no document, so nothing authored can
// SHIP through it -- and it hands back an error produced by loading bytes the
// pass itself wrote, with the ledger and the gate never consulted. It is live
// in the corpus: `basset/basset` reaches it with three authored positions, and
// the consumer is told `invalid schema: value MUST be an object` while the
// artifact's own error is `cannot unmarshal bool into field Schema.required of
// type []string`.
//
// Two-sided, driving `confineEntryDocument` itself with a `reload` that fails
// with an error `confinementBadData` does not match:
//
//   - the pass AUTHORED: the diagnostic is pass-derived, so the pass declines
//     and the caller keeps the ARTIFACT'S OWN error. Red if the exit stops
//     consulting the ledger.
//   - the pass changed NOTHING: the tree that failed to load is the artifact's
//     own, so the error is the artifact's own and still stands. Red if the exit
//     is closed off entirely instead of routed, which would be the easy wrong
//     fix and would take the honest error with it.
func TestConfinement_SeamCErrorExitDoesNotHandOnAPassDerivedDiagnostic(t *testing.T) {
	passDerived := errors.New("invalid schema: value MUST be an object")
	reload := func([]byte) (*openapi3.T, error) { return nil, passDerived }
	gate := func(shipped, marked []byte, floor *acceptanceFloor) (bool, bool) { return true, true }

	// The pass AUTHORS: an empty-array HTTP-method member is located by the
	// oracle walk and neutralised, so the ledger is non-empty when the exit is
	// reached.
	authoring := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},
"paths":{"/good":{"get":{"operationId":"getGood","responses":{"200":{"description":"ok"}}}},"/bad":{"get":[]}}}`)
	doc, err, took := confineEntryDocument(authoring, reload, errors.New("cannot unmarshal array into field PathItem.get"), gate)
	if took {
		t.Fatalf("a diagnostic derived from a tree the pass authored must not be handed on; got err=%v doc=%v", err, doc)
	}
	if err != nil {
		t.Fatalf("a decline hands back no error of its own, so the artifact's own stands; got %v", err)
	}

	// The pass changes NOTHING: no located defect and no climbing URef site, so
	// the tree handed to the loader is the artifact's own and so is the error.
	inert := []byte(`{"openapi":"3.0.3","info":{"title":"T","version":"1"},
"paths":{"/good":{"get":{"operationId":"getGood","responses":{"200":{"description":"ok"}}}}}}`)
	_, err, took = confineEntryDocument(inert, reload, errors.New(`bad data in "#/components/schemas/S" (expecting ref to schema object)`), gate)
	if !took || err == nil {
		t.Fatalf("an unconfined tree's own load error must still be reported; got took=%v err=%v", took, err)
	}
	if !errors.Is(err, passDerived) {
		t.Fatalf("the error reported must be the load's own, got %v", err)
	}
}
