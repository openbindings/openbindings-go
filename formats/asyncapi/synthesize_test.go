package asyncapi

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestSynthesizeInterface_PreservesObjectFormContent(t *testing.T) {
	content := json.RawMessage(`{"asyncapi":"3.0.0","info":{"title":"T","version":"1"},"operations":{}}`)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := iface.Sources[DefaultSourceName].Content; !bytes.Equal(got, content) {
		t.Fatalf("object-form content changed: got %s want %s", got, content)
	}
}

func TestSynthesizeInterface_AppliesStandaloneTraitNormalization(t *testing.T) {
	content := json.RawMessage(`{
  "asyncapi":"3.0.0",
  "info":{"title":"Trait API","version":"1"},
  "servers":{"api":{"host":"api.example.test","protocol":"https"}},
  "channels":{"commands":{"address":"/commands","messages":{"Command":{
    "payload":{"type":"object","properties":{"id":{"type":"integer"}}},
    "traits":[{"$ref":"#/components/messageTraits/json"}]
  }}}},
  "operations":{"submit":{
    "action":"receive",
    "channel":{"$ref":"#/channels/commands"},
    "messages":[{"$ref":"#/channels/commands/messages/Command"}],
    "traits":[{"$ref":"#/components/operationTraits/httpPost"}]
  }},
  "components":{
    "operationTraits":{"httpPost":{"summary":"Submit a command","bindings":{"http":{"method":"POST"}}}},
    "messageTraits":{"json":{"contentType":"application/json"}}
  }
}`)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := iface.Operations["submit"]
	if !ok || operation.Description != "Submit a command" {
		t.Fatalf("operation = %#v", operation)
	}
	input, ok := operation.Input.(map[string]any)
	if !ok || input["type"] != "object" {
		t.Fatalf("input = %#v", operation.Input)
	}
	if _, ok := iface.Bindings["submit.asyncapi"]; !ok {
		t.Fatalf("bindings = %#v", iface.Bindings)
	}
}

