package openapi

import (
	"reflect"
	"testing"
)

func TestTranslateSchemaDialect_NullableTypeString(t *testing.T) {
	in := map[string]any{"type": "string", "nullable": true}
	want := map[string]any{"type": []any{"string", "null"}}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_NullableTypeArray(t *testing.T) {
	in := map[string]any{"type": []any{"string", "number"}, "nullable": true}
	want := map[string]any{"type": []any{"string", "number", "null"}}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_NullableNoDuplicateNull(t *testing.T) {
	in := map[string]any{"type": []any{"string", "null"}, "nullable": true}
	want := map[string]any{"type": []any{"string", "null"}}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_NullableDroppedWithoutType(t *testing.T) {
	in := map[string]any{"nullable": true, "description": "x"}
	want := map[string]any{"description": "x"}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_NullableFalseDropped(t *testing.T) {
	in := map[string]any{"type": "string", "nullable": false}
	want := map[string]any{"type": "string"}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_RecursesIntoProperties(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"next":  map[string]any{"type": "string", "nullable": true},
			"count": map[string]any{"type": "integer"},
		},
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"next":  map[string]any{"type": []any{"string", "null"}},
			"count": map[string]any{"type": "integer"},
		},
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_RecursesIntoItems(t *testing.T) {
	in := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string", "nullable": true},
	}
	want := map[string]any{
		"type":  "array",
		"items": map[string]any{"type": []any{"string", "null"}},
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_RecursesIntoOneOf(t *testing.T) {
	in := map[string]any{
		"oneOf": []any{
			map[string]any{"type": "string", "nullable": true},
			map[string]any{"type": "integer"},
		},
	}
	want := map[string]any{
		"oneOf": []any{
			map[string]any{"type": []any{"string", "null"}},
			map[string]any{"type": "integer"},
		},
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_AdditionalPropertiesSchema(t *testing.T) {
	in := map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": "string", "nullable": true},
	}
	want := map[string]any{
		"type":                 "object",
		"additionalProperties": map[string]any{"type": []any{"string", "null"}},
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_AdditionalPropertiesBool(t *testing.T) {
	in := map[string]any{"type": "object", "additionalProperties": true}
	want := map[string]any{"type": "object", "additionalProperties": true}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_DoesNotRecurseIntoExampleEnumDefault(t *testing.T) {
	in := map[string]any{
		"type":    "string",
		"example": map[string]any{"type": "string", "nullable": true},
		"enum":    []any{"a", "b"},
		"default": "a",
	}
	want := map[string]any{
		"type":    "string",
		"example": map[string]any{"type": "string", "nullable": true},
		"enum":    []any{"a", "b"},
		"default": "a",
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_ExclusiveMinimumBoolToNumeric(t *testing.T) {
	in := map[string]any{
		"type":             "integer",
		"minimum":          float64(0),
		"exclusiveMinimum": true,
	}
	want := map[string]any{
		"type":             "integer",
		"exclusiveMinimum": float64(0),
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_ExclusiveMinimumFalseDropped(t *testing.T) {
	in := map[string]any{
		"type":             "integer",
		"minimum":          float64(0),
		"exclusiveMinimum": false,
	}
	want := map[string]any{
		"type":    "integer",
		"minimum": float64(0),
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_NumericExclusiveMinimumPreserved(t *testing.T) {
	in := map[string]any{
		"type":             "integer",
		"exclusiveMinimum": float64(5),
	}
	want := map[string]any{
		"type":             "integer",
		"exclusiveMinimum": float64(5),
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_ExclusiveMinimumTrueWithoutPairedMinimum(t *testing.T) {
	in := map[string]any{
		"type":             "integer",
		"exclusiveMinimum": true,
	}
	want := map[string]any{"type": "integer"}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_ExclusiveMaximumBoolToNumeric(t *testing.T) {
	in := map[string]any{
		"type":             "integer",
		"maximum":          float64(100),
		"exclusiveMaximum": true,
	}
	want := map[string]any{
		"type":             "integer",
		"exclusiveMaximum": float64(100),
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_PokeAPIPaginatedShape(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count":    map[string]any{"type": "integer"},
			"next":     map[string]any{"type": "string", "nullable": true, "format": "uri"},
			"previous": map[string]any{"type": "string", "nullable": true, "format": "uri"},
			"results": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"url":  map[string]any{"type": "string", "format": "uri"},
					},
				},
			},
		},
		"required": []any{"count", "results"},
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count":    map[string]any{"type": "integer"},
			"next":     map[string]any{"type": []any{"string", "null"}, "format": "uri"},
			"previous": map[string]any{"type": []any{"string", "null"}, "format": "uri"},
			"results": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"url":  map[string]any{"type": "string", "format": "uri"},
					},
				},
			},
		},
		"required": []any{"count", "results"},
	}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\ngot  %#v\nwant %#v", got, want)
	}
}

func TestTranslateSchemaDialect_Salvages31StrayNullable(t *testing.T) {
	in := map[string]any{
		"type":       []any{"string", "null"},
		"properties": map[string]any{"x": map[string]any{"nullable": true}},
	}
	// The type array passes through verbatim; the stray nullable WITHOUT a
	// type has no union to contribute to and simply drops (both dialects
	// document that same outcome).
	want := map[string]any{
		"type":       []any{"string", "null"},
		"properties": map[string]any{"x": map[string]any{}},
	}
	got := translateSchemaDialect(in, "3.1")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_SalvagesNullableUnknownVersion(t *testing.T) {
	// The nullable salvage is version-independent: the keyword means one
	// thing wherever it appears, and OAPI-P-01 refuses genuinely unknown
	// versions at load anyway, so this path only sees version strings from
	// callers that bypassed discrimination.
	in := map[string]any{"type": "string", "nullable": true}
	want := map[string]any{"type": []any{"string", "null"}}
	got := translateSchemaDialect(in, "4.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_PassthroughNil(t *testing.T) {
	if got := translateSchemaDialect(nil, "3.0"); got != nil {
		t.Errorf("got %#v, want nil", got)
	}
}

func TestTranslateSchemaDialect_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"x": map[string]any{"type": "string", "nullable": true},
		},
	}
	// Deep-copy via marshal/unmarshal would be ideal; here we check that
	// the original map's nested "nullable" is still present after translation.
	_ = translateSchemaDialect(in, "3.0")
	props, _ := in["properties"].(map[string]any)
	x, _ := props["x"].(map[string]any)
	if x["nullable"] != true {
		t.Errorf("input was mutated: expected nullable: true to remain on the input copy")
	}
	if x["type"] != "string" {
		t.Errorf("input was mutated: expected original type to remain 'string'")
	}
}

// --- invalid-type salvage (OBI-D-17) -----------------------------------------
// Real-world specs ship schemas like {type: ''} (PokeAPI does, on
// evolution_detail.known_move). An invalid type constrains nothing; dropping
// the keyword salvages the interface instead of rejecting every operation.

func TestTranslateSchemaDialect_DropsEmptyStringType31(t *testing.T) {
	in := map[string]any{"type": "", "nullable": true, "description": "x"}
	// 3.1 now matches 3.0\'s documented outcome for this exact shape: the
	// nullable transform contributes "null", the invalid "" member drops,
	// and the union keeps what the author actually asserted.
	want := map[string]any{"type": []any{"null"}, "description": "x"}
	got := translateSchemaDialect(in, "3.1")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_EmptyTypeWithNullable30KeepsNull(t *testing.T) {
	in := map[string]any{"type": "", "nullable": true}
	// 3.0: the nullable transform contributes "null"; the invalid member is
	// filtered out of the resulting array.
	want := map[string]any{"type": []any{"null"}}
	got := translateSchemaDialect(in, "3.0")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_FiltersInvalidTypeArrayMembers(t *testing.T) {
	in := map[string]any{"type": []any{"string", "", "integer"}}
	want := map[string]any{"type": []any{"string", "integer"}}
	got := translateSchemaDialect(in, "3.1")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_DropsNonStringType(t *testing.T) {
	in := map[string]any{"type": float64(3)}
	want := map[string]any{}
	got := translateSchemaDialect(in, "3.1")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_SalvageRecursesIntoProperties31(t *testing.T) {
	// A property literally named "type" stays a property; only the keyword
	// position inside its schema is sanitized.
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type": map[string]any{"type": ""},
		},
	}
	want := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type": map[string]any{},
		},
	}
	got := translateSchemaDialect(in, "3.1")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestTranslateSchemaDialect_ValidTypesUntouched(t *testing.T) {
	in := map[string]any{"type": []any{"string", "null"}, "description": "ok"}
	got := translateSchemaDialect(in, "3.1")
	if !reflect.DeepEqual(got, in) {
		t.Errorf("got %#v, want %#v", got, in)
	}
}

func TestNormalizeOperationSchema_ReportsDrops(t *testing.T) {
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"known_move": map[string]any{"type": ""},
		},
	}
	var drops []string
	normalizeOperationSchema(in, "3.1", func(path, code, message string) {
		drops = append(drops, path+"="+code)
	})
	if len(drops) != 1 || drops[0] != "/properties/known_move/type=openapi.invalid_schema_type" {
		t.Errorf("drops = %#v, want one entry at /properties/known_move/type", drops)
	}
}
