package openapi

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

type openAPISchemaProjector struct{}

// projectOpenAPISchema clones an already-decycled operation schema without
// treating readOnly or writeOnly annotations as member-deletion instructions.
// OAS leaves enforcement of those annotations to the application, and the
// binding neither deletes a supplied wire member nor synthesizes one.
//
// direction and rootExemptions remain explicit inputs because they define the
// synthesis boundary and are part of the adapter's stable internal API. The
// current family rules preserve annotated members in both directions.
func projectOpenAPISchema(schema map[string]any, direction openAPISchemaDirection, rootExemptions map[string]bool) map[string]any {
	return projectOpenAPISchemaWithRegistry(schema, direction, rootExemptions, nil)
}

// projectOpenAPISchemaWithRegistry is projectOpenAPISchema over a schema whose
// `$ref`s have not been inlined yet. The registry is retained in the signature
// for the registry projection boundary; preserving annotated members means it
// does not need to be resolved during this cloning pass.
func projectOpenAPISchemaWithRegistry(schema map[string]any, direction openAPISchemaDirection, rootExemptions map[string]bool, registry map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	_ = direction
	_ = rootExemptions
	_ = registry
	projector := openAPISchemaProjector{}
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

	_ = root
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
				required = append(required, rawName)
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
