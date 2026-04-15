package openapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestListBindableRefs_BasicRefs(t *testing.T) {
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Test API", "version": "1.0.0"},
  "paths": {
    "/users": {
      "get": {
        "operationId": "listUsers",
        "summary": "List users",
        "responses": {"200": {"description": "OK"}}
      },
      "post": {
        "operationId": "createUser",
        "summary": "Create a user",
        "responses": {"200": {"description": "OK"}}
      }
    },
    "/items": {
      "get": {
        "operationId": "listItems",
        "description": "List all items",
        "responses": {"200": {"description": "OK"}}
      },
      "delete": {
        "operationId": "deleteItem",
        "summary": "Delete an item",
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 4 {
		t.Fatalf("expected 4 refs, got %d", len(result.Refs))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestListBindableRefs_JSONPointerFormat(t *testing.T) {
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Test", "version": "1.0.0"},
  "paths": {
    "/users": {
      "get": {
        "summary": "List users",
        "responses": {"200": {"description": "OK"}}
      }
    },
    "/users/{id}": {
      "put": {
        "summary": "Update user",
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"#/paths/~1users/get":        false,
		"#/paths/~1users~1{id}/put":  false,
	}

	for _, ref := range result.Refs {
		if _, ok := wantRefs[ref.Ref]; ok {
			wantRefs[ref.Ref] = true
		}
	}
	for ref, found := range wantRefs {
		if !found {
			t.Errorf("expected ref %q not found in results", ref)
		}
	}
}

func TestListBindableRefs_DescriptionFromSummary(t *testing.T) {
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Test", "version": "1.0.0"},
  "paths": {
    "/pets": {
      "get": {
        "summary": "List pets",
        "responses": {"200": {"description": "OK"}}
      },
      "post": {
        "description": "Create a new pet",
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	descByRef := map[string]string{}
	for _, ref := range result.Refs {
		descByRef[ref.Ref] = ref.Description
	}

	// Summary is used when description is absent.
	if descByRef["#/paths/~1pets/get"] != "List pets" {
		t.Errorf("get description = %q, want %q", descByRef["#/paths/~1pets/get"], "List pets")
	}
	// Description takes precedence over summary.
	if descByRef["#/paths/~1pets/post"] != "Create a new pet" {
		t.Errorf("post description = %q, want %q", descByRef["#/paths/~1pets/post"], "Create a new pet")
	}
}

func TestListBindableRefs_RefsMatchCreateInterface(t *testing.T) {
	doc := minimalDoc()
	iface := convertDocToInterface(doc, "")

	// Collect binding refs from CreateInterface.
	createRefs := map[string]bool{}
	for _, b := range iface.Bindings {
		createRefs[b.Ref] = true
	}

	// ListBindableRefs should produce the same refs.
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Test API", "version": "2.0.0"},
  "paths": {
    "/users": {
      "get": {
        "operationId": "listUsers",
        "summary": "List users",
        "responses": {"200": {"description": "OK"}}
      },
      "post": {
        "operationId": "createUser",
        "summary": "Create a user",
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Refs {
		if !createRefs[ref.Ref] {
			t.Errorf("ListBindableRefs ref %q not found in CreateInterface bindings", ref.Ref)
		}
	}
	if len(result.Refs) != len(createRefs) {
		t.Errorf("ref count mismatch: ListBindableRefs=%d, CreateInterface=%d", len(result.Refs), len(createRefs))
	}
}

func TestListBindableRefs_NoPaths(t *testing.T) {
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Empty", "version": "1.0.0"}
}`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(result.Refs))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestListBindableRefs_NilContent(t *testing.T) {
	creator := NewCreator()
	_, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}
