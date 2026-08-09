package asyncapi

import "testing"

func TestSynthesisResolvesEscapedChannelAndMessageRefTokens(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "escaped refs", Version: "1.0.0"},
		Servers:  testWSServers(),
		Channels: map[string]channel{
			"/stream": {
				Address: "/stream",
				Messages: map[string]message{
					"event/name": {Payload: map[string]any{"type": "string"}},
				},
			},
		},
		Operations: map[string]asyncOperation{
			"subscribe": {
				Action:   "send",
				Channel:  channelRef{Ref: "#/channels/~1stream"},
				Messages: []messageRef{{Ref: "#/channels/~1stream/messages/event~1name"}},
			},
		},
	}

	iface := testSynthesizeInterface(t, doc, "")
	op, ok := iface.Operations["subscribe"]
	if !ok {
		t.Fatalf("escaped channel ref was classified as dangling: %#v", iface.Operations)
	}
	if op.Output == nil {
		t.Fatal("escaped message ref did not contribute its output schema")
	}
}
