package asyncapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestListBindableRefs_BasicRefs(t *testing.T) {
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

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(result.Refs))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestListBindableRefs_RefFormat(t *testing.T) {
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

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"#/operations/alpha": false,
		"#/operations/beta":  false,
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
	doc := &Document{
		AsyncAPI: "3.0.0",
		Channels: map[string]Channel{"ch": {Address: "/ch"}},
		Operations: map[string]Operation{
			"sendMsg":    {Action: "send", Channel: ChannelRef{Ref: "#/channels/ch"}},
			"receiveMsg": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/ch"}},
		},
	}

	iface := testCreateInterface(t, doc, "")
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

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
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

func TestListBindableRefs_Description(t *testing.T) {
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

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	descByRef := map[string]string{}
	for _, ref := range result.Refs {
		descByRef[ref.Ref] = ref.Description
	}

	if descByRef["#/operations/withDesc"] != "Full description" {
		t.Errorf("withDesc description = %q, want %q", descByRef["#/operations/withDesc"], "Full description")
	}
	if descByRef["#/operations/summaryOnly"] != "Only summary" {
		t.Errorf("summaryOnly description = %q, want %q", descByRef["#/operations/summaryOnly"], "Only summary")
	}
}

func TestListBindableRefs_NoOperations(t *testing.T) {
	content := `{
  "asyncapi": "3.0.0",
  "info": {"title": "Empty", "version": "1.0.0"},
  "operations": {}
}`

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(result.Refs))
	}
}

func TestListBindableRefs_AlphabeticallySorted(t *testing.T) {
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

	creator := NewCreator()
	result, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{
		Content: content,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Refs) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(result.Refs))
	}
	if result.Refs[0].Ref != "#/operations/alpha" {
		t.Errorf("first ref = %q, want #/operations/alpha", result.Refs[0].Ref)
	}
	if result.Refs[1].Ref != "#/operations/mike" {
		t.Errorf("second ref = %q, want #/operations/mike", result.Refs[1].Ref)
	}
	if result.Refs[2].Ref != "#/operations/zeta" {
		t.Errorf("third ref = %q, want #/operations/zeta", result.Refs[2].Ref)
	}
}

func TestListBindableRefs_NilContent(t *testing.T) {
	creator := NewCreator()
	_, err := creator.ListBindableRefs(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}
