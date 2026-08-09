package openbindings

import (
	"encoding/json"
	"testing"
)

func TestOperationSchemaDocumentRootResolutionMatrix(t *testing.T) {
	tests := []struct {
		name            string
		operation       string
		schema          JSONSchema
		schemas         map[string]JSONSchema
		extraOperations map[string]Operation
		valid           any
		invalid         any
	}{
		{
			name:      "named schema",
			operation: "target",
			schema:    map[string]any{"$ref": "#/schemas/Identifier"},
			schemas:   map[string]JSONSchema{"Identifier": map[string]any{"type": "string"}},
			valid:     "ok",
			invalid:   float64(1),
		},
		{
			name:      "operation-local recursive definition",
			operation: "target",
			schema: map[string]any{
				"$ref": "#/operations/target/output/$defs/Node",
				"$defs": map[string]any{
					"Node": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{"type": "string"},
							"next":  map[string]any{"$ref": "#/operations/target/output/$defs/Node"},
						},
						"required": []any{"value"},
					},
				},
			},
			valid:   map[string]any{"value": "a", "next": map[string]any{"value": "b"}},
			invalid: map[string]any{"value": "a", "next": map[string]any{"value": float64(2)}},
		},
		{
			name:            "cross-operation schema",
			operation:       "target",
			schema:          map[string]any{"$ref": "#/operations/shared/output"},
			extraOperations: map[string]Operation{"shared": {Output: map[string]any{"type": "integer"}}},
			valid:           float64(2),
			invalid:         "2",
		},
		{
			name:      "escaped operation key",
			operation: "a/b~c",
			schema: map[string]any{
				"$ref":  "#/operations/a~1b~0c/output/$defs/Value",
				"$defs": map[string]any{"Value": map[string]any{"type": "boolean"}},
			},
			valid:   true,
			invalid: "true",
		},
		{
			name:      "absolute ref to embedded id resource",
			operation: "target",
			schema:    map[string]any{"$ref": "https://schemas.example.test/Identifier"},
			schemas: map[string]JSONSchema{
				"Identifier": map[string]any{
					"$id":    "https://schemas.example.test/Identifier",
					"type":   "string",
					"format": "email",
				},
			},
			valid:   "not-an-email",
			invalid: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operations := map[string]Operation{}
			for name, operation := range test.extraOperations {
				operations[name] = operation
			}
			operations[test.operation] = Operation{Output: test.schema}
			iface := &Interface{
				OpenBindings: "0.2.0",
				Schemas:      test.schemas,
				Operations:   operations,
			}
			if err := ValidateOperationOutput(test.valid, iface, test.operation); err != nil {
				t.Fatalf("valid value rejected: %v", err)
			}
			if err := ValidateOperationOutput(test.invalid, iface, test.operation); err == nil {
				t.Fatal("invalid value accepted")
			}
		})
	}
}

func TestOperationSchemaIgnoresSchemaShapedUnknownRootField(t *testing.T) {
	iface := &Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"target": {Output: map[string]any{"type": "string"}},
		},
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{"type": json.RawMessage(`"integer"`)},
		},
	}
	if err := ValidateOperationOutput("ok", iface, "target"); err != nil {
		t.Fatalf("unknown OBI-root field constrained operation output: %v", err)
	}
	if err := ValidateOperationOutput(float64(1), iface, "target"); err == nil {
		t.Fatal("operation schema itself was not applied")
	}
}
