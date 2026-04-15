package asyncapi

import (
	"testing"
)

func TestResolveRefs_SchemaRefInPayload(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test", Version: "1.0.0"},
		Channels: map[string]Channel{
			"events": {
				Address: "/events",
				Messages: map[string]Message{
					"UserEvent": {
						Payload: map[string]any{
							"$ref": "#/components/schemas/UserEvent",
						},
					},
				},
			},
		},
		Operations: map[string]Operation{
			"receiveEvent": {
				Action:  "receive",
				Channel: ChannelRef{Ref: "#/channels/events"},
			},
		},
		Components: &Components{
			Schemas: map[string]any{
				"UserEvent": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"userId": map[string]any{"type": "string"},
						"action": map[string]any{"type": "string"},
					},
					"required": []any{"userId"},
				},
			},
		},
	}

	resolveRefs(doc)

	// The payload in the channel message should be resolved.
	msg, ok := doc.Channels["events"].Messages["UserEvent"]
	if !ok {
		t.Fatal("expected message 'UserEvent' in channel")
	}
	if msg.Payload == nil {
		t.Fatal("expected payload to be resolved, got nil")
	}
	if msg.Payload["type"] != "object" {
		t.Errorf("payload type = %v, want 'object'", msg.Payload["type"])
	}
	props, ok := msg.Payload["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in payload")
	}
	if _, ok := props["userId"]; !ok {
		t.Error("expected 'userId' in properties")
	}
	// Make sure $ref is gone.
	if _, ok := msg.Payload["$ref"]; ok {
		t.Error("$ref should be resolved and removed from payload")
	}
}

func TestResolveRefs_MessageRefInChannel(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test", Version: "1.0.0"},
		Channels: map[string]Channel{
			"events": {
				Address: "/events",
				Messages: map[string]Message{
					"UserEvent": {
						Ref: "#/components/messages/UserEvent",
					},
				},
			},
		},
		Operations: map[string]Operation{
			"receiveEvent": {
				Action:  "receive",
				Channel: ChannelRef{Ref: "#/channels/events"},
				Messages: []MessageRef{
					{Ref: "#/channels/events/messages/UserEvent"},
				},
			},
		},
		Components: &Components{
			Messages: map[string]Message{
				"UserEvent": {
					Name:    "UserEvent",
					Summary: "A user event",
					Payload: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}

	resolveRefs(doc)

	// The message ref in the channel should be resolved to the component message.
	msg, ok := doc.Channels["events"].Messages["UserEvent"]
	if !ok {
		t.Fatal("expected message 'UserEvent' in channel")
	}
	if msg.Name != "UserEvent" {
		t.Errorf("message name = %q, want 'UserEvent'", msg.Name)
	}
	if msg.Summary != "A user event" {
		t.Errorf("message summary = %q, want 'A user event'", msg.Summary)
	}
	if msg.Payload == nil {
		t.Fatal("expected payload in resolved message")
	}
	if msg.Payload["type"] != "object" {
		t.Errorf("payload type = %v, want 'object'", msg.Payload["type"])
	}
}

func TestResolveRefs_NestedSchemaRef(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test", Version: "1.0.0"},
		Channels: map[string]Channel{
			"orders": {
				Address: "/orders",
				Messages: map[string]Message{
					"Order": {
						Payload: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"items": map[string]any{
									"type": "array",
									"items": map[string]any{
										"$ref": "#/components/schemas/OrderItem",
									},
								},
							},
						},
					},
				},
			},
		},
		Operations: map[string]Operation{},
		Components: &Components{
			Schemas: map[string]any{
				"OrderItem": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"sku":      map[string]any{"type": "string"},
						"quantity": map[string]any{"type": "integer"},
					},
				},
			},
		},
	}

	resolveRefs(doc)

	msg := doc.Channels["orders"].Messages["Order"]
	props := msg.Payload["properties"].(map[string]any)
	items := props["items"].(map[string]any)
	itemSchema := items["items"].(map[string]any)

	if itemSchema["type"] != "object" {
		t.Errorf("nested ref type = %v, want 'object'", itemSchema["type"])
	}
	itemProps, ok := itemSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in resolved nested schema")
	}
	if _, ok := itemProps["sku"]; !ok {
		t.Error("expected 'sku' in nested schema properties")
	}
}

