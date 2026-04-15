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
