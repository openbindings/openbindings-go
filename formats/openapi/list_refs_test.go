package openapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSource_BasicRefs(t *testing.T) {
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 4 {
		t.Fatalf("expected 4 refs, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_JSONPointerFormat(t *testing.T) {
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"#/paths/~1users/get":       false,
		"#/paths/~1users~1{id}/put": false,
	}

	for _, ref := range result.Targets {
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

func TestInspectSource_DescriptionFromSummary(t *testing.T) {
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	descByRef := map[string]string{}
	for _, ref := range result.Targets {
		if ref.Operation != nil {
			descByRef[ref.Ref] = ref.Operation.Description
		}
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

func TestInspectSource_RefsMatchSynthesizeInterface(t *testing.T) {
	doc := minimalDoc()
	iface := mustConvertDocToInterface(t, doc, "")

	// Collect binding refs from SynthesizeInterface.
	createRefs := map[string]bool{}
	for _, b := range iface.Bindings {
		createRefs[b.Ref] = true
	}

	// InspectSource should produce the same refs.
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

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Targets {
		if !createRefs[ref.Ref] {
			t.Errorf("InspectSource ref %q not found in SynthesizeInterface bindings", ref.Ref)
		}
	}
	if len(result.Targets) != len(createRefs) {
		t.Errorf("ref count mismatch: InspectSource=%d, SynthesizeInterface=%d", len(result.Targets), len(createRefs))
	}
}

func TestInspectSource_KeysMatchSynthesizeInterface(t *testing.T) {
	// Inspect and create from the same content, so each suggested
	// operationKey must equal the key create actually assigns for that ref.
	// /health has no operationId, exercising the path-derived key path.
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Test API", "version": "1.0.0"},
  "paths": {
    "/users": {
      "get":  {"operationId": "listUsers",  "responses": {"200": {"description": "OK"}}},
      "post": {"operationId": "createUser", "responses": {"200": {"description": "OK"}}}
    },
    "/health": {
      "get": {"responses": {"200": {"description": "OK"}}}
    }
  }
}`

	doc, err := loadDocument("", openbindings.TextContent(content))
	if err != nil {
		t.Fatal(err)
	}
	iface := mustConvertDocToInterface(t, doc, "")

	// Map each ref to the operation key SynthesizeInterface assigned it.
	createKeyByRef := map[string]string{}
	for _, b := range iface.Bindings {
		createKeyByRef[b.Ref] = b.Operation
	}

	result, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{Content: openbindings.TextContent(content)})
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range result.Targets {
		if target.OperationKey == "" {
			t.Errorf("InspectSource target %q has no suggested operationKey", target.Ref)
			continue
		}
		if want := createKeyByRef[target.Ref]; target.OperationKey != want {
			t.Errorf("InspectSource operationKey for ref %q = %q, want %q (SynthesizeInterface's key)", target.Ref, target.OperationKey, want)
		}
	}
}

func TestInspectSource_NoPaths(t *testing.T) {
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Empty", "version": "1.0.0"}
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 0 {
		t.Errorf("expected 0 refs, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_NilContent(t *testing.T) {
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}
