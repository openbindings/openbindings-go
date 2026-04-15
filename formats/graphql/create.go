package graphql

import (
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// convertToInterface converts a GraphQL introspection schema to an OpenBindings interface.
func convertToInterface(schema *introspectionSchema, sourceLocation string) (openbindings.Interface, error) {
	sourceEntry := openbindings.Source{
		Format: FormatToken,
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
		{"Query", schema.rootTypeName("Query")},
		{"Mutation", schema.rootTypeName("Mutation")},
		{"Subscription", schema.rootTypeName("Subscription")},
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

			// Build the full GraphQL query at creation time.
			queryStr, _, _ := buildQueryFromIntrospection(schema, rt.label, f.Name, nil)

			// Build input schema from field arguments, with the pre-built
			// query embedded as a const property.
			op.Input = argsToInputSchemaWithQuery(f.Args, tm, queryStr)

			// Build output schema from return type.
			returnTypeName := unwrapTypeName(f.Type)
			if returnTypeName != "" {
				op.Output = graphqlTypeToJSONSchema(f.Type, tm, make(map[string]bool))
			}

			iface.Operations[opKey] = op

			bindingKey := opKey + "." + DefaultSourceName
			iface.Bindings[bindingKey] = openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Ref:       ref,
			}
		}
	}

	// GraphQL introspection does not expose security metadata, so we
	// leave the security section empty. If the server requires auth, the
	// executor's auth retry flow will handle it (401 -> resolve
	// credentials -> retry).

	return iface, nil
}

// argsToInputSchemaWithQuery converts field arguments to a JSON Schema and
// embeds the pre-built GraphQL query as a _query const property.
func argsToInputSchemaWithQuery(args []inputValue, tm map[string]*fullType, queryStr string) map[string]any {
	schema := argsToInputSchema(args, tm)
	if queryStr == "" {
		return schema
	}
	// Ensure properties map exists.
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		schema["properties"] = props
	}
	props[queryFieldName] = map[string]any{
		"type":  "string",
		"const": queryStr,
	}
	return schema
}

// argsToInputSchema converts a list of GraphQL field arguments to a JSON Schema
// describing the operation's input.
func argsToInputSchema(args []inputValue, tm map[string]*fullType) map[string]any {
	properties := map[string]any{}
	var required []string

	for _, arg := range args {
		isRequired := arg.Type.Kind == "NON_NULL"
		argType := arg.Type
		if isRequired && argType.OfType != nil {
			argType = *argType.OfType
		}

		prop := graphqlTypeToJSONSchema(argType, tm, make(map[string]bool))
		if arg.Description != "" {
			prop["description"] = arg.Description
		}
		properties[arg.Name] = prop
		if isRequired {
			required = append(required, arg.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = toAnySlice(required)
	}
	return schema
}

// graphqlTypeToJSONSchema converts a GraphQL type reference to a JSON Schema.
// Uses cycle detection via the visited set to handle recursive types.
func graphqlTypeToJSONSchema(t typeRef, tm map[string]*fullType, visited map[string]bool) map[string]any {
	switch t.Kind {
	case "NON_NULL":
		if t.OfType != nil {
			return graphqlTypeToJSONSchema(*t.OfType, tm, visited)
		}
		return map[string]any{"type": "string"}

	case "LIST":
		var items map[string]any
		if t.OfType != nil {
			items = graphqlTypeToJSONSchema(*t.OfType, tm, visited)
		} else {
			items = map[string]any{}
		}
		return map[string]any{
			"type":  "array",
			"items": items,
		}

	case "SCALAR":
		return scalarToJSONSchema(t.Name)

	case "ENUM":
		ft, ok := tm[t.Name]
		if ok && len(ft.EnumValues) > 0 {
			values := make([]any, len(ft.EnumValues))
			for i, v := range ft.EnumValues {
				values[i] = v.Name
			}
			return map[string]any{"type": "string", "enum": values}
		}
		return map[string]any{"type": "string"}

	case "INPUT_OBJECT":
		return inputObjectToJSONSchema(t.Name, tm, visited)

	case "OBJECT":
		return objectToJSONSchema(t.Name, tm, visited)

	case "INTERFACE", "UNION":
		return unionToJSONSchema(t.Name, tm, visited)

	default:
		// For named types not yet resolved, look up in the type map.
		if t.Name != "" {
			if ft, ok := tm[t.Name]; ok {
				resolved := typeRef{Kind: ft.Kind, Name: ft.Name}
				return graphqlTypeToJSONSchema(resolved, tm, visited)
			}
		}
		return map[string]any{"type": "string"}
	}
}

func scalarToJSONSchema(name string) map[string]any {
	switch name {
	case "String", "ID":
		return map[string]any{"type": "string"}
	case "Int":
		return map[string]any{"type": "integer"}
	case "Float":
		return map[string]any{"type": "number"}
	case "Boolean":
		return map[string]any{"type": "boolean"}
	default:
		// Custom scalars default to string.
		return map[string]any{"type": "string"}
	}
}

func objectToJSONSchema(name string, tm map[string]*fullType, visited map[string]bool) map[string]any {
	if visited[name] {
		return map[string]any{"type": "object"}
	}
	visited[name] = true

	ft, ok := tm[name]
	if !ok || len(ft.Fields) == 0 {
		return map[string]any{"type": "object"}
	}

	properties := map[string]any{}
	for _, f := range ft.Fields {
		if strings.HasPrefix(f.Name, "__") {
			continue
		}
		properties[f.Name] = graphqlTypeToJSONSchema(f.Type, tm, visited)
	}

	schema := map[string]any{
		"type": "object",
	}
	if len(properties) > 0 {
		schema["properties"] = properties
	}
	return schema
}

func inputObjectToJSONSchema(name string, tm map[string]*fullType, visited map[string]bool) map[string]any {
	if visited[name] {
		return map[string]any{"type": "object"}
	}
	visited[name] = true

	ft, ok := tm[name]
	if !ok || len(ft.InputFields) == 0 {
		return map[string]any{"type": "object"}
	}

	properties := map[string]any{}
	var required []string

	for _, f := range ft.InputFields {
		isRequired := f.Type.Kind == "NON_NULL"
		argType := f.Type
		if isRequired && argType.OfType != nil {
			argType = *argType.OfType
		}

		properties[f.Name] = graphqlTypeToJSONSchema(argType, tm, visited)
		if isRequired {
			required = append(required, f.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = toAnySlice(required)
	}
	return schema
}

func unionToJSONSchema(name string, tm map[string]*fullType, visited map[string]bool) map[string]any {
	ft, ok := tm[name]
	if !ok || len(ft.PossibleTypes) == 0 {
		return map[string]any{"type": "object"}
	}

	var oneOf []any
	for _, pt := range ft.PossibleTypes {
		resolved := typeRef{Kind: "OBJECT", Name: pt.Name}
		oneOf = append(oneOf, graphqlTypeToJSONSchema(resolved, tm, visited))
	}
	return map[string]any{"oneOf": oneOf}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
