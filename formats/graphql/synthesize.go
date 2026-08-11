package graphql

import (
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// convertToInterface inventories every declared root field. The operation
// schemas intentionally describe only the stable OpenBindings boundary and
// project the selected root-field application value.
func convertToInterface(schema *introspectionSchema, sourceLocation string, bindingSpecs ...string) (openbindings.Interface, error) {
	bindingSpec := BindingSpec
	if len(bindingSpecs) > 0 {
		bindingSpec = bindingSpecs[0]
	}
	sourceEntry := openbindings.Source{
		BindingSpec: bindingSpec,
	}
	if sourceLocation != "" {
		sourceEntry.Location = sourceLocation
	}

	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
	}

	tm := schema.typeMap()
	usedKeys := map[string]string{}

	// Process each root type in a stable order.
	rootTypes := []struct {
		label    string
		typeName string
	}{
		{"query", schema.rootTypeName("query")},
		{"mutation", schema.rootTypeName("mutation")},
		{"subscription", schema.rootTypeName("subscription")},
	}
	if bindingSpec == BindingSpec {
		rootTypes = rootTypes[:2]
	}

	for _, rt := range rootTypes {
		if rt.typeName == "" {
			continue
		}
		t, ok := tm[rt.typeName]
		if !ok {
			continue
		}

		fields := make([]field, len(t.Fields))
		copy(fields, t.Fields)
		sort.Slice(fields, func(i, j int) bool {
			return fields[i].Name < fields[j].Name
		})

		for _, f := range fields {
			if strings.HasPrefix(f.Name, "__") {
				continue
			}

			ref := rt.label + "/" + f.Name
			opKey := openbindings.SanitizeKey(f.Name)
			opKey = openbindings.ResolveKeyCollision(opKey, strings.ToLower(rt.label), usedKeys)
			usedKeys[opKey] = ref

			op := openbindings.Operation{}
			if f.Description != "" {
				op.Description = f.Description
			}
			if f.IsDeprecated {
				op.Deprecated = true
			}

			op.Input = map[string]any{"type": "object"}
			op.Output = graphQLValueSchema(f.Type, tm)

			iface.Operations[opKey] = op

			bindingKey := opKey + "." + DefaultSourceName
			iface.Bindings[bindingKey] = openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Ref:       ref,
			}
		}
	}

	return iface, nil
}

func graphQLValueSchema(ref typeRef, tm map[string]*fullType) map[string]any {
	if ref.Kind == "NON_NULL" && ref.OfType != nil {
		return graphQLNonNullSchema(*ref.OfType, tm)
	}
	base := graphQLNonNullSchema(ref, tm)
	if len(base) == 0 {
		return base
	}
	return map[string]any{"anyOf": []any{base, map[string]any{"type": "null"}}}
}

func graphQLNonNullSchema(ref typeRef, tm map[string]*fullType) map[string]any {
	switch ref.Kind {
	case "LIST":
		if ref.OfType == nil {
			return map[string]any{"type": "array"}
		}
		return map[string]any{"type": "array", "items": graphQLValueSchema(*ref.OfType, tm)}
	case "SCALAR":
		switch ref.Name {
		case "String", "ID":
			return map[string]any{"type": "string"}
		case "Boolean":
			return map[string]any{"type": "boolean"}
		case "Int":
			return map[string]any{"type": "integer"}
		case "Float":
			return map[string]any{"type": "number"}
		default:
			return map[string]any{}
		}
	case "ENUM":
		schema := map[string]any{"type": "string"}
		if named := tm[ref.Name]; named != nil && len(named.EnumValues) > 0 {
			values := make([]any, 0, len(named.EnumValues))
			for _, value := range named.EnumValues {
				values = append(values, value.Name)
			}
			schema["enum"] = values
		}
		return schema
	case "OBJECT", "INTERFACE", "UNION":
		// The executable document controls nested selection and response aliases.
		// An open object is the strongest schema introspection alone can state
		// without rejecting valid document-selected application values.
		return map[string]any{"type": "object"}
	default:
		return map[string]any{}
	}
}

func graphQLResponseSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"data": map[string]any{"type": []any{"object", "null"}},
			"errors": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items":    map[string]any{"type": "object"},
			},
			"extensions": map[string]any{"type": "object"},
		},
		"anyOf": []any{
			map[string]any{"required": []any{"data"}},
			map[string]any{"required": []any{"errors"}},
		},
	}
}
