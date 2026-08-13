package asyncapi

import "strings"

// translateSchemaDialect rewrites an AsyncAPI Schema Object into the JSON
// Schema 2020-12 dialect an OBI schema position requires (core OBI-D-06,
// OBI-D-17). Every accepted AsyncAPI edition's default Schema Object is a
// superset of JSON Schema Draft 07, so a verbatim copy is faithful only where
// the two dialects agree; where they diverge, copying either produces an
// invalid OBI (tuple `items`, Draft-07 `$id` forms, `$schema`) or — worse —
// a valid schema that silently means something the author did not write
// (`dependencies` and `additionalItems` become inert annotations; assertion
// keywords beside `$ref` become active). The binding specification names this
// boundary in §9.2; the mapping itself is synthesis behavior, pinned by the
// portable conformance scenarios.
//
// Translations:
//
//   - `$schema` is dropped: translation asserts the OBI dialect.
//   - array-form `items` → `prefixItems`; a co-present `additionalItems`
//     then becomes `items`. `additionalItems` without a tuple was inert in
//     Draft 07 and is dropped.
//   - `dependencies` splits: string-array values → `dependentRequired`,
//     schema values → `dependentSchemas`.
//   - `$id` keeps only absolute URIs. A plain-name fragment (`#name`)
//     becomes the 2020-12 `$anchor` it turned into; any other non-absolute
//     form is dropped — a reference that depended on it surfaces loudly as
//     an unresolvable pointer rather than silently rebasing.
//   - Beside a `$ref`, Draft 07 ignores assertion keywords entirely, so they
//     are dropped to preserve the author's operative meaning; annotations
//     and unknown keywords stay. (Internal references are resolved wholesale
//     before translation, which is the same ignore semantics; this rule
//     covers references that remain — external or deliberately kept.)
//
// Recursion is keyword-position-aware: literal-value keywords (`enum`,
// `const`, `default`, `examples`) are never entered, while map-of-schema
// keywords (`properties`, …) translate every member value regardless of the
// member's name. Unknown keywords pass through untouched: AsyncAPI's Schema
// Object extensions (`discriminator`, `externalDocs`, `x-…`) are legitimate
// annotations in 2020-12.
func translateSchemaDialect(schema map[string]any) map[string]any {
	out, _ := translateSchemaNode(schema).(map[string]any)
	return out
}

var translateSchemaBearingMapKeys = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
	"dependentSchemas":  true,
}

var translateSchemaBearingArrayKeys = map[string]bool{
	"oneOf":       true,
	"anyOf":       true,
	"allOf":       true,
	"prefixItems": true,
}

var translateSchemaBearingSingleKeys = map[string]bool{
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
	"additionalItems":       true,
	"items":                 true,
}

// draft07AssertionKeys are the validation keywords Draft 07 ignores beside a
// `$ref` (spelled post-translation, so the drop applies after rewrites).
var draft07AssertionKeys = map[string]bool{
	"type": true, "enum": true, "const": true,
	"multipleOf": true, "maximum": true, "exclusiveMaximum": true,
	"minimum": true, "exclusiveMinimum": true,
	"maxLength": true, "minLength": true, "pattern": true,
	"items": true, "prefixItems": true, "maxItems": true, "minItems": true,
	"uniqueItems": true, "contains": true, "maxContains": true, "minContains": true,
	"maxProperties": true, "minProperties": true, "required": true,
	"properties": true, "patternProperties": true, "additionalProperties": true,
	"dependentRequired": true, "dependentSchemas": true, "propertyNames": true,
	"if": true, "then": true, "else": true,
	"allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"unevaluatedItems": true, "unevaluatedProperties": true,
}

func translateSchemaNode(node any) any {
	switch v := node.(type) {
	case map[string]any:
		return translateSchemaObject(v)
	case bool:
		return v
	default:
		return node
	}
}

