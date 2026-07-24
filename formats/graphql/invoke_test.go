package graphql

import (
	"encoding/json"
	"testing"
)

func minimalSchema() *introspectionSchema {
	return &introspectionSchema{
		QueryType:    &typeRef{Kind: "OBJECT", Name: "RootQuery"},
		MutationType: &typeRef{Kind: "OBJECT", Name: "RootMutation"},
		Types: []fullType{
			{
				Kind: "OBJECT", Name: "RootQuery",
				Fields: []field{
					{Name: "viewer", Args: []inputValue{}, Type: typeRef{Kind: "SCALAR", Name: "String"}},
					{Name: "health", Args: []inputValue{}, Type: typeRef{Kind: "SCALAR", Name: "String"}},
				},
			},
			{
				Kind: "OBJECT", Name: "RootMutation",
				Fields: []field{{Name: "save", Args: []inputValue{}, Type: typeRef{Kind: "SCALAR", Name: "String"}}},
			},
			{Kind: "SCALAR", Name: "String"},
		},
	}
}

func TestParseRefCanonical(t *testing.T) {
	for _, tc := range []struct {
		ref, kind, field string
	}{
		{"query/viewer", "query", "viewer"},
		{"mutation/save", "mutation", "save"},
		{"subscription/updates", "subscription", "updates"},
	} {
		kind, field, err := parseRef(tc.ref)
		if err != nil || kind != tc.kind || field != tc.field {
			t.Fatalf("parseRef(%q) = %q, %q, %v", tc.ref, kind, field, err)
		}
	}
	for _, ref := range []string{"", "Query/viewer", "query/", "query/viewer/nested", "query/not-valid"} {
		if _, _, err := parseRef(ref); err == nil {
			t.Errorf("parseRef(%q) unexpectedly succeeded", ref)
		}
	}
}

func TestExecutableDocumentCorrespondence(t *testing.T) {
	doc, err := parseExecutableDocument(`
		query Other { health }
		query Viewer($skip: Boolean!) { ...RootFields }
		fragment RootFields on RootQuery {
			result: viewer @skip(if: $skip)
			health @include(if: $skip)
		}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.verifySelection("Viewer", "query", "viewer", map[string]any{"skip": false}, minimalSchema()); err != nil {
		t.Fatalf("valid selection refused: %v", err)
	}

	cases := []struct {
		source, name, kind, field string
	}{
		{"mutation { save }", "", "query", "save"},
		{"{ viewer health }", "", "query", "viewer"},
		{"{ health }", "", "query", "viewer"},
		{"query A { viewer } query B { health }", "", "query", "viewer"},
	}
	for _, tc := range cases {
		parsed, err := parseExecutableDocument(tc.source)
		if err != nil {
			t.Fatal(err)
		}
		if err := parsed.verifySelection(tc.name, tc.kind, tc.field, nil, minimalSchema()); err == nil {
			t.Errorf("selection %q unexpectedly matched", tc.source)
		}
	}
}

func TestExecutableDocumentRejectsMalformedGraphQLSyntax(t *testing.T) {
	for _, source := range []string{
		`query Viewer() { viewer }`,
		`query Viewer($id) { viewer(id: $id) }`,
		`query Viewer($id: ID = $other) { viewer }`,
		`query { viewer() }`,
		`query { viewer(id) }`,
		`query { viewer(filter: {id}) }`,
		`query { viewer(limit: 01) }`,
		"query { viewer(arg: \"bad\\q\") }",
		"query { \u00e9xample }",
	} {
		if _, err := parseExecutableDocument(source); err == nil {
			t.Errorf("malformed document unexpectedly parsed: %q", source)
		}
	}
}

func TestExecutableDocumentAcceptsGraphQLStringForms(t *testing.T) {
	for _, source := range []string{
		`query { viewer(arg: "\u{1F4A9}") }`,
		`query { viewer(arg: """embedded \""" triple quote""") }`,
	} {
		doc, err := parseExecutableDocument(source)
		if err != nil {
			t.Fatalf("valid document refused: %v", err)
		}
		if err := doc.verifySelection("", "query", "viewer", nil, minimalSchema()); err != nil {
			t.Fatalf("valid selection refused: %v", err)
		}
	}
}

func TestStrictIntrospectionContent(t *testing.T) {
	schema := minimalSchema()
	success, _ := json.Marshal(map[string]any{"data": map[string]any{"__schema": schema}})
	if got, err := parseIntrospectionContent(success); err != nil || got.QueryType.Name != "RootQuery" {
		t.Fatalf("successful execution result refused: %v", err)
	}
	bare, _ := json.Marshal(schema)
	wrapper, _ := json.Marshal(map[string]any{"__schema": schema})
	withErrors, _ := json.Marshal(map[string]any{"data": map[string]any{"__schema": schema}, "errors": []any{}})
	stringified, _ := json.Marshal(string(success))
	for _, raw := range []json.RawMessage{bare, wrapper, withErrors, stringified, json.RawMessage("null")} {
		if _, err := parseIntrospectionContent(raw); err == nil {
			t.Errorf("noncanonical content unexpectedly accepted: %s", raw)
		}
	}
}

func TestWellFormedGraphQLResponse(t *testing.T) {
	valid := []map[string]any{
		{"data": map[string]any{"viewer": "Ada"}},
		{"data": nil, "errors": []any{map[string]any{"message": "failed"}}},
		{"errors": []any{map[string]any{"message": "request rejected"}}},
	}
	for _, value := range valid {
		if !wellFormedGraphQLResponse(value) {
			t.Errorf("valid response refused: %#v", value)
		}
	}
	invalid := []map[string]any{
		{},
		{"data": "not an object"},
		{"errors": []any{}},
		{"errors": []any{map[string]any{"code": "NO_MESSAGE"}}},
	}
	for _, value := range invalid {
		if wellFormedGraphQLResponse(value) {
			t.Errorf("invalid response accepted: %#v", value)
		}
	}
}
