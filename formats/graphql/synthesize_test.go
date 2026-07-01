package graphql

import (
	"strings"
	"testing"
)

func TestScalarToJSONSchema(t *testing.T) {
	tests := []struct {
		name     string
		scalar   string
		wantType string
	}{
		{"String", "String", "string"},
		{"ID", "ID", "string"},
		{"Int", "Int", "integer"},
		{"Float", "Float", "number"},
		{"Boolean", "Boolean", "boolean"},
		{"custom", "DateTime", "string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scalarToJSONSchema(tt.scalar)
			if got["type"] != tt.wantType {
				t.Errorf("scalarToJSONSchema(%q) type = %v, want %v", tt.scalar, got["type"], tt.wantType)
			}
		})
	}
}

func TestGraphqlTypeToJSONSchemaEnum(t *testing.T) {
	tm := map[string]*fullType{
		"Status": {
			Kind: "ENUM",
			Name: "Status",
			EnumValues: []enumValue{
				{Name: "ACTIVE"},
				{Name: "INACTIVE"},
			},
		},
	}
	got := graphqlTypeToJSONSchema(typeRef{Kind: "ENUM", Name: "Status"}, tm, make(map[string]bool))
	if got["type"] != "string" {
		t.Errorf("enum type = %v, want string", got["type"])
	}
	vals, ok := got["enum"].([]any)
	if !ok || len(vals) != 2 {
		t.Errorf("enum values = %v", got["enum"])
	}
}

func TestGraphqlTypeToJSONSchemaList(t *testing.T) {
	tm := map[string]*fullType{}
	got := graphqlTypeToJSONSchema(typeRef{
		Kind:   "LIST",
		OfType: &typeRef{Kind: "SCALAR", Name: "String"},
	}, tm, make(map[string]bool))
	if got["type"] != "array" {
		t.Errorf("list type = %v, want array", got["type"])
	}
	items, ok := got["items"].(map[string]any)
	if !ok || items["type"] != "string" {
		t.Errorf("list items = %v", got["items"])
	}
}

