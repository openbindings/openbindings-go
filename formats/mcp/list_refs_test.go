package mcp

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// MCP's ListBindableRefs requires a live server for discovery.
// These tests use the same test server from integration_test.go.

func TestListBindableRefs_BasicRefs(t *testing.T) {
	ts, _ := setupMCPServer(t)

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Location: ts.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Server has: echo (tool), longRunning (tool), status (resource),
	// greet (prompt) = 4 refs.
	if len(result.Refs) != 4 {
		t.Fatalf("expected 4 refs, got %d", len(result.Refs))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestListBindableRefs_RefFormat(t *testing.T) {
	ts, _ := setupMCPServer(t)

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Location: ts.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"tools/echo":             false,
		"resources/app://status": false,
		"prompts/greet":          false,
	}
	for _, ref := range result.Refs {
		if _, ok := wantRefs[ref.Ref]; ok {
			wantRefs[ref.Ref] = true
		}
	}
	for ref, found := range wantRefs {
		if !found {
			t.Errorf("expected ref %q not found", ref)
		}
	}
}

func TestListBindableRefs_RefsMatchCreateInterface(t *testing.T) {
	ts, _ := setupMCPServer(t)
	ctx := context.Background()

	creator := NewCreator()
	iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{{
			Format:   FormatToken,
			Location: ts.URL,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	createRefs := map[string]bool{}
	for _, b := range iface.Bindings {
		createRefs[b.Ref] = true
	}

	result, err := creator.ListBindableRefs(ctx, &openbindings.Source{
		Location: ts.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Refs {
		if !createRefs[ref.Ref] {
			t.Errorf("ListBindableRefs ref %q not in CreateInterface bindings", ref.Ref)
		}
	}
	if len(result.Refs) != len(createRefs) {
		t.Errorf("ref count mismatch: ListBindableRefs=%d, CreateInterface=%d", len(result.Refs), len(createRefs))
	}
}

func TestListBindableRefs_Descriptions(t *testing.T) {
	ts, _ := setupMCPServer(t)

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Location: ts.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	descByRef := map[string]string{}
	for _, ref := range result.Refs {
		descByRef[ref.Ref] = ref.Description
	}

	if descByRef["tools/echo"] != "Echoes the input message" {
		t.Errorf("echo description = %q, want %q", descByRef["tools/echo"], "Echoes the input message")
	}
	if descByRef["resources/app://status"] != "Application status" {
		t.Errorf("status description = %q, want %q", descByRef["resources/app://status"], "Application status")
	}
	if descByRef["prompts/greet"] != "Generate a greeting" {
		t.Errorf("greet description = %q, want %q", descByRef["prompts/greet"], "Generate a greeting")
	}
}

func TestListBindableRefs_EmptyLocation(t *testing.T) {
	creator := NewCreator()
	_, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source location")
	}
}
