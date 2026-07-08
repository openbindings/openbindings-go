package asyncapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// helper wraps synthesizeInterfaceWithDoc for simpler test calls
func testSynthesizeInterface(t *testing.T, doc *document, location string) openbindings.Interface {
	t.Helper()
	iface, err := synthesizeInterfaceWithDoc(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{Format: FormatToken, Location: location}},
	}, doc)
	if err != nil {
		t.Fatal(err)
	}
	return *iface
}

func TestSynthesizeInterface_CopiesMetadata(t *testing.T) {
	doc := &document{
		AsyncAPI:   "3.0.0",
		Info:       info{Title: "Test API", Version: "1.0.0", Description: "A test"},
		Operations: map[string]asyncOperation{},
	}

	iface := testSynthesizeInterface(t, doc, "")
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

func TestSynthesizeInterface_CreatesOperationsAlphabetically(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Operations: map[string]asyncOperation{
			"zeta":  {Action: "send", Channel: channelRef{Ref: "#/channels/ch"}},
			"alpha": {Action: "receive", Channel: channelRef{Ref: "#/channels/ch"}},
		},
		Channels: map[string]channel{"ch": {Address: "/ch"}},
	}

	iface := testSynthesizeInterface(t, doc, "")
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

func TestSynthesizeInterface_CreatesBindingsWithRefs(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Operations: map[string]asyncOperation{
			"sendMsg": {Action: "send", Channel: channelRef{Ref: "#/channels/messages"}},
		},
		Channels: map[string]channel{"messages": {Address: "/messages"}},
	}

	iface := testSynthesizeInterface(t, doc, "")
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

func TestSynthesizeInterface_SourceLocationConditional(t *testing.T) {
	doc := &document{AsyncAPI: "3.0.0", Operations: map[string]asyncOperation{}}

	withLoc := testSynthesizeInterface(t, doc, "https://example.com/spec.json")
	if withLoc.Sources[DefaultSourceName].Location != "https://example.com/spec.json" {
		t.Errorf("with location: got %q", withLoc.Sources[DefaultSourceName].Location)
	}

	withoutLoc := testSynthesizeInterface(t, doc, "")
	if withoutLoc.Sources[DefaultSourceName].Location != "" {
		t.Errorf("without location: got %q, want empty", withoutLoc.Sources[DefaultSourceName].Location)
	}
}

func TestSynthesizeInterface_NoOperations(t *testing.T) {
	doc := &document{AsyncAPI: "3.0.0", Operations: map[string]asyncOperation{}}
	iface := testSynthesizeInterface(t, doc, "")
	if len(iface.Operations) != 0 {
		t.Errorf("expected 0 operations, got %d", len(iface.Operations))
	}
}

func TestSynthesizeInterface_FormatToken(t *testing.T) {
	doc := &document{AsyncAPI: "3.0.0", Operations: map[string]asyncOperation{}}
	iface := testSynthesizeInterface(t, doc, "")
	if iface.Sources[DefaultSourceName].Format != "asyncapi@3.0" {
		t.Errorf("format = %q, want asyncapi@3.0", iface.Sources[DefaultSourceName].Format)
	}
}

// Content-fed synthesis must emit an invocable source: with no location,
// the provided artifact is embedded (a source needs location or content;
// dropping the content would emit neither).
func TestSynthesizeInterface_ContentOnlyEmbedsSource(t *testing.T) {
	content := `{"asyncapi":"3.0.0","info":{"title":"T","version":"1.0.0"},"operations":{}}`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{Format: "asyncapi@3.0", Content: content}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	src, ok := iface.Sources[DefaultSourceName]
	if !ok {
		t.Fatal("expected the default source entry")
	}
	if src.Location != "" {
		t.Errorf("content-only synthesis must not invent a location, got %q", src.Location)
	}
	if src.Content == nil {
		t.Fatal("content-fed synthesis must embed the artifact")
	}
	if s, _ := src.Content.(string); s != content {
		t.Error("embedded content must be the provided artifact verbatim")
	}
}