func TestSynthesizeInterface_FilePathEmitsInvocableFileURI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api.json")
	content := `{"asyncapi":"3.0.0","info":{"title":"T","version":"1"},"operations":{}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Location: path, Embed: true}},
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
	iface, err := synthesizeInterfaceWithDoc(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Location: location}},
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
	// The routed-envelope ruling (2026-08-14): a headers-declaring message
	// no longer excludes its operation — the direction becomes the envelope.
	if len(iface.Operations) != 2 {
		t.Fatalf("operations = %v, want the bindable operation and the headers envelope operation", iface.Operations)
	}
	output, ok := iface.Operations["good"].Output.(map[string]any)
	if !ok {
		t.Fatalf("good output = %v, want schema union", iface.Operations["good"].Output)
	}
	if choices, ok := output["anyOf"].([]any); !ok || len(choices) != 2 {
		t.Fatalf("good output anyOf = %v, want both artifact message schemas", output["anyOf"])
	}
	envelope, ok := iface.Operations["headers"].Output.(map[string]any)
	if !ok {
		t.Fatalf("headers output = %v, want the routed envelope", iface.Operations["headers"].Output)
	}
	properties, _ := envelope["properties"].(map[string]any)
	if _, present := properties["payload"]; !present {
		t.Fatalf("envelope lacks the payload field: %v", envelope)
	}
	if _, present := properties["headers"]; !present {
		t.Fatalf("envelope lacks the headers field: %v", envelope)
	}
	if envelope["additionalProperties"] != false {
		t.Fatalf("envelope must be closed: %v", envelope)
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
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The routed-envelope ruling (2026-08-14): the headers-declaring
	// message alternative is represented, so nothing here is excluded.
	if !result.Coverage.FullyRepresented {
		t.Fatalf("headers alternatives ride the envelope and every cell is represented; entries=%#v", result.Coverage.Entries)
	}
	for _, entry := range result.Coverage.Entries {
		if entry.ReasonCode == "asyncapi.message_headers" {
			t.Errorf("asyncapi.message_headers must no longer be emitted; entry=%#v", entry)
		}
	}
	foundTarget := false
	for _, entry := range result.Coverage.Entries {
		if entry.SourceRef == "#/operations/publish" && entry.Status == synthesize.SynthesisRepresented {
			foundTarget = true
		}
	}
	if !foundTarget {
		t.Error("bindable publish target was not represented")
	}
	for _, sourceRef := range []string{
		"#/operations/publish#server[0]=broker",
		"#/operations/publish#server[2]=ws",
	} {
		found := false
		for _, entry := range result.Coverage.Entries {
			if entry.SourceRef == sourceRef && entry.Status == synthesize.SynthesisRepresented {
				found = true
			}
		}
		if !found {
			t.Errorf("protocol alternative %s was not represented", sourceRef)
		}
	}
}

func TestSynthesizeInterfaceWithCoverage_RepresentsBothDirectionsOfReplyBearingSend(t *testing.T) {
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

	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	op, ok := result.Interface.Operations["subscribe"]
	if !ok {
		t.Fatalf("reply-bearing send was not synthesized: %#v", result.Interface.Operations)
	}
	input, inputOK := op.Input.(map[string]any)
	output, outputOK := op.Output.(map[string]any)
	if !inputOK || input["type"] != "string" || !outputOK || output["type"] != "object" {
		t.Fatalf("operation directions = input %#v output %#v", op.Input, op.Output)
	}
	if !result.Coverage.FullyRepresented {
		t.Fatalf("driver-independent coverage should be complete: %#v", result.Coverage.Entries)
	}
}

func TestSynthesizeInterfaceWithCoverage_ResolvesReplyDeclaredByReference(t *testing.T) {
	// A Reply Object may be a Reference Object into components.replies. Retaining
	// only the reference leaves a reply with no channel and no messages, which is
	// indistinguishable from an operation that declares no reply, so the reply
	// direction silently disappears. The reference form must be equivalent to the
	// inline form.
	content := json.RawMessage(`{
	  "asyncapi": "3.0.0",
	  "info": {"title": "Referenced reply", "version": "1"},
	  "servers": {"ws": {"host": "api.example", "protocol": "wss"}},
	  "channels": {
	    "events": {"address": "/events", "messages": {"event": {"payload": {"type": "object"}}}},
	    "commands": {"address": "/commands", "messages": {"command": {"payload": {"type": "string"}}}}
	  },
	  "components": {
	    "replies": {
	      "commandReply": {"messages": [{"$ref": "#/channels/commands/messages/command"}]}
	    }
	  },
	  "operations": {
	    "subscribe": {
	      "action": "send",
	      "channel": {"$ref": "#/channels/events"},
	      "messages": [{"$ref": "#/channels/events/messages/event"}],
	      "reply": {"$ref": "#/components/replies/commandReply"}
	    }
	  }
	}`)

	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	op, ok := result.Interface.Operations["subscribe"]
	if !ok {
		t.Fatalf("reply-bearing send was not synthesized: %#v", result.Interface.Operations)
	}
	input, inputOK := op.Input.(map[string]any)
	output, outputOK := op.Output.(map[string]any)
	if !inputOK || input["type"] != "string" || !outputOK || output["type"] != "object" {
		t.Fatalf("operation directions = input %#v output %#v", op.Input, op.Output)
	}
	if !result.Coverage.FullyRepresented {
		t.Fatalf("driver-independent coverage should be complete: %#v", result.Coverage.Entries)
	}
	var replyEntries int
	for _, entry := range result.Coverage.Entries {
		if strings.Contains(entry.SourceRef, "#reply-message") {
			replyEntries++
		}
	}
	if replyEntries == 0 {
		t.Fatalf("referenced reply contributed no reply-message coverage: %#v", result.Coverage.Entries)
	}
}

func TestSynthesizeInterface_PreservesAsyncAPIV2NativeRefAndPerspective(t *testing.T) {
	content := json.RawMessage(`{
	  "asyncapi":"2.6.0",
	  "info":{"title":"Legacy events","version":"1"},
	  "servers":{"broker":{"url":"mqtt://broker.example","protocol":"mqtt"}},
	  "channels":{"events/{tenant}":{"publish":{"message":{"payload":{"type":"object","required":["id"]}}}}}
	}`)
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Interface.Bindings) != 1 {
		t.Fatalf("bindings = %#v", result.Interface.Bindings)
	}
	for _, binding := range result.Interface.Bindings {
		if binding.Ref != "#/channels/events~1{tenant}/publish" {
			t.Fatalf("binding ref = %q", binding.Ref)
		}
	}
	for _, op := range result.Interface.Operations {
		schema, ok := op.Input.(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("input = %#v", op.Input)
		}
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
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: openbindings.TextContent(content)}},
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
