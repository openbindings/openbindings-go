package openapi

import "strings"

// translateSchemaDialect rewrites a JSON schema from OpenAPI 3.0's Draft-4
// subset dialect into JSON Schema 2020-12. OBI documents are required to use
// the 2020-12 dialect (spec §6.2, OBI-D-07), so 3.0 sources must be normalized
// at synthesis time.
//
// Translations performed when openapiVersion is in the 3.0 family:
//
//   - {type: T, nullable: true}        → {type: [T, "null"]}
//   - {type: [...], nullable: true}    → {type: [..., "null"]}
//   - {nullable: true} without type    → drop the keyword
//   - {minimum: N, exclusiveMinimum: true}   → {exclusiveMinimum: N}
//   - {exclusiveMinimum: false} (or unpaired) → drop the keyword
//   - same for maximum / exclusiveMaximum
//
// 3.1 sources pass through unchanged (3.1 schemas are already 2020-12).
// Unknown versions also pass through (forward-compatible).
func translateSchemaDialect(schema map[string]any, openapiVersion string) map[string]any {
	if !isOpenAPI30(openapiVersion) {
		return schema
	}
	if schema == nil {
		return nil
	}
	out, _ := translateNode(schema).(map[string]any)
	return out
}

func isOpenAPI30(version string) bool {
	return version == "3.0" || strings.HasPrefix(version, "3.0.")
}

var schemaBearingMapKeys = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
	"dependentSchemas":  true,
}

var schemaBearingArrayKeys = map[string]bool{
	"oneOf":       true,
	"anyOf":       true,
	"allOf":       true,
	"prefixItems": true,
}

var schemaBearingSingleKeys = map[string]bool{
	"items":                  true,
	"additionalProperties":   true,
	"not":                    true,
	"if":                     true,
	"then":                   true,
	"else":                   true,
	"propertyNames":          true,
	"contains":               true,
	"unevaluatedItems":       true,
	"unevaluatedProperties":  true,
}

func translateNode(node any) any {
	switch v := node.(type) {
	case map[string]any:
		return translateObject(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = translateNode(item)
		}
		return out
	default:
		return v
	}
}

func translateObject(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))

	for k, v := range in {
		if k == "nullable" || k == "exclusiveMinimum" || k == "exclusiveMaximum" {
			continue
		}
		switch {
		case schemaBearingMapKeys[k]:
			out[k] = translateSchemaMap(v)
		case schemaBearingArrayKeys[k]:
			out[k] = translateSchemaArray(v)
		case schemaBearingSingleKeys[k]:
			out[k] = translateNode(v)
		default:
			out[k] = v
		}
	}

	if nullable, ok := in["nullable"].(bool); ok && nullable {
		switch t := in["type"].(type) {
		case string:
			out["type"] = []any{t, "null"}
		case []any:
			if containsString(t, "null") {
				cp := make([]any, len(t))
				copy(cp, t)
				out["type"] = cp
			} else {
				cp := make([]any, 0, len(t)+1)
				cp = append(cp, t...)
				cp = append(cp, "null")
				out["type"] = cp
			}
		}
	}

	if exMin, ok := in["exclusiveMinimum"].(bool); ok {
		if exMin {
			if m, hasMin := numericValue(in["minimum"]); hasMin {
				out["exclusiveMinimum"] = m
				delete(out, "minimum")
			}
		}
	} else if m, hasNum := numericValue(in["exclusiveMinimum"]); hasNum {
		out["exclusiveMinimum"] = m
	}

	if exMax, ok := in["exclusiveMaximum"].(bool); ok {
		if exMax {
			if m, hasMax := numericValue(in["maximum"]); hasMax {
				out["exclusiveMaximum"] = m
				delete(out, "maximum")
			}
		}
	} else if m, hasNum := numericValue(in["exclusiveMaximum"]); hasNum {
		out["exclusiveMaximum"] = m
	}

	return out
}

func translateSchemaMap(value any) any {
	m, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = translateNode(v)
	}
	return out
}

func translateSchemaArray(value any) any {
	arr, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, len(arr))
	for i, item := range arr {
		out[i] = translateNode(item)
	}
	return out
}

func containsString(arr []any, s string) bool {
	for _, item := range arr {
		if str, ok := item.(string); ok && str == s {
			return true
		}
	}
	return false
}

// numericValue returns the value as a number-typed `any` if it is one of the
// common JSON-decoded numeric types. The encoding/json default decodes all JSON
// numbers as float64; we also accept int and json.Number for callers that use
// alternate decoders.
func numericValue(v any) (any, bool) {
	switch v.(type) {
	case float64, float32, int, int32, int64:
		return v, true
	}
	return nil, false
}
