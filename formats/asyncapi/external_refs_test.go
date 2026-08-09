package asyncapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestLoadDocumentResolvesExternalServerChannelAndMessageClosure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/servers.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("servers:\n  events:\n    host: example.test\n    protocol: wss\n"))
	})
	mux.HandleFunc("/channels.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("channels:\n  events:\n    address: /events\n    messages:\n      event:\n        $ref: './messages.yaml#/messages/event'\n"))
	})
	mux.HandleFunc("/messages.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("messages:\n  event:\n    contentType: application/json\n    payload:\n      type: object\n      properties:\n        id:\n          type: string\n"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	content := openbindings.TextContent(`asyncapi: 3.0.0
info: {title: External, version: "1"}
servers:
  events: {$ref: "./servers.yaml#/servers/events"}
channels:
  events: {$ref: "./channels.yaml#/channels/events"}
operations:
  subscribe:
    action: send
    channel: {$ref: "#/channels/events"}
    messages: [{$ref: "#/channels/events/messages/event"}]
`)
	doc, err := loadDocument(context.Background(), server.Client(), server.URL+"/root.yaml", content)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	if got := doc.Servers["events"].Protocol; got != "wss" {
		t.Fatalf("server protocol = %q, want wss", got)
	}
	if got := doc.Channels["events"].Messages["event"].Payload["type"]; got != "object" {
		t.Fatalf("message payload type = %v, want object", got)
	}
	if !operationBindable(doc, addressableOperation(t, doc, "subscribe")) {
		t.Fatal("externally composed subscription should be bindable")
	}
}

func TestLoadDocumentHoistsDirectExternalOperationChannel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/level2.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("channels:\n  data:\n    address: /data\n    messages:\n      event:\n        $ref: './level3.yaml#/components/messages/Event'\n"))
	})
	mux.HandleFunc("/level3.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("components:\n  messages:\n    Event:\n      contentType: application/json\n      payload:\n        type: string\n"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	content := openbindings.TextContent(`asyncapi: 3.0.0
info: {title: External, version: "1"}
servers:
  events: {host: example.test, protocol: wss}
operations:
  subscribe:
    action: send
    channel: {$ref: "./level2.yaml#/channels/data"}
`)
	doc, err := loadDocument(context.Background(), server.Client(), server.URL+"/level1.yaml", content)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	op := addressableOperation(t, doc, "subscribe")
	channelName := extractRefName(op.Channel.Ref)
	if channelName == "" || doc.Channels[channelName].Address != "/data" {
		t.Fatalf("resolved channel %q was not hoisted into the primary document", channelName)
	}
	if !operationBindable(doc, op) {
		t.Fatal("direct externally referenced channel should be bindable")
	}
}

func TestLoadDocumentRejectsRelativeExternalRefWithoutBase(t *testing.T) {
	content := openbindings.TextContent(`asyncapi: 3.0.0
info: {title: External, version: "1"}
channels:
  events: {$ref: "./channels.yaml#/channels/events"}
operations: {}
`)
	_, err := loadDocument(context.Background(), nil, "", content)
	if err == nil {
		t.Fatal("expected a relative external ref without a base URI to be refused")
	}
}

func addressableOperation(t *testing.T, doc *document, name string) *asyncOperation {
	t.Helper()
	op, ok := doc.Operations[name]
	if !ok {
		t.Fatalf("operation %q not found", name)
	}
	return &op
}
