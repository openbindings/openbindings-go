package openapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func wholeJSONSpec(mediaType string) string {
	return wholeJSONSpecForSchema(mediaType, `{"oneOf":[{"type":"object","properties":{"kind":{"const":"named"},"name":{"type":"string"}},"required":["kind","name"],"additionalProperties":false},{"type":"array","items":{"type":"string"}}]}`)
}

func wholeJSONSpecForSchema(mediaType, schema string) string {
	return dynamicBodySpec("3.1.2", mediaType, schema)
}

func TestRevision6CarriesCombinatorialJSONAsOneApplicationValue(t *testing.T) {
	spec := wholeJSONSpec("application/json")
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := iface.Operations["createItem"].Input.(map[string]any)
	properties := input["properties"].(map[string]any)
	if _, ok := properties["id"]; !ok {
		t.Fatalf("input properties omit query id: %#v", properties)
	}
	payload, ok := properties["payload"].(map[string]any)
	if !ok || len(payload["oneOf"].([]any)) != 2 {
		t.Fatalf("payload schema did not preserve the declared union: %#v", properties["payload"])
	}
	binding := iface.Bindings["createItem.openapi"]
	if binding.InputTransform == nil || !strings.Contains(binding.InputTransform.Inline, `"whole":"payload"`) {
		t.Fatalf("input transform = %#v", binding.InputTransform)
	}

	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.Query().Get("id"); got != "query-value" {
			t.Errorf("query id = %q", got)
		}
		body, _ := io.ReadAll(req.Body)
		if string(body) != `{"kind":"named","name":"Ada"}` {
			t.Errorf("body = %s", body)
		}
		return &http.Response{StatusCode: 204, Status: "204 No Content", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	})
	call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Ref:    "#/paths/~1items/post",
	})
	inputValue := []any{map[string]any{
		"$openbindings": BindingSpec,
		"value": map[string]any{
			"id":      "query-value",
			"payload": map[string]any{"kind": "named", "name": "Ada"},
		},
		"parameters": []any{map[string]any{"in": "query", "name": "id", "field": "id"}},
		"body":       map[string]any{"whole": "payload"},
	}}
	outputs, invocationErr := driveOutputs(context.Background(), call, inputValue)
	if invocationErr != nil || len(outputs) != 0 {
		t.Fatalf("outputs = %#v, error = %#v", outputs, invocationErr)
	}
}

func TestRevision6DoesNotInventCombinatorialFormSerialization(t *testing.T) {
	_, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(wholeJSONSpec("application/x-www-form-urlencoded"))}},
	})
	if err == nil || !strings.Contains(err.Error(), "conditional/combinatorial request schema") {
		t.Fatalf("revision 6 form result = %v", err)
	}
}

func TestRevision6WholeJSONDeclarationTriggers(t *testing.T) {
	tests := map[string]string{
		"anyOf":                 `{"anyOf":[{"type":"object"},{"type":"array"}]}`,
		"not":                   `{"type":"object","not":{"required":["forbidden"]}}`,
		"if":                    `{"type":"object","if":{"required":["kind"]}}`,
		"then":                  `{"type":"object","then":{"required":["name"]}}`,
		"else":                  `{"type":"object","else":{"required":["fallback"]}}`,
		"dependentSchemas":      `{"type":"object","dependentSchemas":{"kind":{"required":["name"]}}}`,
		"unevaluatedProperties": `{"type":"object","unevaluatedProperties":true}`,
		"allOf traversal":       `{"allOf":[{"type":"object"},{"anyOf":[{"required":["a"]},{"required":["b"]}]}]}`,
	}
	for name, schema := range tests {
		t.Run(name, func(t *testing.T) {
			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(wholeJSONSpecForSchema("application/json", schema))}},
			})
			if err != nil {
				t.Fatal(err)
			}
			properties := iface.Operations["createItem"].Input.(map[string]any)["properties"].(map[string]any)
			if _, ok := properties["payload"]; !ok {
				t.Fatalf("whole-value payload missing: %#v", properties)
			}
			binding := iface.Bindings["createItem.openapi"]
			if binding.InputTransform == nil || !strings.Contains(binding.InputTransform.Inline, `"whole":"payload"`) {
				t.Fatalf("input transform = %#v", binding.InputTransform)
			}
		})
	}
}

func TestRevision6DoesNotPromoteInertOrNestedDeclarations(t *testing.T) {
	tests := map[string]string{
		"empty dependentSchemas":       `{"type":"object","properties":{"value":{"type":"string"}},"dependentSchemas":{}}`,
		"closed unevaluatedProperties": `{"type":"object","properties":{"value":{"type":"string"}},"unevaluatedProperties":false}`,
		"nested oneOf":                 `{"type":"object","properties":{"value":{"oneOf":[{"type":"string"},{"type":"number"}]}}}`,
	}
	for name, schema := range tests {
		t.Run(name, func(t *testing.T) {
			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(wholeJSONSpecForSchema("application/json", schema))}},
			})
			if err != nil {
				t.Fatal(err)
			}
			properties := iface.Operations["createItem"].Input.(map[string]any)["properties"].(map[string]any)
			if _, ok := properties["value"]; !ok {
				t.Fatalf("flattened value missing: %#v", properties)
			}
			if _, ok := properties["payload"]; ok {
				t.Fatalf("inert or nested declaration acquired whole-value payload: %#v", properties)
			}
		})
	}
}
