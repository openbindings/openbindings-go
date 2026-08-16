package openapi

import "strings"

// openAPISchemaDirection names the data direction represented by a
// synthesized operation boundary. Directionality is an OpenAPI adapter
// concern: it is projected before the schema enters the protocol-neutral OBI.
type openAPISchemaDirection uint8

const (
	openAPIRequestSchema openAPISchemaDirection = iota
	openAPIResponseSchema
)

var directionalSchemaMapKeys = map[string]bool{
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
	"dependentSchemas":  true,
}

var directionalSchemaArrayKeys = map[string]bool{
	"oneOf":       true,
	"anyOf":       true,
	"allOf":       true,
	"prefixItems": true,
}

var directionalSchemaSingleKeys = map[string]bool{
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

type openAPISchemaProjector struct {
	direction      openAPISchemaDirection
	rootExemptions map[string]bool
	defs           map[string]any
	// registry, when set, lets the ownership calculation follow an artifact-space
	// `#/components/schemas/...` reference the way it follows a local `$defs`
	// one. It is set only when projecting the reference registry itself (see
	// projectRefRegistry): there the references have not been inlined yet, so a
	// property whose schema is a reference to a readOnly component would
	// otherwise look unannotated.
	registry map[string]any
	// omitted records whether this projection dropped anything. Callers that
	// project a whole registry use it to tell the two directions apart without
	// comparing their results.
	omitted bool
}

// projectOpenAPISchema applies OpenAPI read/write directionality to an
// already-decycled operation schema. Request projection omits readOnly
// properties; response projection omits writeOnly properties. Applying the
// projection after decycling deliberately leaves stable $defs names and refs
// untouched while still projecting every reachable definition. Projection can
// however remove the last reference to a hoisted definition, so callers close
// the emitted schema over reachability afterwards (pruneUnreachableDefs).
//
// rootExemptions names request-wrapper properties that are not source-schema
// properties at all (parameters and a synthetic whole-body field). A
// readOnly annotation on such a schema root cannot erase the independently
// declared OpenAPI input; nested properties are still projected normally.
func projectOpenAPISchema(schema map[string]any, direction openAPISchemaDirection, rootExemptions map[string]bool) map[string]any {
	return projectOpenAPISchemaWithRegistry(schema, direction, rootExemptions, nil)
}

// projectOpenAPISchemaWithRegistry is projectOpenAPISchema over a schema whose
// `$ref`s have not been inlined yet. `registry` lets the ownership calculation
// read a referenced declaration's annotations, which the inlined form would have
// carried inline.
func projectOpenAPISchemaWithRegistry(schema map[string]any, direction openAPISchemaDirection, rootExemptions map[string]bool, registry map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	projector := openAPISchemaProjector{direction: direction, rootExemptions: rootExemptions, registry: registry}
	projector.defs, _ = schema["$defs"].(map[string]any)
	projected, ok := projector.project(schema, true).(map[string]any)
	if !ok {
		return schema
	}
	return projected
}

func requestSchemaProjectionExemptions(routes abstractInputRoutes) map[string]bool {
	exemptions := make(map[string]bool, len(routes.parameters)+1)
	for _, route := range routes.parameters {
		exemptions[route.Field] = true
	}
	if routes.wholeBodyField != "" {
		exemptions[routes.wholeBodyField] = true
	}
	return exemptions
}

func (p *openAPISchemaProjector) project(value any, root bool) any {
	schema, ok := value.(map[string]any)
	if !ok {
		if items, ok := value.([]any); ok {
			out := make([]any, len(items))
			for i, item := range items {
				out[i] = p.project(item, false)
			}
			return out
		}
		return value
	}

	var exemptions map[string]bool
	if root {
		exemptions = p.rootExemptions
	}
	omitted := p.omittedProperties(schema, exemptions, map[string]bool{})
	out := make(map[string]any, len(schema))
	for key, raw := range schema {
		switch {
		case key == "properties":
			properties, ok := raw.(map[string]any)
			if !ok {
				out[key] = raw
				continue
			}
			projected := make(map[string]any, len(properties))
			for name, property := range properties {
				if omitted[name] {
					p.omitted = true
					continue
				}
				projected[name] = p.project(property, false)
			}
			out[key] = projected

		case key == "required":
			names, ok := raw.([]any)
			if !ok {
				if strings, ok := raw.([]string); ok {
					names = make([]any, len(strings))
					for i, name := range strings {
						names[i] = name
					}
				} else {
					out[key] = raw
					continue
				}
			}
			required := make([]any, 0, len(names))
			for _, rawName := range names {
				name, ok := rawName.(string)
				if ok && !omitted[name] {
					required = append(required, name)
				}
			}
			if len(required) > 0 {
				out[key] = required
			}

		case directionalSchemaMapKeys[key]:
			values, ok := raw.(map[string]any)
			if !ok {
				out[key] = raw
				continue
			}
			projected := make(map[string]any, len(values))
			for name, nested := range values {
				projected[name] = p.project(nested, false)
			}
			out[key] = projected

		case directionalSchemaArrayKeys[key]:
			items, ok := raw.([]any)
			if !ok {
				out[key] = raw
				continue
			}
			projected := make([]any, len(items))
			for i, item := range items {
				projected[i] = p.project(item, false)
			}
			out[key] = projected

		case directionalSchemaSingleKeys[key]:
			out[key] = p.project(raw, false)

		default:
			out[key] = raw
		}
	}
	return out
}

// omittedProperties finds the names excluded at one schema's own instance
// boundary. allOf members jointly describe that same instance, so their
// omissions also repair a required list owned by the parent schema. Local
// $defs refs are followed only for this ownership calculation; emitted refs
// remain unchanged.
func (p *openAPISchemaProjector) omittedProperties(schema map[string]any, exemptions map[string]bool, seenRefs map[string]bool) map[string]bool {
	omitted := map[string]bool{}
	if resolved, ref, ok := p.resolveLocalRef(schema); ok {
		if seenRefs[ref] {
			return omitted
		}
		seenRefs[ref] = true
		for name := range p.omittedProperties(resolved, exemptions, seenRefs) {
			omitted[name] = true
		}
		delete(seenRefs, ref)
		return omitted
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, property := range properties {
			if !exemptions[name] && p.excludedProperty(property, map[string]bool{}) {
				omitted[name] = true
			}
		}
	}
	if members, ok := schema["allOf"].([]any); ok {
		for _, member := range members {
			nested, ok := member.(map[string]any)
			if !ok {
				continue
			}
			for name := range p.omittedProperties(nested, nil, seenRefs) {
				omitted[name] = true
			}
		}
	}
	return omitted
}

func (p *openAPISchemaProjector) excludedProperty(value any, seenRefs map[string]bool) bool {
	schema, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if resolved, ref, ok := p.resolveLocalRef(schema); ok {
		if seenRefs[ref] {
			return false
		}
		seenRefs[ref] = true
		return p.excludedProperty(resolved, seenRefs)
	}
	if p.direction == openAPIRequestSchema {
		excluded, _ := schema["readOnly"].(bool)
		return excluded
	}
	excluded, _ := schema["writeOnly"].(bool)
	return excluded
}

func (p *openAPISchemaProjector) resolveLocalRef(schema map[string]any) (map[string]any, string, bool) {
	ref, ok := schema["$ref"].(string)
	if !ok {
		return nil, "", false
	}
	if p.registry != nil {
		if resolved, found := resolveRegistryRef(ref, p.registry); found {
			if target, ok := resolved.(map[string]any); ok {
				return target, ref, true
			}
		}
	}
	if p.defs == nil {
		return nil, "", false
	}
	marker := "/$defs/"
	index := strings.LastIndex(ref, marker)
	if index < 0 {
		if strings.HasPrefix(ref, "#/$defs/") {
			index = 0
			marker = "#/$defs/"
		} else {
			return nil, "", false
		}
	}
	encoded := ref[index+len(marker):]
	if slash := strings.IndexByte(encoded, '/'); slash >= 0 {
		encoded = encoded[:slash]
	}
	name := unescapeJSONPointerSegment(encoded)
	resolved, ok := p.defs[name].(map[string]any)
	return resolved, ref, ok
}
