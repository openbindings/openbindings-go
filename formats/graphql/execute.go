package graphql

import (
	"fmt"
	"strings"
)

const maxSelectionDepth = 3

// queryFieldName is the conventional input schema property name for a
// pre-built GraphQL query string. When the operation's input schema declares
// this property with a const value, the executor uses it instead of building
// a query from introspection.
const queryFieldName = "_query"

// parseRef parses a GraphQL ref in the form "Query/fieldName", "Mutation/fieldName",
// or "Subscription/fieldName".
func parseRef(ref string) (rootType string, fieldName string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty GraphQL ref")
	}
	idx := strings.Index(ref, "/")
	if idx < 0 || idx == 0 || idx == len(ref)-1 {
		return "", "", fmt.Errorf("GraphQL ref %q must be in the form Query/fieldName, Mutation/fieldName, or Subscription/fieldName", ref)
	}
	rootType = ref[:idx]
	fieldName = ref[idx+1:]
	switch rootType {
	case "Query", "Mutation", "Subscription":
		return rootType, fieldName, nil
	default:
		return "", "", fmt.Errorf("GraphQL ref %q has invalid root type %q (must be Query, Mutation, or Subscription)", ref, rootType)
	}
}

// buildQuery constructs a GraphQL query string with variable declarations and
// a selection set for the given field. It returns the full query string and a
// variables map built from the input.
//
// If the operation's input schema contains a _query property with a const value,
// that query is used directly. Otherwise, a query is constructed from the
// introspected schema with an auto-generated selection set.
func buildQuery(schema *introspectionSchema, rootType, fieldName string, input any, inputSchema map[string]any) (string, map[string]any, error) {
	// If the input schema provides a pre-built query, use it directly.
	if q, ok := queryFromSchema(inputSchema); ok {
		variables := inputToVariablesPassthrough(input)
		return q, variables, nil
	}

	// Fall back to introspection-based query construction.
	return buildQueryFromIntrospection(schema, rootType, fieldName, input)
}

// buildQueryFromIntrospection constructs a query by introspecting the schema
// and auto-generating a selection set (depth-limited, cycle-safe).
func buildQueryFromIntrospection(schema *introspectionSchema, rootType, fieldName string, input any) (string, map[string]any, error) {
	typeName := schema.rootTypeName(rootType)
	if typeName == "" {
		return "", nil, fmt.Errorf("schema has no %s type", rootType)
	}

	tm := schema.typeMap()
	rootTypeObj, ok := tm[typeName]
	if !ok {
		return "", nil, fmt.Errorf("type %q not found in schema", typeName)
	}

	var targetField *field
	for i := range rootTypeObj.Fields {
		if rootTypeObj.Fields[i].Name == fieldName {
			targetField = &rootTypeObj.Fields[i]
			break
		}
	}
	if targetField == nil {
		return "", nil, fmt.Errorf("field %q not found on %s type %q", fieldName, rootType, typeName)
	}

	// Build variable declarations and argument list from the field's args.
	varDecls, argList := buildVariables(targetField.Args)

	// Build selection set from the field's return type.
	returnTypeName := unwrapTypeName(targetField.Type)
	selectionSet := ""
	if returnTypeName != "" {
		if rt, ok := tm[returnTypeName]; ok && (rt.Kind == "OBJECT" || rt.Kind == "INTERFACE" || rt.Kind == "UNION") {
			selectionSet = buildSelectionSet(returnTypeName, tm, 0, make(map[string]bool))
		}
	}

	// Build the variables map from input.
	variables := inputToVariables(input, targetField.Args)

	// Determine operation keyword.
	var keyword string
	switch rootType {
	case "Query":
		keyword = "query"
	case "Mutation":
		keyword = "mutation"
	case "Subscription":
		keyword = "subscription"
	}

	// Assemble the query.
	var sb strings.Builder
	sb.WriteString(keyword)
	if varDecls != "" {
		sb.WriteString("(")
		sb.WriteString(varDecls)
		sb.WriteString(")")
	}
	sb.WriteString(" { ")
	sb.WriteString(fieldName)
	if argList != "" {
		sb.WriteString("(")
		sb.WriteString(argList)
		sb.WriteString(")")
	}
	if selectionSet != "" {
		sb.WriteString(" ")
		sb.WriteString(selectionSet)
	}
	sb.WriteString(" }")

	return sb.String(), variables, nil
}

// buildVariables produces the variable declaration string (e.g., "$id: ID!, $name: String")
// and argument passing string (e.g., "id: $id, name: $name") from a field's args.
func buildVariables(args []inputValue) (varDecls, argList string) {
	if len(args) == 0 {
		return "", ""
	}
	var decls, passing []string
	for _, arg := range args {
		varName := "$" + arg.Name
		graphqlType := typeRefToGraphQL(arg.Type)
		decls = append(decls, varName+": "+graphqlType)
		passing = append(passing, arg.Name+": "+varName)
	}
	return strings.Join(decls, ", "), strings.Join(passing, ", ")
}

