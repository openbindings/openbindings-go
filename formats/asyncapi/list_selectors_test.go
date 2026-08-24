package asyncapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSource_BasicRefs(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test API", "version": "1.0.0"},
  "servers": {"ws": {"host": "events.example", "protocol": "wss"}},
  "channels": {
    "messages": {"address": "/messages", "messages": {"event": {"payload": {"type": "object"}}}},
    "events": {"address": "/events", "messages": {"event": {"payload": {"type": "object"}}}}
  },
  "operations": {
    "sendMessage": {
      "action": "send",
      "summary": "Send a message",
      "channel": {"$ref": "#/channels/messages"}
    },
    "receiveEvent": {
      "action": "receive",
      "description": "Receive an event",
      "channel": {"$ref": "#/channels/events"},
      "bindings": {"http": {"method": "POST"}}
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

	if len(result.Targets) != 2 {
		t.Fatalf("expected 2 selectors, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_RefFormat(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "servers": {"ws": {"host": "events.example", "protocol": "wss"}},
  "channels": {
    "ch": {"address": "/ch", "messages": {"event": {"payload": {"type": "object"}}}}
  },
  "operations": {
    "alpha": {
      "action": "send",
      "channel": {"$ref": "#/channels/ch"}
    },
    "beta": {
      "action": "receive",
      "channel": {"$ref": "#/channels/ch"},
      "bindings": {"http": {"method": "POST"}}
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

	wantSelectors := map[string]bool{
		"#/operations/alpha": false,
		"#/operations/beta":  false,
	}
	for _, selector := range result.Targets {
		if _, ok := wantSelectors[selector.Selector]; ok {
			wantSelectors[selector.Selector] = true
		}
	}
	for selector, found := range wantSelectors {
		if !found {
			t.Errorf("expected selector %q not found", selector)
		}
	}
}

func TestInspectSource_RefsMatchSynthesizeInterface(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Servers:  testWSServers(),
		Channels: map[string]channel{"ch": {Address: "/ch", Messages: map[string]message{"event": {Payload: map[string]any{"type": "object"}}}}},
		Operations: map[string]asyncOperation{
			"sendMsg":    {Action: "send", Channel: channelRef{Ref: "#/channels/ch"}},
			"receiveMsg": {Action: "receive", Channel: channelRef{Ref: "#/channels/ch"}, Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}}},
		},
	}

	iface := testSynthesizeInterface(t, doc, "")
	createSelectors := map[string]bool{}
	for _, b := range iface.Bindings {
		createSelectors[b.Selector] = true
	}

	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "servers": {"ws": {"host": "events.example", "protocol": "wss"}},
  "channels": {"ch": {"address": "/ch", "messages": {"event": {"payload": {"type": "object"}}}}},
  "operations": {
    "sendMsg": {"action": "send", "channel": {"$ref": "#/channels/ch"}},
    "receiveMsg": {"action": "receive", "channel": {"$ref": "#/channels/ch"}, "bindings": {"http": {"method": "POST"}}}
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, selector := range result.Targets {
		if !createSelectors[selector.Selector] {
			t.Errorf("InspectSource selector %q not in SynthesizeInterface bindings", selector.Selector)
		}
	}
	if len(result.Targets) != len(createSelectors) {
		t.Errorf("selector count mismatch: InspectSource=%d, SynthesizeInterface=%d", len(result.Targets), len(createSelectors))
	}
}

func TestInspectSource_Description(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "servers": {"ws": {"host": "events.example", "protocol": "wss"}},
  "channels": {"ch": {"address": "/ch", "messages": {"event": {"payload": {"type": "object"}}}}},
  "operations": {
    "withDesc": {
      "action": "send",
      "description": "Full description",
      "summary": "Short summary",
      "channel": {"$ref": "#/channels/ch"}
    },
    "summaryOnly": {
      "action": "receive",
      "summary": "Only summary",
      "channel": {"$ref": "#/channels/ch"},
      "bindings": {"http": {"method": "POST"}}
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

	descBySelector := map[string]string{}
	for _, selector := range result.Targets {
		if selector.Operation != nil {
			descBySelector[selector.Selector] = selector.Operation.Description
		}
	}

	if descBySelector["#/operations/withDesc"] != "Full description" {
		t.Errorf("withDesc description = %q, want %q", descBySelector["#/operations/withDesc"], "Full description")
	}
	if descBySelector["#/operations/summaryOnly"] != "Only summary" {
		t.Errorf("summaryOnly description = %q, want %q", descBySelector["#/operations/summaryOnly"], "Only summary")
	}
}

func TestInspectSource_NoOperations(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Empty", "version": "1.0.0"},
  "operations": {}
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 0 {
		t.Errorf("expected 0 selectors, got %d", len(result.Targets))
	}
}

func TestInspectSource_AlphabeticallySorted(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "servers": {"ws": {"host": "events.example", "protocol": "wss"}},
  "channels": {"ch": {"address": "/ch", "messages": {"event": {"payload": {"type": "object"}}}}},
  "operations": {
    "zeta": {"action": "send", "channel": {"$ref": "#/channels/ch"}},
    "alpha": {"action": "receive", "channel": {"$ref": "#/channels/ch"}, "bindings": {"http": {"method": "POST"}}},
    "mike": {"action": "send", "channel": {"$ref": "#/channels/ch"}}
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 3 {
		t.Fatalf("expected 3 selectors, got %d", len(result.Targets))
	}
	if result.Targets[0].Selector != "#/operations/alpha" {
		t.Errorf("first selector = %q, want #/operations/alpha", result.Targets[0].Selector)
	}
	if result.Targets[1].Selector != "#/operations/mike" {
		t.Errorf("second selector = %q, want #/operations/mike", result.Targets[1].Selector)
	}
	if result.Targets[2].Selector != "#/operations/zeta" {
		t.Errorf("third selector = %q, want #/operations/zeta", result.Targets[2].Selector)
	}
}

func TestInspectSource_NilContent(t *testing.T) {
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}
