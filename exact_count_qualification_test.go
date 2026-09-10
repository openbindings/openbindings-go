package openbindings

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestExactLargeCountQualification(t *testing.T) {
	for _, test := range []struct {
		keyword string
		value   any
		valid   bool
	}{
		{"minItems", []any{}, false}, {"maxItems", []any{1}, true},
		{"minLength", "", false}, {"maxLength", "a", true},
		{"minProperties", map[string]any{}, false}, {"maxProperties", map[string]any{"a": 1}, true},
	} {
		t.Run(test.keyword, func(t *testing.T) {
			schema := map[string]any{test.keyword: json.Number("18446744073709551616")}
			err := ValidateAgainstSchema(test.value, schema, nil)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v; want %v; error=%v", err == nil, test.valid, err)
			}
		})
	}
}

func TestExactLargeCountBranchesAndDialects(t *testing.T) {
	for _, token := range []string{"9223372036854775808", "18446744073709551616", "18446744073709551616.0", "18446744073709551616e0", "1e40"} {
		for _, draft := range []string{"http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft/2019-09/schema", "https://json-schema.org/draft/2020-12/schema"} {
			for _, contains := range []any{true, false, map[string]any{"type": "integer"}} {
				for _, mode := range []string{"absent", "minimum", "maximum", "both"} {
					for _, data := range []any{[]any{}, []any{1}, []any{1, "x"}, "not an array"} {
						name := fmt.Sprintf("%s/%s/%v/%s/%v", token, draft, contains, mode, data)
						t.Run(name, func(t *testing.T) {
							schema := map[string]any{"contains": contains}
							if mode == "minimum" || mode == "both" {
								schema["minContains"] = json.Number(token)
							}
							if mode == "maximum" || mode == "both" {
								schema["maxContains"] = json.Number(token)
							}
							valid := true
							if array, ok := data.([]any); ok {
								matches := len(array) > 0 && contains != false
								valid = matches
								if draft != "http://json-schema.org/draft-07/schema#" && (mode == "minimum" || mode == "both") {
									valid = false
								}
							}
							for _, wrapper := range []string{"direct", "allOf", "anyOf", "oneOf", "not", "if", "$ref"} {
								root := map[string]any{"$schema": draft}
								want := valid
								switch wrapper {
								case "direct":
									for key, value := range schema {
										root[key] = value
									}
								case "allOf", "anyOf", "oneOf":
									root[wrapper] = []any{schema}
								case "not":
									root[wrapper] = schema
									want = !valid
								case "if":
									root["if"] = schema
									root["then"] = false
									root["else"] = true
									want = !valid
								case "$ref":
									root["definitions"] = map[string]any{"target": schema}
									root["$ref"] = "#/definitions/target"
								}
								compiler := exactCountCompiler()
								if err := compiler.AddResource("urn:test:counts", root); err != nil {
									t.Fatal(err)
								}
								compiled, err := compiler.Compile("urn:test:counts")
								if err != nil {
									t.Fatal(err)
								}
								for repeat := 0; repeat < 3; repeat++ {
									err = compiled.Validate(data)
									if (err == nil) != want {
										t.Fatalf("%s: valid=%v want=%v: %v", wrapper, err == nil, want, err)
									}
								}
							}
						})
					}
				}
			}
		}
	}
}

func TestExactCountFailedBranchDoesNotLeakCoverage(t *testing.T) {
	schema := map[string]any{
		"anyOf": []any{
			map[string]any{"contains": true, "minContains": json.Number("18446744073709551616")},
			map[string]any{"prefixItems": []any{true}},
		},
		"unevaluatedItems": false,
	}
	for _, test := range []struct {
		data  []any
		valid bool
	}{{[]any{1}, true}, {[]any{1, 2}, false}} {
		if err := ValidateAgainstSchema(test.data, schema, nil); (err == nil) != test.valid {
			t.Fatalf("%v: %v", test.data, err)
		}
	}
}
