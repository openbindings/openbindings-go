package openapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// A document with one clean operation and two operations unrepresentable
// under revision 1's flattened boundary: a required conditional (oneOf)
// request body, and a cross-location parameter name collision. The tolerant
// coverage surface must return a sound partial OBI that binds the clean
// operation and accounts for both exclusions; the strict surface must keep
// refusing the whole document. Mirrors the TS SDK's tolerant-coverage.test.ts
// (SDK parity is a loop invariant).
const mixedDoc = `{
  "openapi": "3.0.3",
  "info": {"title": "mixed", "version": "1.0.0"},
  "paths": {
    "/good": {
      "get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}
    },
    "/conditional": {
      "post": {
        "operationId": "postConditional",
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"oneOf": [
            {"type": "string"},
            {"type": "object", "properties": {"a": {"type": "string"}}}
          ]}}}
        },
        "responses": {"200": {"description": "ok"}}
      }
    },
    "/collide": {
      "get": {
        "operationId": "getCollide",
        "parameters": [
          {"name": "id", "in": "query", "schema": {"type": "string"}},
          {"name": "id", "in": "header", "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`

func mixedInput() *openbindings.SynthesizeInput {
	return &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Content:     json.RawMessage(mixedDoc),
		}},
	}
}

func TestStrictSynthesisStillRefusesWholeDocument(t *testing.T) {
	synth := &Synthesizer{}
	_, err := synth.SynthesizeInterface(context.Background(), mixedInput())
	if err == nil {
		t.Fatal("strict synthesis succeeded; want whole-document refusal")
	}
	if !strings.Contains(err.Error(), "cannot synthesize OpenAPI operation") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCoverageSynthesisReturnsSoundPartialOBI(t *testing.T) {
	synth := &Synthesizer{}
	result, err := synth.SynthesizeInterfaceWithCoverage(context.Background(), mixedInput())
	if err != nil {
		t.Fatalf("coverage synthesis failed: %v", err)
	}

	if len(result.Interface.Operations) != 1 {
		t.Fatalf("operations = %v; want exactly getGood", result.Interface.Operations)
	}
	if _, ok := result.Interface.Operations["getGood"]; !ok {
		t.Fatalf("missing getGood; got %v", result.Interface.Operations)
	}

	targets := map[string]openbindings.SynthesisCoverageEntry{}
	for _, entry := range result.Coverage.Entries {
		if entry.Scope == openbindings.SynthesisCoverageTarget {
			targets[entry.SourceRef] = entry
			if entry.Status == openbindings.SynthesisImplementationUnsupported {
				t.Fatalf("implementation-unsupported target %q; every omission must be spec-governed", entry.SourceRef)
			}
		}
	}

	good, ok := targets["#/paths/~1good/get"]
	if !ok || good.Status != openbindings.SynthesisRepresented {
		t.Fatalf("good target = %+v; want represented", good)
	}

	conditional, ok := targets["#/paths/~1conditional/post"]
	if !ok || conditional.Status != openbindings.SynthesisExcluded {
		t.Fatalf("conditional target = %+v; want excluded", conditional)
	}
	if conditional.ReasonCode != "openapi.unresolvable_request_body" && conditional.ReasonCode != "openapi.media_schema_mismatch" {
		t.Fatalf("conditional reasonCode = %q", conditional.ReasonCode)
	}
	if conditional.Rule != "OAPI-P-04" {
		t.Fatalf("conditional rule = %q; want OAPI-P-04", conditional.Rule)
	}

	collide, ok := targets["#/paths/~1collide/get"]
	if !ok || collide.Status != openbindings.SynthesisExcluded {
		t.Fatalf("collide target = %+v; want excluded", collide)
	}
	if collide.ReasonCode != "openapi.flattening_collision" || collide.Rule != "OAPI-P-03" {
		t.Fatalf("collide reason = %q rule = %q; want openapi.flattening_collision / OAPI-P-03", collide.ReasonCode, collide.Rule)
	}

	if !result.Coverage.Exhaustive {
		t.Fatal("coverage must remain exhaustive")
	}
	if result.Coverage.FullyRepresented {
		t.Fatal("coverage must honestly report not fully represented")
	}
}

func TestInspectSourceFiltersUnrepresentableTargets(t *testing.T) {
	synth := &Synthesizer{}
	inspection, err := synth.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Content:     json.RawMessage(mixedDoc),
	})
	if err != nil {
		t.Fatalf("inspection failed: %v", err)
	}
	if len(inspection.Targets) != 1 || inspection.Targets[0].Ref != "#/paths/~1good/get" {
		t.Fatalf("targets = %+v; want exactly #/paths/~1good/get", inspection.Targets)
	}
	if !inspection.Exhaustive {
		t.Fatal("inspection must remain exhaustive")
	}
}

func TestAllUnrepresentableYieldsEmptySoundOBI(t *testing.T) {
	doc := `{
	  "openapi": "3.0.3",
	  "info": {"title": "all-bad", "version": "1.0.0"},
	  "paths": {
	    "/conditional": {
	      "post": {
	        "operationId": "postConditional",
	        "requestBody": {
	          "required": true,
	          "content": {"application/json": {"schema": {"oneOf": [{"type": "string"}, {"type": "object"}]}}}
	        },
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`
	synth := &Synthesizer{}
	result, err := synth.SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(doc)}},
	})
	if err != nil {
		t.Fatalf("coverage synthesis failed: %v", err)
	}
	if len(result.Interface.Operations) != 0 {
		t.Fatalf("operations = %v; want none", result.Interface.Operations)
	}
	var targetCount int
	for _, entry := range result.Coverage.Entries {
		if entry.Scope == openbindings.SynthesisCoverageTarget {
			targetCount++
			if entry.Status != openbindings.SynthesisExcluded {
				t.Fatalf("target status = %q; want excluded", entry.Status)
			}
		}
	}
	if targetCount != 1 {
		t.Fatalf("target entries = %d; want 1", targetCount)
	}
}
