package graphql

import (
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		name      string
		ref       string
		wantRoot  string
		wantField string
		wantErr   bool
	}{
		{"query", "Query/users", "Query", "users", false},
		{"mutation", "Mutation/createUser", "Mutation", "createUser", false},
		{"subscription", "Subscription/onOrderUpdated", "Subscription", "onOrderUpdated", false},
		{"empty", "", "", "", true},
		{"no slash", "QueryUsers", "", "", true},
		{"invalid root", "Invalid/users", "", "", true},
		{"trailing slash", "Query/", "", "", true},
		{"leading slash", "/users", "", "", true},
		{"lowercase root", "query/users", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, field, err := parseRef(tt.ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseRef(%q) error = %v, wantErr %v", tt.ref, err, tt.wantErr)
			}
			if root != tt.wantRoot {
				t.Errorf("parseRef(%q) rootType = %q, want %q", tt.ref, root, tt.wantRoot)
			}
			if field != tt.wantField {
				t.Errorf("parseRef(%q) fieldName = %q, want %q", tt.ref, field, tt.wantField)
			}
		})
	}
}

func TestTypeRefToGraphQL(t *testing.T) {
	tests := []struct {
		name string
		ref  typeRef
		want string
	}{
		{"scalar", typeRef{Kind: "SCALAR", Name: "String"}, "String"},
		{"non-null scalar", typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "ID"}}, "ID!"},
		{"list", typeRef{Kind: "LIST", OfType: &typeRef{Kind: "SCALAR", Name: "String"}}, "[String]"},
		{"non-null list of non-null", typeRef{
			Kind: "NON_NULL",
			OfType: &typeRef{
				Kind: "LIST",
				OfType: &typeRef{
					Kind:   "NON_NULL",
					OfType: &typeRef{Kind: "SCALAR", Name: "Int"},
				},
			},
		}, "[Int!]!"},
		{"named type", typeRef{Kind: "OBJECT", Name: "User"}, "User"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := typeRefToGraphQL(tt.ref)
			if got != tt.want {
				t.Errorf("typeRefToGraphQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildSelectionSet(t *testing.T) {
	tm := map[string]*fullType{
		"User": {
			Kind: "OBJECT",
			Name: "User",
			Fields: []field{
				{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
				{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "email", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	}

	got := buildSelectionSet("User", tm, 0, make(map[string]bool))
	if got != "{ id name email }" {
		t.Errorf("buildSelectionSet(User) = %q, want %q", got, "{ id name email }")
	}
}

func TestBuildSelectionSetNested(t *testing.T) {
	tm := map[string]*fullType{
		"User": {
			Kind: "OBJECT",
			Name: "User",
			Fields: []field{
				{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
				{Name: "posts", Type: typeRef{Kind: "LIST", OfType: &typeRef{Kind: "OBJECT", Name: "Post"}}},
			},
		},
		"Post": {
			Kind: "OBJECT",
			Name: "Post",
			Fields: []field{
				{Name: "title", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "body", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	}

	got := buildSelectionSet("User", tm, 0, make(map[string]bool))
	if got != "{ id posts { title body } }" {
		t.Errorf("buildSelectionSet(User nested) = %q, want %q", got, "{ id posts { title body } }")
	}
}

func TestBuildSelectionSetCycle(t *testing.T) {
	tm := map[string]*fullType{
		"User": {
			Kind: "OBJECT",
			Name: "User",
			Fields: []field{
				{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
				{Name: "friends", Type: typeRef{Kind: "LIST", OfType: &typeRef{Kind: "OBJECT", Name: "User"}}},
			},
		},
	}

	got := buildSelectionSet("User", tm, 0, make(map[string]bool))
	// Should include id but not recurse infinitely into friends.
	if got != "{ id }" {
		t.Errorf("buildSelectionSet(cycle) = %q, want %q", got, "{ id }")
	}
}

func TestBuildSelectionSetDepthLimit(t *testing.T) {
	tm := map[string]*fullType{
		"A": {
			Kind: "OBJECT",
			Name: "A",
			Fields: []field{
				{Name: "a_val", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "b", Type: typeRef{Kind: "OBJECT", Name: "B"}},
			},
		},
		"B": {
			Kind: "OBJECT",
			Name: "B",
			Fields: []field{
				{Name: "b_val", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "c", Type: typeRef{Kind: "OBJECT", Name: "C"}},
			},
		},
		"C": {
			Kind: "OBJECT",
			Name: "C",
			Fields: []field{
				{Name: "c_val", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "d", Type: typeRef{Kind: "OBJECT", Name: "D"}},
			},
		},
		"D": {
			Kind: "OBJECT",
			Name: "D",
			Fields: []field{
				{Name: "value", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	}

	got := buildSelectionSet("A", tm, 0, make(map[string]bool))
	// Depth 0: A, depth 1: B, depth 2: C (includes c_val but D is at depth 3, hits limit).
	want := "{ a_val b { b_val c { c_val } } }"
	if got != want {
		t.Errorf("buildSelectionSet(depth limit) = %q, want %q", got, want)
	}
}

func TestBuildSelectionSetUnion(t *testing.T) {
	tm := map[string]*fullType{
		"SearchResult": {
			Kind: "UNION",
			Name: "SearchResult",
			PossibleTypes: []typeRef{
				{Name: "User"},
				{Name: "Post"},
			},
		},
		"User": {
			Kind: "OBJECT",
			Name: "User",
			Fields: []field{
				{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
		"Post": {
			Kind: "OBJECT",
			Name: "Post",
			Fields: []field{
				{Name: "title", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	}

	got := buildSelectionSet("SearchResult", tm, 0, make(map[string]bool))
	want := "{ __typename ... on User { name } ... on Post { title } }"
	if got != want {
		t.Errorf("buildSelectionSet(union) = %q, want %q", got, want)
	}
}

func TestBuildQuery(t *testing.T) {
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

	query, vars, err := buildQuery(schema, "Query", "user", map[string]any{"id": "123"}, nil)
	if err != nil {
		t.Fatalf("buildQuery() error = %v", err)
	}
	if query != "query($id: ID!) { user(id: $id) { id name } }" {
		t.Errorf("buildQuery() query = %q", query)
	}
	if vars["id"] != "123" {
		t.Errorf("buildQuery() vars = %v", vars)
	}
}

func TestBuildQueryWithSchemaQuery(t *testing.T) {
	// When the input schema has a _query const, buildQuery should use it
	// instead of introspecting.
	schema := &introspectionSchema{
		QueryType: &typeRef{Name: "Query"},
		Types: []fullType{
			{Kind: "OBJECT", Name: "Query", Fields: []field{
				{Name: "user", Type: typeRef{Kind: "OBJECT", Name: "User"}},
			}},
			{Kind: "OBJECT", Name: "User", Fields: []field{
				{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
				{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "email", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			}},
		},
	}

	customQuery := "query($id: ID!) { user(id: $id) { id name } }"
	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string"},
			"_query": map[string]any{"type": "string", "const": customQuery},
		},
	}

	query, vars, err := buildQuery(schema, "Query", "user", map[string]any{"id": "123"}, inputSchema)
	if err != nil {
		t.Fatalf("buildQuery() error = %v", err)
	}
	if query != customQuery {
		t.Errorf("buildQuery() should use schema query, got %q", query)
	}
	if vars["id"] != "123" {
		t.Errorf("buildQuery() vars = %v", vars)
	}
}

func TestBuildQueryWithSchemaQueryExcludesQueryField(t *testing.T) {
	schema := &introspectionSchema{
		QueryType: &typeRef{Name: "Query"},
		Types: []fullType{
			{Kind: "OBJECT", Name: "Query", Fields: []field{
				{Name: "user", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			}},
		},
	}

	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"_query": map[string]any{"type": "string", "const": "query { user }"},
		},
	}

	// Even if _query somehow ends up in the input, it should not be passed as a variable.
	_, vars, err := buildQuery(schema, "Query", "user", map[string]any{"_query": "ignored"}, inputSchema)
	if err != nil {
		t.Fatalf("buildQuery() error = %v", err)
	}
	if _, ok := vars["_query"]; ok {
		t.Error("_query should not appear in variables")
	}
}

func TestQueryFromSchema(t *testing.T) {
	tests := []struct {
		name    string
		schema  map[string]any
		want    string
		wantOK  bool
	}{
		{"nil schema", nil, "", false},
		{"no properties", map[string]any{"type": "object"}, "", false},
		{"no _query", map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string"}},
		}, "", false},
		{"_query without const", map[string]any{
			"type":       "object",
			"properties": map[string]any{"_query": map[string]any{"type": "string"}},
		}, "", false},
		{"_query with const", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"_query": map[string]any{"type": "string", "const": "query { users { id } }"},
			},
		}, "query { users { id } }", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := queryFromSchema(tt.schema)
			if ok != tt.wantOK {
				t.Errorf("queryFromSchema() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("queryFromSchema() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInputToVariables(t *testing.T) {
	args := []inputValue{
		{Name: "name"},
		{Name: "age"},
	}
	input := map[string]any{"name": "Alice", "age": 30, "extra": "ignored"}
	vars := inputToVariables(input, args)
	if vars["name"] != "Alice" || vars["age"] != 30 {
		t.Errorf("inputToVariables() = %v", vars)
	}
	if _, ok := vars["extra"]; ok {
		t.Error("inputToVariables() should not include extra keys")
	}
}
