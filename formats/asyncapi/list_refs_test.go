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
  "channels": {
    "messages": {"address": "/messages"},
    "events": {"address": "/events"}
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
      "channel": {"$ref": "#/channels/events"}
    }
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_RefFormat(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "channels": {
    "ch": {"address": "/ch"}
  },
  "operations": {
    "alpha": {
      "action": "send",
      "channel": {"$ref": "#/channels/ch"}
    },
    "beta": {
      "action": "receive",
      "channel": {"$ref": "#/channels/ch"}
    }
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"#/operations/alpha": false,
		"#/operations/beta":  false,
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
	doc := &document{
		AsyncAPI: "3.0.0",
		Channels: map[string]channel{"ch": {Address: "/ch"}},
		Operations: map[string]asyncOperation{
			"sendMsg":    {Action: "send", Channel: channelRef{Ref: "#/channels/ch"}},
			"receiveMsg": {Action: "receive", Channel: channelRef{Ref: "#/channels/ch"}},
		},
	}

	iface := testSynthesizeInterface(t, doc, "")
	createRefs := map[string]bool{}
	for _, b := range iface.Bindings {
		createRefs[b.Ref] = true
	}

	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "channels": {"ch": {"address": "/ch"}},
  "operations": {
    "sendMsg": {"action": "send", "channel": {"$ref": "#/channels/ch"}},
    "receiveMsg": {"action": "receive", "channel": {"$ref": "#/channels/ch"}}
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: content,
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

func TestInspectSource_Description(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "channels": {"ch": {"address": "/ch"}},
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
      "channel": {"$ref": "#/channels/ch"}
    }
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: content,
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

	if descByRef["#/operations/withDesc"] != "Full description" {
		t.Errorf("withDesc description = %q, want %q", descByRef["#/operations/withDesc"], "Full description")
	}
	if descByRef["#/operations/summaryOnly"] != "Only summary" {
		t.Errorf("summaryOnly description = %q, want %q", descByRef["#/operations/summaryOnly"], "Only summary")
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
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 0 {
		t.Errorf("expected 0 refs, got %d", len(result.Targets))
	}
}

func TestInspectSource_AlphabeticallySorted(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Test", "version": "1.0.0"},
  "channels": {"ch": {"address": "/ch"}},
  "operations": {
    "zeta": {"action": "send", "channel": {"$ref": "#/channels/ch"}},
    "alpha": {"action": "receive", "channel": {"$ref": "#/channels/ch"}},
    "mike": {"action": "send", "channel": {"$ref": "#/channels/ch"}}
  }
}`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(result.Targets))
	}
	if result.Targets[0].Ref != "#/operations/alpha" {
		t.Errorf("first ref = %q, want #/operations/alpha", result.Targets[0].Ref)
	}
	if result.Targets[1].Ref != "#/operations/mike" {
		t.Errorf("second ref = %q, want #/operations/mike", result.Targets[1].Ref)
	}
	if result.Targets[2].Ref != "#/operations/zeta" {
		t.Errorf("third ref = %q, want #/operations/zeta", result.Targets[2].Ref)
	}
}

func TestInspectSource_NilContent(t *testing.T) {
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}
