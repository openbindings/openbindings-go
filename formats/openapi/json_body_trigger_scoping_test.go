package openapi

// The §9.1 JSON-body trigger-scoping case table, shared byte-identically
// with the TypeScript adapter and both openapi-client engines
// (testdata/json-body-trigger-scoping-cases.json). This engine owns
// SYNTHESIS, so the cells are asserted through the shipped synthesizer: a
// whole cell publishes the one protocol-neutral `payload` operation property
// and an inputTransform carrying "whole":"payload"; a flattened cell
// publishes the artifact's own `value` property and no `payload`.
//
// Two rules ride the same cells. The trigger keywords are read under the
// GOVERNING EDITION'S dialect: on the 3.0 line patternProperties, if, then,
// else, dependentSchemas and unevaluatedProperties are not in the Schema
// Object dialect at all and decide as if absent, while oneOf, anyOf, not and
// additionalProperties decide alike on both lines. And an explicit
// unevaluatedProperties triggers on ANY value, the `false` spelling
// included.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

type jsonBodyTriggerCase struct {
	Name     string         `json:"name"`
	OpenAPI  string         `json:"openapi"`
	Line     string         `json:"line"`
	Keyword  string         `json:"keyword"`
	Presence string         `json:"presence"`
	Schema   map[string]any `json:"schema"`
	Expect   string         `json:"expect"`
}

type jsonBodyTriggerTable struct {
	Cases []jsonBodyTriggerCase `json:"cases"`
}

func jsonBodyTriggerDocument(t *testing.T, fixture jsonBodyTriggerCase) string {
	t.Helper()
	document := map[string]any{
		"openapi": fixture.OpenAPI,
		"info":    map[string]any{"title": "json body trigger scoping", "version": "1"},
		"servers": []any{map[string]any{"url": "https://fixture.invalid"}},
		"paths": map[string]any{
			"/items": map[string]any{
				"post": map[string]any{
					"operationId": "createItem",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							"application/json": map[string]any{"schema": fixture.Schema},
						},
					},
					"responses": map[string]any{
						"204": map[string]any{"description": "stored"},
					},
				},
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestSharedJSONBodyTriggerScopingSynthesis(t *testing.T) {
	data, err := os.ReadFile("testdata/json-body-trigger-scoping-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var table jsonBodyTriggerTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	for _, fixture := range table.Cases {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			switch fixture.Expect {
			case "whole", "flattened":
			default:
				t.Fatalf("unknown expect %q", fixture.Expect)
			}
			document := jsonBodyTriggerDocument(t, fixture)
			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(document)}},
			})
			if err != nil {
				t.Fatalf("synthesis: %v", err)
			}
			operation, present := iface.Operations["createItem"]
			if !present {
				t.Fatalf("operation createItem was not published: %#v", iface.Operations)
			}
			properties, _ := operation.Input.(map[string]any)["properties"].(map[string]any)
			_, payload := properties["payload"]
			_, value := properties["value"]
			binding := iface.Bindings["createItem.openapi"]
			wholeTransform := binding.InputTransform != nil &&
				strings.Contains(binding.InputTransform.Inline, `"whole":"payload"`)

			if fixture.Expect == "whole" {
				if !payload || value {
					t.Fatalf("whole carriage did not publish a lone payload property: %#v", properties)
				}
				if !wholeTransform {
					t.Fatalf("input transform does not route the whole body: %#v", binding.InputTransform)
				}
				return
			}
			if payload || !value {
				t.Fatalf("flattened carriage did not publish the artifact's own property: %#v", properties)
			}
			if wholeTransform {
				t.Fatalf("flattened carriage routed a whole body: %#v", binding.InputTransform)
			}
		})
	}
}