func TestGraphqlTypeToJSONSchemaObjectCycle(t *testing.T) {
	tm := map[string]*fullType{
		"Node": {
			Kind: "OBJECT",
			Name: "Node",
			Fields: []field{
				{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
				{Name: "parent", Type: typeRef{Kind: "OBJECT", Name: "Node"}},
			},
		},
	}
	got := graphqlTypeToJSONSchema(typeRef{Kind: "OBJECT", Name: "Node"}, tm, make(map[string]bool))
	if got["type"] != "object" {
		t.Errorf("cycle type = %v, want object", got["type"])
	}
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	parent, ok := props["parent"].(map[string]any)
	if !ok {
		t.Fatal("expected parent property")
	}
	// Cyclic reference should be a bare object with no properties.
	if parent["type"] != "object" {
		t.Errorf("cyclic parent type = %v, want object", parent["type"])
	}
	if _, hasProps := parent["properties"]; hasProps {
		t.Error("cyclic parent should not have properties")
	}
}

func TestGraphqlTypeToJSONSchemaUnion(t *testing.T) {
	tm := map[string]*fullType{
		"Result": {
			Kind: "UNION",
			Name: "Result",
			PossibleTypes: []typeRef{
				{Name: "TypeA"},
				{Name: "TypeB"},
			},
		},
		"TypeA": {
			Kind: "OBJECT",
			Name: "TypeA",
			Fields: []field{
				{Name: "a", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
		"TypeB": {
			Kind: "OBJECT",
			Name: "TypeB",
			Fields: []field{
				{Name: "b", Type: typeRef{Kind: "SCALAR", Name: "Int"}},
			},
		},
	}
	got := graphqlTypeToJSONSchema(typeRef{Kind: "UNION", Name: "Result"}, tm, make(map[string]bool))
	oneOf, ok := got["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		t.Errorf("union oneOf = %v", got["oneOf"])
	}
}

func TestGraphqlTypeToJSONSchemaInputObject(t *testing.T) {
	tm := map[string]*fullType{
		"CreateUserInput": {
			Kind: "INPUT_OBJECT",
			Name: "CreateUserInput",
			InputFields: []inputValue{
				{Name: "name", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "String"}}},
				{Name: "email", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	}
	got := graphqlTypeToJSONSchema(typeRef{Kind: "INPUT_OBJECT", Name: "CreateUserInput"}, tm, make(map[string]bool))
	if got["type"] != "object" {
		t.Errorf("input object type = %v, want object", got["type"])
	}
	props := got["properties"].(map[string]any)
	if len(props) != 2 {
		t.Errorf("expected 2 properties, got %d", len(props))
	}
	req := got["required"].([]any)
	if len(req) != 1 || req[0] != "name" {
		t.Errorf("required = %v, want [name]", req)
	}
}

func TestConvertToInterface(t *testing.T) {
	schema := &introspectionSchema{
		QueryType:    &typeRef{Name: "Query"},
		MutationType: &typeRef{Name: "Mutation"},
		Types: []fullType{
			{
				Kind: "OBJECT",
				Name: "Query",
				Fields: []field{
					{
						Name:        "users",
						Description: "List all users",
						Type:        typeRef{Kind: "LIST", OfType: &typeRef{Kind: "OBJECT", Name: "User"}},
					},
				},
			},
			{
				Kind: "OBJECT",
				Name: "Mutation",
				Fields: []field{
					{
						Name:         "deleteUser",
						Description:  "Delete a user",
						IsDeprecated: true,
						Args: []inputValue{
							{Name: "id", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "ID"}}},
						},
						Type: typeRef{Kind: "SCALAR", Name: "Boolean"},
					},
				},
			},
			{
				Kind: "OBJECT",
				Name: "User",
				Fields: []field{
					{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
					{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				},
			},
		},
	}

	iface, err := convertToInterface(schema, "https://api.example.com/graphql")
	if err != nil {
		t.Fatalf("convertToInterface() error = %v", err)
	}

	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}

	usersOp, ok := iface.Operations["users"]
	if !ok {
		t.Fatal("missing users operation")
	}
	if usersOp.Description != "List all users" {
		t.Errorf("users description = %q", usersOp.Description)
	}

	deleteOp, ok := iface.Operations["deleteUser"]
	if !ok {
		t.Fatal("missing deleteUser operation")
	}
	if !deleteOp.Deprecated {
		t.Error("deleteUser should be deprecated")
	}
	if deleteOp.Input == nil {
		t.Error("deleteUser should have input schema")
	}

	if len(iface.Bindings) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(iface.Bindings))
	}

	usersBinding := iface.Bindings["users.graphql"]
	if usersBinding.Ref != "Query/users" {
		t.Errorf("users binding ref = %q, want %q", usersBinding.Ref, "Query/users")
	}

	deleteBinding := iface.Bindings["deleteUser.graphql"]
	if deleteBinding.Ref != "Mutation/deleteUser" {
		t.Errorf("deleteUser binding ref = %q, want %q", deleteBinding.Ref, "Mutation/deleteUser")
	}

	src := iface.Sources[DefaultSourceName]
	if src.Format != FormatToken {
		t.Errorf("source format = %q, want %q", src.Format, FormatToken)
	}
	if src.Location != "https://api.example.com/graphql" {
		t.Errorf("source location = %q", src.Location)
	}
}

func TestConvertToInterfaceGeneratesQueryConst(t *testing.T) {
	schema := &introspectionSchema{
		QueryType: &typeRef{Name: "Query"},
		Types: []fullType{
			{
				Kind: "OBJECT",
				Name: "Query",
				Fields: []field{
					{
						Name: "user",
						Args: []inputValue{
							{Name: "id", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "ID"}}},
						},
						Type: typeRef{Kind: "OBJECT", Name: "User"},
					},
				},
			},
			{
				Kind: "OBJECT",
				Name: "User",
				Fields: []field{
					{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
					{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				},
			},
		},
	}

	iface, err := convertToInterface(schema, "https://api.example.com/graphql")
	if err != nil {
		t.Fatalf("convertToInterface() error = %v", err)
	}

	op := iface.Operations["user"]
	if op.Input == nil {
		t.Fatal("expected input schema")
	}
	props, ok := op.Input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in input schema")
	}
	queryProp, ok := props["_query"].(map[string]any)
	if !ok {
		t.Fatal("expected _query property in input schema")
	}
	constVal, ok := queryProp["const"].(string)
	if !ok || constVal == "" {
		t.Fatal("expected _query const value")
	}
	// The const should be a valid query with variable declarations and selection set.
	if !strings.Contains(constVal, "$id: ID!") {
		t.Errorf("_query const should contain variable declaration, got %q", constVal)
	}
	if !strings.Contains(constVal, "{ id name }") {
		t.Errorf("_query const should contain selection set, got %q", constVal)
	}
}

func TestArgsToInputSchema(t *testing.T) {
	tm := map[string]*fullType{}
	args := []inputValue{
		{Name: "id", Description: "User ID", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "ID"}}},
		{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
	}

	schema := argsToInputSchema(args, tm)
	if schema["type"] != "object" {
		t.Errorf("type = %v, want object", schema["type"])
	}

	props := schema["properties"].(map[string]any)
	if len(props) != 2 {
		t.Errorf("expected 2 properties, got %d", len(props))
	}

	idProp := props["id"].(map[string]any)
	if idProp["description"] != "User ID" {
		t.Errorf("id description = %v", idProp["description"])
	}

	req := schema["required"].([]any)
	if len(req) != 1 || req[0] != "id" {
		t.Errorf("required = %v, want [id]", req)
	}
}
