package mcp

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// MCP's InspectSource requires a live server for discovery.
// These tests use the same test server from integration_test.go.

func TestInspectSource_BasicRefs(t *testing.T) {
	ts, _ := setupMCPServer(t)

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Location: ts.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Server has: echo, alwaysFails, slow, longRunning (tools),
	// status (resource), greet (prompt) = 6 refs.
	if len(result.Targets) != 6 {
		t.Fatalf("expected 6 refs, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_RefFormat(t *testing.T) {
	ts, _ := setupMCPServer(t)

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
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
	for _, ref := range result.Targets {
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

func TestInspectSource_RefsMatchSynthesizeInterface(t *testing.T) {
	ts, _ := setupMCPServer(t)
	ctx := context.Background()

	synthesizer := NewSynthesizer()
	iface, err := synthesizer.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
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

	result, err := synthesizer.InspectSource(ctx, &openbindings.Source{
		Location: ts.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Targets {
		if !createRefs[ref.Ref] {
			t.Errorf("InspectSource ref %q not in SynthesizeInterface bindings", ref.Ref)
		}
	}
	if len(result.Targets) != len(createRefs) {
		t.Errorf("ref count mismatch: InspectSource=%d, SynthesizeInterface=%d", len(result.Targets), len(createRefs))
	}
}

func TestInspectSource_Descriptions(t *testing.T) {
	ts, _ := setupMCPServer(t)

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Location: ts.URL,
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

func TestInspectSource_EmptyLocation(t *testing.T) {
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source location")
	}
}
