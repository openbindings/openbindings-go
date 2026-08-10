package asyncapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestSynthesizeInterface_PreservesObjectFormContent(t *testing.T) {
	content := json.RawMessage(`{"asyncapi":"3.0.0","info":{"title":"T","version":"1"},"operations":{}}`)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := iface.Sources[DefaultSourceName].Content; !bytes.Equal(got, content) {
		t.Fatalf("object-form content changed: got %s want %s", got, content)
	}
}

func TestSynthesizeInterface_FilePathEmitsInvocableFileURI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.json")
	content := `{"asyncapi":"3.0.0","info":{"title":"T","version":"1"},"operations":{}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Location: path, Embed: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := iface.Sources[DefaultSourceName].Location, "file://"+path; got != want {
		t.Fatalf("emitted location = %q, want %q", got, want)
	}
	if got, err := openbindings.ContentToBytes(iface.Sources[DefaultSourceName].Content); err != nil || string(got) != content {
		t.Fatalf("embed directive did not preserve the artifact: %s (%v)", got, err)
	}
}

// helper wraps synthesizeInterfaceWithDoc for simpler test calls
func testSynthesizeInterface(t *testing.T, doc *document, location string) openbindings.Interface {
	t.Helper()
	iface, err := synthesizeInterfaceWithDoc(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Location: location}},
	}, doc)
	if err != nil {
		t.Fatal(err)
	}
	return *iface
}

func testWSServers() map[string]server {
	return map[string]server{"ws": {Host: "events.example", Protocol: "wss"}}
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
		Servers:  testWSServers(),
		Operations: map[string]asyncOperation{
			"zeta":  {Action: "send", Channel: channelRef{Ref: "#/channels/ch"}},
			"alpha": {Action: "receive", Channel: channelRef{Ref: "#/channels/ch"}, Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}}},
		},
		Channels: map[string]channel{"ch": {Address: "/ch", Messages: map[string]message{"event": {Payload: map[string]any{"type": "object"}}}}},
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
		Servers:  testWSServers(),
		Operations: map[string]asyncOperation{
			"sendMsg": {Action: "send", Channel: channelRef{Ref: "#/channels/messages"}},
		},
		Channels: map[string]channel{"messages": {Address: "/messages", Messages: map[string]message{"event": {Payload: map[string]any{"type": "object"}}}}},
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

func TestSynthesizeInterface_UsesInvocationEligibilityAndPreservesMessageAlternatives(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Servers:  testWSServers(),
		Channels: map[string]channel{
			"good": {Address: "/good", Messages: map[string]message{
				"a": {Payload: map[string]any{"type": "string"}},
				"b": {Payload: map[string]any{"type": "number"}},
			}},
			"headers": {Address: "/headers", Messages: map[string]message{
				"event": {Payload: map[string]any{"type": "object"}, Headers: map[string]any{}},
			}},
		},
		Operations: map[string]asyncOperation{
			"good":        {Action: "send", Channel: channelRef{Ref: "#/channels/good"}},
			"headers":     {Action: "send", Channel: channelRef{Ref: "#/channels/headers"}},
			"missing":     {Action: "send", Channel: channelRef{Ref: "#/channels/missing"}},
			"replyNoHTTP": {Action: "receive", Channel: channelRef{Ref: "#/channels/good"}, Reply: &operationReply{}},
		},
	}
	iface := testSynthesizeInterface(t, doc, "")
	if len(iface.Operations) != 1 {
		t.Fatalf("operations = %v, want only the bindable operation", iface.Operations)
	}
	output, ok := iface.Operations["good"].Output.(map[string]any)
	if !ok {
		t.Fatalf("good output = %v, want schema union", iface.Operations["good"].Output)
	}
	if choices, ok := output["anyOf"].([]any); !ok || len(choices) != 2 {
		t.Fatalf("good output anyOf = %v, want both artifact message schemas", output["anyOf"])
	}
}

func TestSynthesizeInterfaceWithCoverage_AccountsForMessagesAndProtocolCells(t *testing.T) {
	content := json.RawMessage(`{
	  "asyncapi": "3.0.0",
	  "info": {"title": "Events", "version": "1"},
	  "servers": {
	    "http": {"host": "api.example", "protocol": "https"},
	    "ws": {"host": "api.example", "protocol": "wss"},
	    "broker": {"host": "api.example", "protocol": "mqtt"}
	  },
	  "channels": {
	    "events": {
	      "address": "/events",
	      "messages": {
	        "good": {"payload": {"type": "object"}},
	        "headers": {"headers": {"type": "object"}, "payload": {"type": "object"}}
	      }
	    },
	    "replies": {
	      "messages": {"reply": {"payload": {"type": "string"}}}
	    }
	  },
	  "operations": {
	    "publish": {
	      "action": "receive",
	      "channel": {"$ref": "#/channels/events"},
	      "bindings": {"http": {"method": "POST"}},
	      "reply": {"channel": {"$ref": "#/channels/replies"}}
	    }
	  }
	}`)
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.FullyRepresented {
		t.Fatal("mixed message and server alternatives must disclose exclusions")
	}
	statusByCode := map[string]openbindings.SynthesisCoverageStatus{}
	for _, entry := range result.Coverage.Entries {
		statusByCode[entry.ReasonCode] = entry.Status
	}
	for _, code := range []string{
		"asyncapi.message_headers",
		"asyncapi.websocket_reply",
		"asyncapi.protocol_outside_revision",
	} {
		if statusByCode[code] != openbindings.SynthesisExcluded {
			t.Errorf("%s status = %q, want excluded; entries=%#v", code, statusByCode[code], result.Coverage.Entries)
		}
	}
	foundTarget := false
	for _, entry := range result.Coverage.Entries {
		if entry.SourceRef == "#/operations/publish" && entry.Status == openbindings.SynthesisRepresented {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Error("bindable publish target was not represented")
	}
}

func TestSynthesizeInterfaceWithCoverage_RefusesReplyBearingWSSendInRevision2(t *testing.T) {
	content := json.RawMessage(`{
	  "asyncapi": "3.0.0",
	  "info": {"title": "Reply-bearing send", "version": "1"},
	  "servers": {"ws": {"host": "api.example", "protocol": "wss"}},
	  "channels": {
	    "events": {"address": "/events", "messages": {"event": {"payload": {"type": "object"}}}},
	    "commands": {"address": "/commands", "messages": {"command": {"payload": {"type": "string"}}}}
	  },
	  "operations": {
	    "subscribe": {
	      "action": "send",
	      "channel": {"$ref": "#/channels/events"},
	      "messages": [{"$ref": "#/channels/events/messages/event"}],
	      "reply": {"messages": [{"$ref": "#/channels/commands/messages/command"}]}
	    }
	  }
	}`)

	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Interface.Operations) != 0 {
		t.Fatalf("revision 2 synthesized reply-bearing send: %#v", result.Interface.Operations)
	}
	found := false
	for _, entry := range result.Coverage.Entries {
		if entry.SourceRef == "#/operations/subscribe" && entry.Status == openbindings.SynthesisExcluded && entry.ReasonCode == "asyncapi.websocket_reply" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing reply-bearing send exclusion: %#v", result.Coverage.Entries)
	}

	legacy, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: LegacyBindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := legacy.Operations["subscribe"]; !ok {
		t.Fatalf("revision 1 compatibility lost its send operation: %#v", legacy.Operations)
	}
	if got := legacy.Sources[DefaultSourceName].BindingSpec; got != LegacyBindingSpec {
		t.Fatalf("legacy source bindingSpec = %q, want %q", got, LegacyBindingSpec)
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

func TestSynthesizeInterface_BindingSpec(t *testing.T) {
	doc := &document{AsyncAPI: "3.0.0", Operations: map[string]asyncOperation{}}
	iface := testSynthesizeInterface(t, doc, "")
	if iface.Sources[DefaultSourceName].BindingSpec != BindingSpec {
		t.Errorf("bindingSpec = %q, want %q", iface.Sources[DefaultSourceName].BindingSpec, BindingSpec)
	}
}

// Content-fed synthesis must emit an invocable source: with no location,
// the provided artifact is embedded (a source needs location or content;
// dropping the content would emit neither).
func TestSynthesizeInterface_ContentOnlyEmbedsSource(t *testing.T) {
	content := `{"asyncapi":"3.0.0","info":{"title":"T","version":"1.0.0"},"operations":{}}`
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(content)}},
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
	if got, err := openbindings.ContentToBytes(src.Content); err != nil || string(got) != content {
		t.Error("embedded content must be the provided artifact verbatim")
	}
}
