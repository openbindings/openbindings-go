package asyncapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// helper wraps createInterfaceWithDoc for simpler test calls
func testCreateInterface(t *testing.T, doc *Document, location string) openbindings.Interface {
	t.Helper()
	iface, err := createInterfaceWithDoc(context.Background(), &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{{Format: FormatToken, Location: location}},
	}, doc)
	if err != nil {
		t.Fatal(err)
	}
	return *iface
}

func TestCreateInterface_CopiesMetadata(t *testing.T) {
	doc := &Document{
		AsyncAPI:   "3.0.0",
		Info:       Info{Title: "Test API", Version: "1.0.0", Description: "A test"},
		Operations: map[string]Operation{},
	}

	iface := testCreateInterface(t, doc, "")
	if iface.Name != "Test API" {
		t.Errorf("Name = %q, want %q", iface.Name, "Test API")
	}
	if iface.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", iface.Version, "1.0.0")
	}
	if iface.Description != "A test" {
		t.Errorf("Description = %q, want %q", iface.Description, "A test")
	}
}

func TestCreateInterface_CreatesOperationsAlphabetically(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Operations: map[string]Operation{
			"zeta":  {Action: "send", Channel: ChannelRef{Ref: "#/channels/ch"}},
			"alpha": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/ch"}},
		},
		Channels: map[string]Channel{"ch": {Address: "/ch"}},
	}

	iface := testCreateInterface(t, doc, "")
	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["alpha"]; !ok {
		t.Error("expected operation 'alpha'")
	}
	if _, ok := iface.Operations["zeta"]; !ok {
		t.Error("expected operation 'zeta'")
	}
}

func TestCreateInterface_CreatesBindingsWithRefs(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Operations: map[string]Operation{
			"sendMsg": {Action: "send", Channel: ChannelRef{Ref: "#/channels/messages"}},
		},
		Channels: map[string]Channel{"messages": {Address: "/messages"}},
	}

	iface := testCreateInterface(t, doc, "")
	key := "sendMsg." + DefaultSourceName
	binding, ok := iface.Bindings[key]
	if !ok {
		t.Fatalf("expected binding %q", key)
	}
	if binding.Ref != "#/operations/sendMsg" {
		t.Errorf("ref = %q, want %q", binding.Ref, "#/operations/sendMsg")
	}
	if binding.Operation != "sendMsg" {
		t.Errorf("operation = %q, want %q", binding.Operation, "sendMsg")
	}
}

func TestCreateInterface_SourceLocationConditional(t *testing.T) {
	doc := &Document{AsyncAPI: "3.0.0", Operations: map[string]Operation{}}

	withLoc := testCreateInterface(t, doc, "https://example.com/spec.json")
	if withLoc.Sources[DefaultSourceName].Location != "https://example.com/spec.json" {
		t.Errorf("with location: got %q", withLoc.Sources[DefaultSourceName].Location)
	}

	withoutLoc := testCreateInterface(t, doc, "")
	if withoutLoc.Sources[DefaultSourceName].Location != "" {
		t.Errorf("without location: got %q, want empty", withoutLoc.Sources[DefaultSourceName].Location)
	}
}

func TestCreateInterface_NoOperations(t *testing.T) {
	doc := &Document{AsyncAPI: "3.0.0", Operations: map[string]Operation{}}
	iface := testCreateInterface(t, doc, "")
	if len(iface.Operations) != 0 {
		t.Errorf("expected 0 operations, got %d", len(iface.Operations))
	}
}

func TestCreateInterface_FormatToken(t *testing.T) {
	doc := &Document{AsyncAPI: "3.0.0", Operations: map[string]Operation{}}
	iface := testCreateInterface(t, doc, "")
	if iface.Sources[DefaultSourceName].Format != "asyncapi@3.0" {
		t.Errorf("format = %q, want asyncapi@3.0", iface.Sources[DefaultSourceName].Format)
	}
}
