package openapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
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

func TestRecursiveComponentSynthesizesAndInvokesThroughCoreDocumentRoot(t *testing.T) {
	doc := strings.Replace(recursiveDoc, `"paths":`, `"servers": [{"url": "https://api.example.test"}], "paths":`, 1)
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(doc)}},
	})
	if err != nil {
		t.Fatalf("synthesis failed: %v", err)
	}
	tree := map[string]any{
		"label": "root",
		"children": []any{
			map[string]any{"label": "leaf", "children": []any{}},
		},
	}
	body, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})}
	invoker := openbindings.NewOperationInvoker(NewInvokerWithClient(client))
	invoker.TransformEvaluator = openAPIJSONataEvaluator{}
	call := openbindings.Invoke(
		context.Background(),
		invoker,
		result.Interface,
		openbindings.NewOperationSignature[any, any]("createTree"),
	)
	if err := call.Write(context.Background(), tree); err != nil {
		t.Fatalf("write recursive input: %v", err)
	}
	if err := call.Close(); err != nil {
		t.Fatalf("close input: %v", err)
	}
	output, err := openbindings.Single(context.Background(), call.Outputs())
	if err != nil {
		t.Fatalf("read recursive output: %v", err)
	}
	if !reflect.DeepEqual(output, tree) {
		t.Fatalf("output = %#v, want %#v", output, tree)
	}
}

func TestResolvedParameterSchemaRefDoesNotEscapeIntoOBI(t *testing.T) {
	doc := `{
  "openapi": "3.1.0",
  "info": {"title": "parameters", "version": "1.0.0"},
  "paths": {"/items": {"get": {
    "operationId": "listItems",
    "parameters": [{
      "name": "ordering",
      "in": "query",
      "description": "requested ordering",
      "schema": {"$ref": "#/components/schemas/Ordering"}
    }],
    "responses": {"200": {"description": "ok", "content": {
      "application/json": {"schema": {"$ref": "#/components/schemas/Envelope"}}
    }}}
  }}},
  "components": {"schemas": {
    "Ordering": {"type": "string", "enum": ["weak", "strong"]},
    "Definitions": {"type": "object", "properties": {
      "ordering": {"type": "string", "enum": ["weak", "strong"]}
    }},
    "Envelope": {"type": "object", "properties": {
      "ordering": {
        "$ref": "#/components/schemas/Definitions/properties/ordering",
        "description": "resolved through a nested pointer"
      }
    }}
  }}
}`

	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: json.RawMessage(doc)}},
	})
	if err != nil {
		t.Fatalf("synthesis failed: %v", err)
	}
	op := result.Interface.Operations["listItems"]
	encoded, err := json.Marshal(op.Input)
	if err != nil {
		t.Fatalf("input must serialize: %v", err)
	}
	if strings.Contains(string(encoded), `#/components/schemas/`) {
		t.Fatalf("input carries a dangling source-artifact reference: %s", encoded)
	}
	properties := op.Input.(map[string]any)["properties"].(map[string]any)
	ordering := properties["ordering"].(map[string]any)
	if ordering["type"] != "string" || ordering["description"] != "requested ordering" {
		t.Fatalf("resolved parameter schema lost constraints or annotation: %#v", ordering)
	}
	output, err := json.Marshal(op.Output)
	if err != nil {
		t.Fatalf("output must serialize: %v", err)
	}
	if strings.Contains(string(output), `#/components/schemas/`) {
		t.Fatalf("output carries a dangling nested source-artifact reference: %s", output)
	}
	outputProperties := op.Output.(map[string]any)["properties"].(map[string]any)
	outputOrdering := outputProperties["ordering"].(map[string]any)
	if outputOrdering["type"] != "string" {
		t.Fatalf("nested pointer constraints were lost: %#v", outputOrdering)
	}
}
