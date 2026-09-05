package openapi

import (
	"context"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestInspectSource_BasicSelectors(t *testing.T) {
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
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 4 {
		t.Fatalf("expected 4 selectors, got %d", len(result.Targets))
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
        "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "OK"}}
      }
    }
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantSelectors := map[string]bool{
		"#/paths/~1users/get":       false,
		"#/paths/~1users~1{id}/put": false,
	}

	for _, selector := range result.Targets {
		if _, ok := wantSelectors[selector.Selector]; ok {
			wantSelectors[selector.Selector] = true
		}
	}
	for selector, found := range wantSelectors {
		if !found {
			t.Errorf("expected selector %q not found in results", selector)
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
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	descBySelector := map[string]string{}
	for _, selector := range result.Targets {
		if selector.Operation != nil {
			descBySelector[selector.Selector] = selector.Operation.Description
		}
	}

	// Summary is used when description is absent.
	if descBySelector["#/paths/~1pets/get"] != "List pets" {
		t.Errorf("get description = %q, want %q", descBySelector["#/paths/~1pets/get"], "List pets")
	}
	// Description takes precedence over summary.
	if descBySelector["#/paths/~1pets/post"] != "Create a new pet" {
		t.Errorf("post description = %q, want %q", descBySelector["#/paths/~1pets/post"], "Create a new pet")
	}
}

func TestInspectSource_SelectorsMatchSynthesizeInterface(t *testing.T) {
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
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	createSelectors := map[string]bool{}
	for _, b := range iface.Bindings {
		createSelectors[b.Selector] = true
	}

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, selector := range result.Targets {
		if !createSelectors[selector.Selector] {
			t.Errorf("InspectSource selector %q not found in SynthesizeInterface bindings", selector.Selector)
		}
	}
	if len(result.Targets) != len(createSelectors) {
		t.Errorf("selector count mismatch: InspectSource=%d, SynthesizeInterface=%d", len(result.Targets), len(createSelectors))
	}
}

func TestInspectSource_KeysMatchSynthesizeInterface(t *testing.T) {
	// Inspect and create from the same content, so each suggested
	// operationKey must equal the key create actually assigns for that selector.
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

	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	}}})
	if err != nil {
		t.Fatal(err)
	}

	// Map each selector to the operation key SynthesizeInterface assigned it.
	createKeyBySelector := map[string]string{}
	for _, b := range iface.Bindings {
		createKeyBySelector[b.Selector] = b.Operation
	}

	result, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range result.Targets {
		if target.OperationKey == "" {
			t.Errorf("InspectSource target %q has no suggested operationKey", target.Selector)
			continue
		}
		if want := createKeyBySelector[target.Selector]; target.OperationKey != want {
			t.Errorf("InspectSource operationKey for selector %q = %q, want %q (SynthesizeInterface's key)", target.Selector, target.OperationKey, want)
		}
	}
}

func TestInspectSource_NoPaths(t *testing.T) {
	// The 3.0 line makes `paths` REQUIRED: a document that declares none has
	// no position from which any target can be addressed, and §3 part 2's
	// derived whole-source refusal fires (openbindings.openapi-3.0@1 §3).
	content := `{
  "openapi": "3.0.3",
  "info": {"title": "Empty", "version": "1.0.0"}
}`

	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpecOpenAPI30,
		Content:     openbindings.TextContent(content),
	})
	if err == nil {
		t.Fatal("expected the §3 part-2 whole-source refusal for a 3.0 document with no paths")
	}
	if !strings.Contains(err.Error(), "whole-source refusal") {
		t.Errorf("refusal should cite §3 part 2, got: %v", err)
	}

	// The 3.1 line requires only one of paths/components/webhooks: a
	// components-only document conformantly declares no target and inspects
	// as an empty, exhaustive inventory (the emptiness carve-out).
	componentsOnly := `{
  "openapi": "3.1.0",
  "info": {"title": "Empty", "version": "1.0.0"},
  "components": {"schemas": {"Thing": {"type": "object"}}}
}`
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpecOpenAPI31,
		Content:     openbindings.TextContent(componentsOnly),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 0 {
		t.Errorf("expected 0 selectors, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_NilContent(t *testing.T) {
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{BindingSpec: BindingSpecOpenAPI30})
	if err == nil {
		t.Error("expected error for empty source")
	}
}
