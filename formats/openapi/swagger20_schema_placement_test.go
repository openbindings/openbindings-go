package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

type placementTransport func(*http.Request) (*http.Response, error)

func (f placementTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSwagger20SchemaPlacementPreservesIndependentClosures(t *testing.T) {
	for _, external := range []bool{false, true} {
		for _, alternatives := range []bool{false, true} {
			t.Run(fmt.Sprintf("external=%t/alternatives=%t", external, alternatives), func(t *testing.T) {
				ref := func(name string) map[string]any { return map[string]any{"$ref": "#/definitions/" + name} }
				definitions := map[string]any{}
				for name, kind := range map[string]string{"Node": "integer", "Word": "string"} {
					definitions[name] = map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{
						"value": map[string]any{"type": kind}, "next": ref(name),
						"literal": map[string]any{"type": "object", "default": map[string]any{"$ref": "#/$defs/schema0"}},
					}}
				}
				prefix := ""
				if external {
					prefix = "schema.json"
				}
				rootRef := func(name string) map[string]any { return map[string]any{"$ref": prefix + "#/definitions/" + name} }
				responses := map[string]any{"200": map[string]any{"description": "ok", "schema": rootRef("Node")}}
				if alternatives {
					responses["201"] = map[string]any{"description": "word", "schema": rootRef("Word")}
				}
				document := map[string]any{"swagger": "2.0", "info": map[string]any{"title": "Placement", "version": "1"},
					"host": "api.example", "schemes": []any{"https"}, "consumes": []any{"application/json"}, "produces": []any{"application/json"},
					"definitions": definitions, "paths": map[string]any{"/echo": map[string]any{"post": map[string]any{
						"operationId": "echo", "parameters": []any{
							map[string]any{"name": "body", "in": "query", "type": "boolean"},
							map[string]any{"name": "payload", "in": "body", "required": true, "schema": rootRef("Node")},
						}, "responses": responses,
					}}}}
				body, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				resource, err := json.Marshal(map[string]any{"definitions": definitions})
				if err != nil {
					t.Fatal(err)
				}
				reads := 0
				client := &http.Client{Transport: placementTransport(func(r *http.Request) (*http.Response, error) {
					reads++
					if r.URL.String() != "https://artifact.example/schema.json" {
						t.Fatalf("unexpected acquisition: %s", r.URL)
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(resource))), Request: r}, nil
				})}
				iface, err := NewSynthesizerWithClient(client).SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
					Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpecOpenAPI20, Location: "https://artifact.example/openapi.json", Content: body}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if (reads > 0) != external {
					t.Fatalf("external=%t, acquisitions=%d", external, reads)
				}
				good := map[string]any{"value": 7, "next": map[string]any{"value": 8}}
				if err := openbindings.ValidateOperationInput(map[string]any{"body": true, "body_2": good}, iface, "echo"); err != nil {
					t.Fatal(err)
				}
				if err := openbindings.ValidateOperationInput(map[string]any{"body_2": map[string]any{"value": "wrong"}}, iface, "echo"); err == nil {
					t.Fatal("input reference lost its integer constraint")
				}
				if err := openbindings.ValidateOperationOutput(good, iface, "echo"); err != nil {
					t.Fatal(err)
				}
				wordErr := openbindings.ValidateOperationOutput(map[string]any{"value": "word", "next": map[string]any{"value": "nested"}}, iface, "echo")
				if (wordErr == nil) != alternatives {
					t.Fatalf("independent response closure: %v", wordErr)
				}
				if err := openbindings.ValidateOperationOutput(map[string]any{"value": true}, iface, "echo"); err == nil {
					t.Fatal("output references lost their constraints")
				}
				encoded, err := json.Marshal(iface.Operations["echo"])
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(encoded), `"default":{"$ref":"#/$defs/schema0"}`) {
					t.Fatal("literal default was changed")
				}
			})
		}
	}
}
