package openapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// A document with one clean operation and two operations genuinely
// unrepresentable by the complete first candidate: a required multipart
// scalar body with no artifact-defined carriage, and header declarations
// whose case-folded wire identities collide. Mirrors the TS adapter.
const mixedDoc = `{
  "openapi": "3.0.3",
  "info": {"title": "mixed", "version": "1.0.0"},
  "paths": {
    "/good": {
      "get": {"operationId": "getGood", "responses": {"200": {"description": "ok"}}}
    },
    "/conditional": {
      "post": {
		"operationId": "postUncarriable",
        "requestBody": {
          "required": true,
		  "content": {"multipart/form-data": {"schema": {"type": "string"}}}
        },
        "responses": {"200": {"description": "ok"}}
      }
    },
    "/collide": {
      "get": {
        "operationId": "getCollide",
        "parameters": [
		  {"name": "X-ID", "in": "header", "schema": {"type": "string"}},
		  {"name": "x-id", "in": "header", "schema": {"type": "string"}}
        ],
        "responses": {"200": {"description": "ok"}}
      }
    }
  }
}`

func mixedInput() *synthesize.SynthesizeInput {
	return &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{
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

	targets := map[string]synthesize.SynthesisCoverageEntry{}
	for _, entry := range result.Coverage.Entries {
		if entry.Scope == synthesize.SynthesisCoverageTarget {
			targets[entry.SourceRef] = entry
			if entry.Status == synthesize.SynthesisImplementationUnsupported {
				t.Fatalf("implementation-unsupported target %q; every omission must be spec-governed", entry.SourceRef)
			}
		}
	}

	good, ok := targets["#/paths/~1good/get"]
	if !ok || good.Status != synthesize.SynthesisRepresented {
		t.Fatalf("good target = %+v; want represented", good)
	}

	conditional, ok := targets["#/paths/~1conditional/post"]
	if !ok || conditional.Status != synthesize.SynthesisExcluded {
		t.Fatalf("conditional target = %+v; want excluded", conditional)
	}
	if conditional.ReasonCode != "openapi.unresolvable_request_body" && conditional.ReasonCode != "openapi.media_schema_mismatch" {
		t.Fatalf("conditional reasonCode = %q", conditional.ReasonCode)
	}
	if conditional.Rule != "OAPI-P-04" {
		t.Fatalf("conditional rule = %q; want OAPI-P-04", conditional.Rule)
	}

	collide, ok := targets["#/paths/~1collide/get"]
	if !ok || collide.Status != synthesize.SynthesisExcluded {
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
	if len(inspection.Targets) != 1 || inspection.Targets[0].Selector != "#/paths/~1good/get" {
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
	        "operationId": "postUncarriable",
	        "requestBody": {
	          "required": true,
	          "content": {"multipart/form-data": {"schema": {"type": "string"}}}
	        },
	        "responses": {"200": {"description": "ok"}}
	      }
	    }
	  }
	}`
	synth := &Synthesizer{}
	result, err := synth.SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(doc)}},
	})
	if err != nil {
		t.Fatalf("coverage synthesis failed: %v", err)
	}
	if len(result.Interface.Operations) != 0 {
		t.Fatalf("operations = %v; want none", result.Interface.Operations)
	}
	var targetCount int
	for _, entry := range result.Coverage.Entries {
		if entry.Scope == synthesize.SynthesisCoverageTarget {
			targetCount++
			if entry.Status != synthesize.SynthesisExcluded {
				t.Fatalf("target status = %q; want excluded", entry.Status)
			}
		}
	}
	if targetCount != 1 {
		t.Fatalf("target entries = %d; want 1", targetCount)
	}
}