func translateSchemaObject(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))

	for key, value := range in {
		switch {
		case key == "$schema":
			continue
		case key == "$id":
			text, ok := value.(string)
			if !ok {
				continue
			}
			if isAbsoluteURI(text) {
				out["$id"] = text
			} else if anchor, ok := plainNameFragmentAnchor(text); ok {
				if _, exists := in["$anchor"]; !exists {
					out["$anchor"] = anchor
				}
			}
			// Other non-absolute forms drop; dependent refs dangle loudly.
			continue
		case key == "dependencies":
			deps, ok := value.(map[string]any)
			if !ok {
				continue
			}
			required := map[string]any{}
			schemas := map[string]any{}
			for name, dep := range deps {
				if names, ok := dep.([]any); ok {
					required[name] = names
				} else {
					schemas[name] = translateSchemaNode(dep)
				}
			}
			mergeAbsent(out, in, "dependentRequired", required)
			mergeAbsent(out, in, "dependentSchemas", schemas)
			continue
		case key == "items":
			if tuple, ok := value.([]any); ok {
				prefix := make([]any, len(tuple))
				for i, member := range tuple {
					prefix[i] = translateSchemaNode(member)
				}
				if authored, exists := in["prefixItems"].([]any); !exists || len(authored) == 0 {
					out["prefixItems"] = prefix
				}
				// additionalItems handled below against the tuple form.
				if rest, ok := in["additionalItems"]; ok {
					if _, exists := in["items"].(map[string]any); !exists {
						out["items"] = translateSchemaNode(rest)
					}
				}
				continue
			}
			out["items"] = translateSchemaNode(value)
			continue
		case key == "additionalItems":
			// Meaningful only with a tuple (handled above); inert otherwise
			// in Draft 07, so dropping preserves meaning.
			continue
		case translateSchemaBearingMapKeys[key]:
			members, ok := value.(map[string]any)
			if !ok {
				out[key] = value
				continue
			}
			translated := make(map[string]any, len(members))
			for name, member := range members {
				translated[name] = translateSchemaNode(member)
			}
			out[key] = translated
			continue
		case key == "prefixItems":
			// Draft 07 does not define prefixItems, so the author's dialect
			// ignored it. Carrying an empty array into 2020-12 would turn an
			// inert annotation into an ill-formed keyword (2020-12 requires a
			// non-empty array); dropping preserves the declared semantics
			// exactly. Non-empty arrays carry through with members translated.
			members, ok := value.([]any)
			if !ok || len(members) == 0 {
				continue
			}
			translated := make([]any, len(members))
			for i, member := range members {
				translated[i] = translateSchemaNode(member)
			}
			out[key] = translated
			continue
		case translateSchemaBearingArrayKeys[key]:
			members, ok := value.([]any)
			if !ok {
				out[key] = value
				continue
			}
			translated := make([]any, len(members))
			for i, member := range members {
				translated[i] = translateSchemaNode(member)
			}
			out[key] = translated
			continue
		case translateSchemaBearingSingleKeys[key]:
			out[key] = translateSchemaNode(value)
			continue
		default:
			out[key] = value
		}
	}

	// Draft 07: a schema containing $ref ignores assertion siblings; keeping
	// them in 2020-12 would activate constraints the author's dialect made
	// inert. Annotations and unknown keywords stay.
	if _, hasRef := out["$ref"].(string); hasRef {
		for key := range out {
			if key != "$ref" && draft07AssertionKeys[key] {
				delete(out, key)
			}
		}
	}

	return out
}

func mergeAbsent(out, in map[string]any, key string, members map[string]any) {
	if len(members) == 0 {
		return
	}
	if existing, ok := in[key].(map[string]any); ok {
		merged := make(map[string]any, len(existing)+len(members))
		for name, member := range members {
			merged[name] = member
		}
		for name, member := range existing {
			merged[name] = translateSchemaNode(member)
		}
		out[key] = merged
		return
	}
	out[key] = members
}

func isAbsoluteURI(text string) bool {
	colon := strings.IndexByte(text, ':')
	if colon <= 0 {
		return false
	}
	for _, r := range text[:colon] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return false
		}
	}
	first := text[0]
	return (first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z') && !strings.Contains(text, "#")
}

// plainNameFragmentAnchor recognizes Draft 07's `$id: "#name"` form, which
// 2020-12 split into `$anchor`.
func plainNameFragmentAnchor(text string) (string, bool) {
	if !strings.HasPrefix(text, "#") || len(text) < 2 {
		return "", false
	}
	name := text[1:]
	for i, r := range name {
		valid := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' ||
			(i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '.'))
		if !valid {
			return "", false
		}
	}
	return name, true
}