func TestResolveRefs_CircularRefNoInfiniteLoop(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test", Version: "1.0.0"},
		Channels: map[string]Channel{
			"nodes": {
				Address: "/nodes",
				Messages: map[string]Message{
					"Node": {
						Payload: map[string]any{
							"$ref": "#/components/schemas/Node",
						},
					},
				},
			},
		},
		Operations: map[string]Operation{},
		Components: &Components{
			Schemas: map[string]any{
				"Node": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "string"},
						"children": map[string]any{
							"type": "array",
							"items": map[string]any{
								"$ref": "#/components/schemas/Node",
							},
						},
					},
				},
			},
		},
	}

	// This should not hang or panic.
	resolveRefs(doc)

	msg := doc.Channels["nodes"].Messages["Node"]
	if msg.Payload == nil {
		t.Fatal("expected payload to be resolved")
	}
	if msg.Payload["type"] != "object" {
		t.Errorf("payload type = %v, want 'object'", msg.Payload["type"])
	}

	// The circular ref should be stopped: the nested $ref should still be
	// present as a $ref (not infinitely expanded).
	props := msg.Payload["properties"].(map[string]any)
	children := props["children"].(map[string]any)
	childItems := children["items"].(map[string]any)
	if _, hasRef := childItems["$ref"]; !hasRef {
		// It's OK if the first level resolved but the circular ref remains.
		// Just verify we didn't infinitely recurse (the test completing is proof).
		if childItems["type"] != "object" {
			t.Log("circular ref was partially resolved, which is acceptable")
		}
	}
}

func TestResolveRefs_NilDocument(t *testing.T) {
	// Should not panic.
	resolveRefs(nil)
}

func TestResolveRefs_NoComponents(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test", Version: "1.0.0"},
		Channels: map[string]Channel{
			"ch": {
				Address: "/ch",
				Messages: map[string]Message{
					"Msg": {
						Payload: map[string]any{
							"type": "string",
						},
					},
				},
			},
		},
		Operations: map[string]Operation{},
	}

	// Should not panic when there are no components/refs.
	resolveRefs(doc)

	msg := doc.Channels["ch"].Messages["Msg"]
	if msg.Payload["type"] != "string" {
		t.Errorf("payload type = %v, want 'string'", msg.Payload["type"])
	}
}

func TestResolveRefs_EndToEnd_CreateInterface(t *testing.T) {
	// Verify that $ref resolution works end-to-end through createInterfaceWithDoc.
	// The message in the operation uses a $ref to a component message,
	// whose payload uses a $ref to a component schema.
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Ref Test", Version: "2.0.0"},
		Channels: map[string]Channel{
			"events": {
				Address: "/events",
				Messages: map[string]Message{
					"MyEvent": {
						Ref: "#/components/messages/MyEvent",
					},
				},
			},
		},
		Operations: map[string]Operation{
			"receiveEvent": {
				Action:  "receive",
				Channel: ChannelRef{Ref: "#/channels/events"},
				Messages: []MessageRef{
					{Ref: "#/components/messages/MyEvent"},
				},
			},
		},
		Components: &Components{
			Messages: map[string]Message{
				"MyEvent": {
					Name: "MyEvent",
					Payload: map[string]any{
						"$ref": "#/components/schemas/EventPayload",
					},
				},
			},
			Schemas: map[string]any{
				"EventPayload": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"eventId": map[string]any{"type": "string"},
						"data":    map[string]any{"type": "object"},
					},
				},
			},
		},
	}

	// resolveRefs is called by loadDocument, but since we're using
	// createInterfaceWithDoc directly, call it manually.
	resolveRefs(doc)

	iface := testCreateInterface(t, doc, "")

	op, ok := iface.Operations["receiveEvent"]
	if !ok {
		t.Fatal("expected operation 'receiveEvent'")
	}
	if op.Output == nil {
		t.Fatal("expected output schema from resolved $ref chain")
	}
	if op.Output["type"] != "object" {
		t.Errorf("output type = %v, want 'object'", op.Output["type"])
	}
	props, ok := op.Output["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in output")
	}
	if _, ok := props["eventId"]; !ok {
		t.Error("expected 'eventId' in output properties")
	}
}

func TestResolveRefs_ComponentSchemaRefToAnotherSchema(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test", Version: "1.0.0"},
		Operations: map[string]Operation{},
		Components: &Components{
			Schemas: map[string]any{
				"Address": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"street": map[string]any{"type": "string"},
						"city":   map[string]any{"type": "string"},
					},
				},
				"User": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"address": map[string]any{
							"$ref": "#/components/schemas/Address",
						},
					},
				},
			},
		},
	}

	resolveRefs(doc)

	userSchema, ok := doc.Components.Schemas["User"].(map[string]any)
	if !ok {
		t.Fatal("expected User schema to be a map")
	}
	props := userSchema["properties"].(map[string]any)
	addr := props["address"].(map[string]any)
	if addr["type"] != "object" {
		t.Errorf("address type = %v, want 'object'", addr["type"])
	}
	addrProps, ok := addr["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties in resolved address schema")
	}
	if _, ok := addrProps["street"]; !ok {
		t.Error("expected 'street' in address properties")
	}
}
