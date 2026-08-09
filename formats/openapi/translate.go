package openapi

import (
	"fmt"
	"strconv"
	"strings"
)

// translateSchemaDialect rewrites a JSON schema from OpenAPI 3.0's Draft-4
// subset dialect into JSON Schema 2020-12. OBI documents are required to use
// the 2020-12 dialect (spec §6.2, OBI-D-06), so 3.0 sources must be normalized
// at synthesis time.
//
// Translations performed when openapiVersion is in the 3.0 family:
//
//   - {minimum: N, exclusiveMinimum: true}   → {exclusiveMinimum: N}
//   - {exclusiveMinimum: false} (or unpaired) → drop the keyword
//   - same for maximum / exclusiveMaximum
//
// Additional translations performed only for OpenAPI 3.0:
//
//   - {type: T, nullable: true}        → {type: [T, "null"]}
//   - {type: [...], nullable: true}    → {type: [..., "null"]}
//   - {nullable: true} without type    → drop the keyword
//   - {nullable: false}                → drop the keyword
//
// OpenAPI 3.1 uses JSON Schema 2020-12 and removed nullable. There it is an
// unknown, inert annotation: synthesis preserves it when the typed source
// path retained it, but never widens the artifact's asserted type. Guessing
// that a stray 3.0 spelling carries the old meaning would overfit to common
// generators and change the accepted instance set without 3.1 authority.
//
// For EVERY version the walk additionally salvages obviously-invalid
// `type` constraints that would fail OBI validation (OBI-D-17) — see
// normalizeOperationSchema. Unknown versions get the salvage passes only
// (forward-compatible).
func translateSchemaDialect(schema map[string]any, openapiVersion string) map[string]any {
	return normalizeOperationSchema(schema, openapiVersion, nil)
}

// normalizeOperationSchema is translateSchemaDialect with drop evidence: it
// rewrites the 3.0 dialect when applicable and, for all versions, salvages
// `type` values that are not JSON Schema 2020-12 type names — a string that
// is not one of the seven primitives is dropped, invalid members of a type
// array are filtered out (dropping the keyword when nothing valid remains).
// Real-world specs ship these (PokeAPI's openapi.yml declares `type: ”`),
// and an invalid type constrains nothing — rejecting a whole
// multi-operation interface over it hands the user a dead end for a spec
// every other tool accepts. Salvage is never silent: each dropped token is
// reported through onDrop with a JSON-pointer-style path into the schema so
// callers can emit a SynthesizerWarning.
func normalizeOperationSchema(schema map[string]any, openapiVersion string, report func(path, code, message string)) map[string]any {
	if schema == nil {
		return nil
	}
	w := &schemaWalker{legacy: isOpenAPI30(openapiVersion), report: report}
	out, _ := w.node(schema, "").(map[string]any)
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
	"items":                 true,
	"additionalProperties":  true,
	"not":                   true,
	"if":                    true,
	"then":                  true,
	"else":                  true,
	"propertyNames":         true,
	"contains":              true,
	"contentSchema":         true,
	"unevaluatedItems":      true,
	"unevaluatedProperties": true,
}

// validSchemaTypes are the seven JSON Schema 2020-12 primitive type names —
// the only values OBI-D-17 admits in a `type` keyword.
var validSchemaTypes = map[string]bool{
	"array":   true,
	"boolean": true,
	"integer": true,
	"null":    true,
	"number":  true,
	"object":  true,
	"string":  true,
}

// schemaWalker carries the per-run configuration through the recursive
// rewrite: whether 3.0-dialect transforms apply, and where to report
// salvage drops. Paths use JSON-pointer notation rooted at the schema
// being normalized.
type schemaWalker struct {
	legacy bool
	report func(path, code, message string)
}

func (w *schemaWalker) node(node any, path string) any {
	switch v := node.(type) {
	case map[string]any:
		return w.object(v, path)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = w.node(item, path+"/"+strconv.Itoa(i))
		}
		return out
	default:
		return v
	}
}

func (w *schemaWalker) object(in map[string]any, path string) map[string]any {
	out := make(map[string]any, len(in))

	for k, v := range in {
		// nullable is a dialect keyword only in 3.0. In 3.1 it is unknown
		// annotation data and remains inert rather than widening type.
		if w.legacy && (k == "nullable" || k == "exclusiveMinimum" || k == "exclusiveMaximum") {
			continue
		}
		switch {
		case schemaBearingMapKeys[k]:
			out[k] = w.schemaMap(v, path+"/"+k)
		case schemaBearingArrayKeys[k]:
			out[k] = w.schemaArray(v, path+"/"+k)
		case schemaBearingSingleKeys[k]:
			out[k] = w.node(v, path+"/"+k)
		default:
			out[k] = v
		}
	}

	// nullable changes validation semantics only in the OpenAPI 3.0 dialect.
	if nullable, ok := in["nullable"].(bool); w.legacy && ok && nullable {
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

	if w.legacy {
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
	}

	w.salvageType(out, path)

	return out
}

// salvageType drops `type` values that no 2020-12 validator accepts. It runs
// after the legacy transforms so a 3.0 {type: ”, nullable: true} still keeps
// the "null" member the nullable transform contributed.
func (w *schemaWalker) salvageType(out map[string]any, path string) {
	t, present := out["type"]
	if !present {
		return
	}
	switch v := t.(type) {
	case string:
		if !validSchemaTypes[v] {
			delete(out, "type")
			w.drop(path+"/type", v)
		}
	case []any:
		kept := make([]any, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && validSchemaTypes[s] {
				kept = append(kept, item)
				continue
			}
			w.drop(path+"/type", stringify(item))
		}
		if len(kept) == 0 {
			delete(out, "type")
		} else if len(kept) < len(v) {
			out["type"] = kept
		}
	default:
		delete(out, "type")
		w.drop(path+"/type", stringify(v))
	}
}

func (w *schemaWalker) drop(path, token string) {
	if w.report != nil {
		w.report(path, "openapi.invalid_schema_type", fmt.Sprintf(
			"dropped invalid JSON Schema type %q declared by the source artifact; the salvaged position accepts any value",
			token,
		))
	}
}

func (w *schemaWalker) schemaMap(value any, path string) any {
	m, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = w.node(v, path+"/"+escapeJSONPointerSegment(k))
	}
	return out
}

func (w *schemaWalker) schemaArray(value any, path string) any {
	arr, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, len(arr))
	for i, item := range arr {
		out[i] = w.node(item, path+"/"+strconv.Itoa(i))
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

// stringify renders a dropped token for the warning message. fmt.Sprintf
// handles every JSON-decoded shape; strings pass through unchanged so the
// common case (`type: ”` → "") reads literally.
func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
