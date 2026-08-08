package mcp

import (
	"context"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// MCP's InspectSource dials live for discovery unless the source carries a
// pinned listing (MCP-D-01), which is read offline.
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

	// Server has: echo, alwaysFails, slow, longRunning, progressZeroTotal
	// (tools), status (resource), greet (prompt) = 7 refs.
	if len(result.Targets) != 7 {
		t.Fatalf("expected 7 refs, got %d", len(result.Targets))
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
			BindingSpec: LegacyBindingSpec,
			Location:    ts.URL,
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

// ---------------------------------------------------------------------------
// Pinned-listing inspection (MCP-D-01, §6 content primacy)
// ---------------------------------------------------------------------------

// A source carrying content is a pinned listing: inspection reads the pin
// OFFLINE — the server is never dialed (the dead-end counter proves it) —
// and previews the same keys pinned synthesis assigns.
func TestInspectSource_PinnedListingIsOffline(t *testing.T) {
	ts, requests := deadEndServer(t)

	pin := map[string]any{
		"tools":     []any{map[string]any{"name": "echo", "description": "Echoes"}},
		"resources": []any{map[string]any{"uri": "app://status", "name": "status"}},
		"prompts":   []any{map[string]any{"name": "greet"}},
	}

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: LegacyBindingSpec,
		Location:    ts.URL,
		Content:     mustContent(pin),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}

	got := map[string]string{}
	for _, target := range result.Targets {
		got[target.Ref] = target.OperationKey
	}
	want := map[string]string{
		"tools/echo":             "echo",
		"resources/app://status": "status",
		"prompts/greet":          "greet",
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want refs %v", got, want)
	}
	for ref, key := range want {
		if got[ref] != key {
			t.Errorf("ref %q operationKey = %q, want %q", ref, got[ref], key)
		}
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("pinned inspection must be offline; server saw %d requests", n)
	}
}

// An invalid pin refuses inspection loudly before any I/O, and content does
// not waive MCP-D-02's location requirement.
func TestInspectSource_InvalidPinRefusedLoudly(t *testing.T) {
	ts, requests := deadEndServer(t)

	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Location:    ts.URL,
		Content:     mustContent(map[string]any{"tools": []any{}, "nextCursor": "p2"}),
	})
	if err == nil || !strings.Contains(err.Error(), "MCP-D-01") {
		t.Fatalf("expected an MCP-D-01 refusal, got: %v", err)
	}

	_, err = synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Content:     mustContent(map[string]any{"tools": []any{map[string]any{"name": "probe"}}}),
	})
	if err == nil || !strings.Contains(err.Error(), "MCP-D-02") {
		t.Fatalf("expected an MCP-D-02 refusal for a pin without location, got: %v", err)
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("pin refusals must precede network I/O; server saw %d requests", n)
	}
}

// Without content there is no pin: inspection still dials the live server.
func TestInspectSource_AbsentContentDialsLive(t *testing.T) {
	ts, requests := deadEndServer(t)

	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Location:    ts.URL,
	})
	if err == nil {
		t.Fatal("expected discovery against a dead-end server to fail")
	}
	if n := requests.Load(); n == 0 {
		t.Error("absent content must dial the live server; server saw no requests")
	}
}
