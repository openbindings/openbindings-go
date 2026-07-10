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

// TestValidateAgainstSchema_ExternalRefFailsClosed pins the OBI-T-07/T-08
// clarification: validation is against the FULLY RESOLVED schema, so a
// schema carrying an external $ref the tool cannot fetch is a validation
// error (fail closed), never a partial pass.
func TestValidateAgainstSchema_ExternalRefFailsClosed(t *testing.T) {
	opSchema := JSONSchema{"$ref": "https://example.com/schemas/user-input.json"}
	err := ValidateAgainstSchema(map[string]any{"id": "u1"}, opSchema, nil)
	if err == nil {
		t.Fatal("external $ref should fail closed, not validate partially")
	}
}
