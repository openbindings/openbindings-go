package openapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Mirrors the TS SDK's cyclic-schema.test.ts (rev 2a): a recursive component
// must synthesize as $defs/$ref — the dialect's own recursion mechanism —
// with same-document pointers from the OBI root, never a dangling
// components reference and never an unserializable value.
const recursiveDoc = `{
  "openapi": "3.1.0",
  "info": {"title": "recursive", "version": "1.0.0"},
  "paths": {
    "/trees": {
      "post": {
        "operationId": "createTree",
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Tree"}}}
        },
        "responses": {
          "200": {
            "description": "ok",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Tree"}}}
          }
        }
      }
    }
  },
  "components": {"schemas": {"Tree": {
    "type": "object",
    "properties": {
      "label": {"type": "string"},
      "children": {"type": "array", "items": {"$ref": "#/components/schemas/Tree"}}
    }
  }}}
}`

func TestRecursiveComponentSynthesizesAsDefs(t *testing.T) {
	synth := &Synthesizer{}
	result, err := synth.SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(recursiveDoc)}},
	})
	if err != nil {
		t.Fatalf("synthesis failed: %v", err)
	}
	if _, err := json.Marshal(result.Interface); err != nil {
		t.Fatalf("interface must serialize: %v", err)
	}
	op := result.Interface.Operations["createTree"]
	input, err := json.Marshal(op.Input)
	if err != nil {
		t.Fatalf("input must serialize: %v", err)
	}
	output, err := json.Marshal(op.Output)
	if err != nil {
		t.Fatalf("output must serialize: %v", err)
	}
	if !strings.Contains(string(input), `"#/operations/createTree/input/$defs/Tree"`) {
		t.Fatalf("input missing $defs self-reference; got: %s", input)
	}
	if !strings.Contains(string(output), `"#/operations/createTree/output/$defs/Tree"`) {
		t.Fatalf("output missing $defs self-reference; got: %s", output)
	}
	// The operation schemas must be self-contained: no dangling references
	// into the source artifact's components namespace.
	for name, text := range map[string][]byte{"input": input, "output": output} {
		if strings.Contains(string(text), `#/components/schemas/`) {
			t.Fatalf("%s carries a dangling components reference", name)
		}
	}
}
