package openapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func minimalDoc() *openapi3.T {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info: &openapi3.Info{
			Title:       "Test API",
			Version:     "2.0.0",
			Description: "A test API",
		},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/users", &openapi3.PathItem{
				Get: &openapi3.Operation{
					OperationID: "listUsers",
					Summary:     "List users",
					Responses:   openapi3.NewResponses(),
				},
				Post: &openapi3.Operation{
					OperationID: "createUser",
					Summary:     "Create a user",
					Responses:   openapi3.NewResponses(),
				},
			}),
		),
	}
	return doc
}

func TestConvertDocToInterface_CopiesMetadata(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "https://example.com/openapi.json")

	if iface.Name != "Test API" {
		t.Errorf("Name = %q, want %q", iface.Name, "Test API")
	}
	if iface.Version != "2.0.0" {
		t.Errorf("Version = %q, want %q", iface.Version, "2.0.0")
	}
	if iface.Description != "A test API" {
		t.Errorf("Description = %q, want %q", iface.Description, "A test API")
	}
}

func TestConvertDocToInterface_CreatesOperations(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "")

	if len(iface.Operations) != 2 {
		t.Fatalf("len(Operations) = %d, want 2", len(iface.Operations))
	}
	if _, ok := iface.Operations["listUsers"]; !ok {
		t.Error("missing operation 'listUsers'")
	}
	if _, ok := iface.Operations["createUser"]; !ok {
		t.Error("missing operation 'createUser'")
	}
}

func TestConvertDocToInterface_CreatesBindingsWithRefs(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "")

	if len(iface.Bindings) != 2 {
		t.Fatalf("len(Bindings) = %d, want 2", len(iface.Bindings))
	}

	// Check that bindings have JSON pointer refs
	for key, binding := range iface.Bindings {
		if binding.Ref == "" {
			t.Errorf("binding %q has empty ref", key)
		}
		if binding.Source != DefaultSourceName {
			t.Errorf("binding %q source = %q, want %q", key, binding.Source, DefaultSourceName)
		}
		// The ref should be parseable
		_, _, err := parseRef(binding.Ref)
		if err != nil {
			t.Errorf("binding %q ref %q is not parseable: %v", key, binding.Ref, err)
		}
	}
}

func TestConvertDocToInterface_CreatesSourceEntry(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "https://example.com/openapi.json")

	src, ok := iface.Sources[DefaultSourceName]
	if !ok {
		t.Fatal("missing source entry for DefaultSourceName")
	}
	if src.Location != "https://example.com/openapi.json" {
		t.Errorf("source Location = %q, want %q", src.Location, "https://example.com/openapi.json")
	}
	if src.Format == "" {
		t.Error("source Format is empty")
	}
	// Format should contain "openapi@" prefix
	if src.Format[:8] != "openapi@" {
		t.Errorf("source Format = %q, want prefix %q", src.Format, "openapi@")
	}
}

func TestConvertDocToInterface_NoPaths(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "Empty", Version: "1.0.0"},
	}
	iface := convertDocToInterface(doc, "")

	if len(iface.Operations) != 0 {
		t.Errorf("len(Operations) = %d, want 0", len(iface.Operations))
	}
	if len(iface.Bindings) != 0 {
		t.Errorf("len(Bindings) = %d, want 0", len(iface.Bindings))
	}
}

func TestConvertDocToInterface_DerivesKeyFromOperationId(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "Test", Version: "1.0.0"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/pets", &openapi3.PathItem{
				Get: &openapi3.Operation{
					OperationID: "findPets",
					Responses:   openapi3.NewResponses(),
				},
			}),
		),
	}
	iface := convertDocToInterface(doc, "")

	if _, ok := iface.Operations["findPets"]; !ok {
		t.Errorf("expected operation key 'findPets', got keys: %v", keys(iface.Operations))
	}
}

func TestConvertDocToInterface_DerivesKeyFromPathWhenNoOperationId(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "Test", Version: "1.0.0"},
		Paths: openapi3.NewPaths(
			openapi3.WithPath("/pets", &openapi3.PathItem{
				Get: &openapi3.Operation{
					Summary:   "List pets",
					Responses: openapi3.NewResponses(),
				},
			}),
		),
	}
	iface := convertDocToInterface(doc, "")

	// Should derive key from path + method
	if _, ok := iface.Operations["pets.get"]; !ok {
		t.Errorf("expected operation key 'pets.get', got keys: %v", keys(iface.Operations))
	}
}

