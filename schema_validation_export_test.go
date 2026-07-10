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

// TestValidateAgainstSchema_FormatIsAnnotationOnly pins §6.2's boundary
// rule: `format` never asserts at OBI validation boundaries — a value
// violating `format` still validates; enforced syntax belongs to `pattern`.
func TestValidateAgainstSchema_FormatIsAnnotationOnly(t *testing.T) {
	opSchema := JSONSchema{"type": "string", "format": "email"}
	if err := ValidateAgainstSchema("not-an-email", opSchema, nil); err != nil {
		t.Fatalf("format must be annotation-only at OBI boundaries, got %v", err)
	}
	patternSchema := JSONSchema{"type": "string", "pattern": "^[^@]+@[^@]+$"}
	if err := ValidateAgainstSchema("not-an-email", patternSchema, nil); err == nil {
		t.Fatal("pattern is the assertion lane and must reject")
	}
}