// typeRefToGraphQL converts an introspection typeRef to a GraphQL type string
// (e.g., "String!", "[ID!]!", "MyInput").
func typeRefToGraphQL(t typeRef) string {
	switch t.Kind {
	case "NON_NULL":
		if t.OfType != nil {
			return typeRefToGraphQL(*t.OfType) + "!"
		}
		return t.Name + "!"
	case "LIST":
		if t.OfType != nil {
			return "[" + typeRefToGraphQL(*t.OfType) + "]"
		}
		return "[" + t.Name + "]"
	default:
		if t.Name != "" {
			return t.Name
		}
		return "String"
	}
}

// buildSelectionSet recursively builds a GraphQL selection set for the given type.
// It expands object fields up to maxSelectionDepth levels, with cycle detection.
func buildSelectionSet(typeName string, tm map[string]*fullType, depth int, visited map[string]bool) string {
	if depth >= maxSelectionDepth {
		return ""
	}
	if visited[typeName] {
		return ""
	}

	t, ok := tm[typeName]
	if !ok {
		return ""
	}

	switch t.Kind {
	case "UNION", "INTERFACE":
		return buildUnionSelectionSet(t, tm, depth, visited)
	case "OBJECT":
		return buildObjectSelectionSet(t, tm, depth, visited)
	default:
		return ""
	}
}

func buildObjectSelectionSet(t *fullType, tm map[string]*fullType, depth int, visited map[string]bool) string {
	if len(t.Fields) == 0 {
		return ""
	}

	visited[t.Name] = true
	defer func() { visited[t.Name] = false }()

	var fields []string
	for _, f := range t.Fields {
		// Skip built-in introspection fields.
		if strings.HasPrefix(f.Name, "__") {
			continue
		}

		returnType := unwrapTypeName(f.Type)
		if returnType == "" {
			fields = append(fields, f.Name)
			continue
		}

		ft, ok := tm[returnType]
		if !ok {
			fields = append(fields, f.Name)
			continue
		}

		switch ft.Kind {
		case "SCALAR", "ENUM":
			fields = append(fields, f.Name)
		case "OBJECT", "INTERFACE", "UNION":
			nested := buildSelectionSet(returnType, tm, depth+1, visited)
			if nested != "" {
				fields = append(fields, f.Name+" "+nested)
			}
		}
	}

	if len(fields) == 0 {
		return ""
	}
	return "{ " + strings.Join(fields, " ") + " }"
}

func buildUnionSelectionSet(t *fullType, tm map[string]*fullType, depth int, visited map[string]bool) string {
	if len(t.PossibleTypes) == 0 {
		return ""
	}

	// Always include __typename for union/interface types.
	var fragments []string
	fragments = append(fragments, "__typename")

	for _, pt := range t.PossibleTypes {
		nested := buildSelectionSet(pt.Name, tm, depth, visited)
		if nested != "" {
			fragments = append(fragments, "... on "+pt.Name+" "+nested)
		}
	}

	return "{ " + strings.Join(fragments, " ") + " }"
}

// unwrapTypeName unwraps NON_NULL and LIST wrappers to get the underlying named type.
func unwrapTypeName(t typeRef) string {
	for t.Kind == "NON_NULL" || t.Kind == "LIST" {
		if t.OfType == nil {
			break
		}
		t = *t.OfType
	}
	return t.Name
}

// queryFromSchema extracts a pre-built GraphQL query from the operation's input
// schema. Returns the query string and true if the schema has a _query property
// with a const value; returns ("", false) otherwise.
func queryFromSchema(schema map[string]any) (string, bool) {
	if schema == nil {
		return "", false
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return "", false
	}
	queryProp, ok := props[queryFieldName].(map[string]any)
	if !ok {
		return "", false
	}
	constVal, ok := queryProp["const"].(string)
	if !ok || constVal == "" {
		return "", false
	}
	return constVal, true
}

// inputToVariablesPassthrough converts the operation input to a GraphQL
// variables map, passing through all keys except the _query field.
func inputToVariablesPassthrough(input any) map[string]any {
	if input == nil {
		return nil
	}
	inputMap, ok := input.(map[string]any)
	if !ok {
		return nil
	}
	vars := make(map[string]any, len(inputMap))
	for k, v := range inputMap {
		if k == queryFieldName {
			continue
		}
		vars[k] = v
	}
	if len(vars) == 0 {
		return nil
	}
	return vars
}

// inputToVariables converts the operation input to a GraphQL variables map,
// using only keys that match declared field arguments.
func inputToVariables(input any, args []inputValue) map[string]any {
	if input == nil || len(args) == 0 {
		return nil
	}
	inputMap, ok := input.(map[string]any)
	if !ok {
		return nil
	}
	argNames := make(map[string]bool, len(args))
	for _, a := range args {
		argNames[a.Name] = true
	}
	vars := make(map[string]any)
	for k, v := range inputMap {
		if argNames[k] {
			vars[k] = v
		}
	}
	if len(vars) == 0 {
		return nil
	}
	return vars
}