func keys[V any](m map[string]V) []string {
	result := make([]string, 0, len(m))
	for k := range m {
		result = append(result, k)
	}
	return result
}

func TestConvertDocToInterface_TranslatesNullableIn30(t *testing.T) {
	yaml := []byte(`openapi: 3.0.3
info: { title: P, version: "1.0.0" }
paths:
  /ability:
    get:
      operationId: abilityList
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  count: { type: integer }
                  next: { type: string, nullable: true, format: uri }
                  previous: { type: string, nullable: true, format: uri }
                required: [count]
`)
	doc, err := loadDocument("", yaml)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	iface := convertDocToInterface(doc, "")

	op, ok := iface.Operations["abilityList"]
	if !ok {
		t.Fatalf("abilityList operation missing")
	}

	props, ok := op.Output["properties"].(map[string]any)
	if !ok {
		t.Fatalf("output.properties missing or wrong type: %#v", op.Output)
	}

	next, ok := props["next"].(map[string]any)
	if !ok {
		t.Fatalf("next property missing")
	}
	gotType, ok := next["type"].([]any)
	if !ok {
		t.Fatalf("next.type expected []any, got %#v", next["type"])
	}
	if len(gotType) != 2 || gotType[0] != "string" || gotType[1] != "null" {
		t.Errorf("next.type = %#v, want [\"string\", \"null\"]", gotType)
	}
	if _, hasNullable := next["nullable"]; hasNullable {
		t.Errorf("next.nullable should have been removed, got %#v", next)
	}

	if iface.Sources["openapi"].Format != "openapi@3.0" {
		t.Errorf("source.format = %q, want \"openapi@3.0\"", iface.Sources["openapi"].Format)
	}
}

func TestConvertDocToInterface_Preserves31Verbatim(t *testing.T) {
	yaml := []byte(`openapi: 3.1.0
info: { title: T, version: "1.0.0" }
paths:
  /x:
    get:
      operationId: x
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                properties:
                  next: { type: [string, "null"], format: uri }
                  legacy: { type: string, nullable: true }
`)
	doc, err := loadDocument("", yaml)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	iface := convertDocToInterface(doc, "")

	op := iface.Operations["x"]
	props := op.Output["properties"].(map[string]any)

	legacy := props["legacy"].(map[string]any)
	// In 3.1, nullable: true is an inert annotation; we pass it through.
	if legacy["nullable"] != true {
		t.Errorf("legacy.nullable = %#v, want true (3.1 inert annotation should pass through)", legacy["nullable"])
	}
	if legacy["type"] != "string" {
		t.Errorf("legacy.type = %#v, want \"string\"", legacy["type"])
	}
}

func TestConvertDocToInterface_TranslatesExclusiveMinIn30(t *testing.T) {
	yaml := []byte(`openapi: 3.0.3
info: { title: T, version: "1.0.0" }
paths:
  /q:
    get:
      operationId: q
      parameters:
        - name: page
          in: query
          schema:
            type: integer
            minimum: 0
            exclusiveMinimum: true
            maximum: 100
            exclusiveMaximum: false
      responses:
        '200': { description: OK }
`)
	doc, err := loadDocument("", yaml)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	iface := convertDocToInterface(doc, "")

	op := iface.Operations["q"]
	props := op.Input["properties"].(map[string]any)
	page := props["page"].(map[string]any)

	if _, hasMin := page["minimum"]; hasMin {
		t.Errorf("page.minimum should have been removed, got %#v", page)
	}
	em, ok := page["exclusiveMinimum"].(float64)
	if !ok || em != 0 {
		t.Errorf("page.exclusiveMinimum = %#v, want 0 (numeric)", page["exclusiveMinimum"])
	}
	if max, ok := page["maximum"].(float64); !ok || max != 100 {
		t.Errorf("page.maximum = %#v, want 100", page["maximum"])
	}
	if _, hasExMax := page["exclusiveMaximum"]; hasExMax {
		t.Errorf("page.exclusiveMaximum (false) should have been removed, got %#v", page)
	}
}
