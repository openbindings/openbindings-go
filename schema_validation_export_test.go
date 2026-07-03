package openbindings

import "testing"

func TestValidateAgainstSchema(t *testing.T) {
	schemas := map[string]JSONSchema{
		"Thing": {
			"type":                 "object",
			"properties":           map[string]any{"name": map[string]any{"type": "string"}},
			"required":             []any{"name"},
			"additionalProperties": false,
		},
	}
	opSchema := JSONSchema{"$ref": "#/schemas/Thing"}

	if err := ValidateAgainstSchema(map[string]any{"name": "ok"}, opSchema, schemas); err != nil {
		t.Fatalf("valid value rejected: %v", err)
	}
	if err := ValidateAgainstSchema(map[string]any{"stdout": "text"}, opSchema, schemas); err == nil {
		t.Fatal("invalid value accepted")
	}
}
