package asyncapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

type countingRejectTransport struct{ calls int }

func (t *countingRejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls++
	return nil, errors.New("must not fetch")
}

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
	if !operationBindable(doc, addressableOperation(t, doc, "subscribe"), BindingSpec) {
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
	if !operationBindable(doc, op, BindingSpec) {
		t.Fatal("direct externally referenced channel should be bindable")
	}
}

func TestLoadDocumentRetainsDraft07PlainNameIDsAndDanglingSchemaFragments(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/channel.yaml", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`channels:
  commands:
    address: /commands
    messages:
      Command:
        contentType: application/json
        payload:
          $schema: http://json-schema.org/draft-07/schema#
          $id: '#Command'
          type: object
          properties:
            optional: {$ref: '#/missing'}
`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	content := openbindings.TextContent(`asyncapi: 3.0.0
info: {title: External schema, version: "1"}
servers:
  api: {host: api.example.test, protocol: https}
channels:
  commands: {$ref: "./channel.yaml#/channels/commands"}
operations:
  submit:
    action: receive
    channel: {$ref: "#/channels/commands"}
    bindings: {http: {method: POST}}
`)
	doc, err := loadDocument(context.Background(), server.Client(), server.URL+"/root.yaml", content)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	// Composition retains the dangling fragment rather than crashing or
	// silently repairing it; the schema-defect gate then confines the
	// artifact's unresolvable ref to exactly this operation (§9.2): it is
	// excluded as invalid, never emitted as an OBI that fails OBI-D-16.
	exclusion := operationExclusion(doc, addressableOperation(t, doc, "submit"), BindingSpec)
	if exclusion == nil {
		t.Fatal("operation with a dangling payload-schema fragment should be confined, not emitted")
	}
	if exclusion.code != "asyncapi.payload_schema_invalid" || exclusion.status != "invalid" {
		t.Fatalf("unexpected exclusion: %+v", exclusion)
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

func TestLoadDocumentDiscriminatesUnsupportedEditionBeforeExternalRefs(t *testing.T) {
	transport := &countingRejectTransport{}
	client := &http.Client{Transport: transport}
	content := openbindings.TextContent(`asyncapi: 3.2.0
info: {title: Future external schema, version: "1"}
channels: {}
components:
  messages:
    Event:
      payload: {$ref: "https://schema.example.com/shared.json"}
`)

	_, err := loadDocument(context.Background(), client, "https://artifact.example/asyncapi.yaml", content)
	if err == nil || !strings.Contains(err.Error(), "ASYNC-P-01") {
		t.Fatalf("loadDocument error = %v, want ASYNC-P-01", err)
	}
	if transport.calls != 0 {
		t.Fatalf("unsupported edition performed %d external fetches, want zero", transport.calls)
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

// A top-level Avro union is a JSON ARRAY — a legal Avro schema form — so an
// external .avsc referenced from an Avro-declared payload position must
// compose even though its document root is not an object, and the direction
// must derive under the named Avro correspondence with nothing lost
// (MC5 seal-1 finding F-V3-2; the admission is positional — see
// referenceDocumentRootAdmissible).
func TestLoadDocumentComposesTopLevelAvroUnionExternalDocument(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/File.avsc", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`["null", {"type": "record", "name": "File", "fields": [{"name": "path", "type": "string"}]}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	content := openbindings.TextContent(`asyncapi: 2.6.0
info: {title: Avro union, version: "1"}
channels:
  files:
    publish:
      message:
        name: file
        schemaFormat: application/vnd.apache.avro;version=1.9.0
        payload:
          $ref: "./File.avsc"
`)
	doc, err := loadDocument(context.Background(), server.Client(), server.URL+"/root.yaml", content)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	message, ok := doc.Channels["files"].Messages["file"]
	if !ok {
		t.Fatalf("normalized channel message missing; have %v", doc.Channels["files"].Messages)
	}
	// The composed non-object schema rides the Multi Format Schema Object
	// wrapper (hoistNonObjectAvroPayloads), the shape the typed model
	// carries a non-object schema in.
	schema, format := effectivePayload(message)
	if classifySchemaFormat(format) != schemaFormatAvro {
		t.Fatalf("effective schemaFormat = %q, want the declared Avro format", format)
	}
	union, ok := schema.([]any)
	if !ok || len(union) != 2 {
		t.Fatalf("effective schema = %#v, want the composed two-branch union array", schema)
	}
	if reason := messagePayloadLossReason(doc, message); reason != "" {
		t.Fatalf("loss reason = %q, want none: a derivable top-level union crosses as logical values", reason)
	}
	boundary := messagePayloadBoundarySchema(doc, message)
	branches, ok := boundary["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("boundary schema = %#v, want the union's oneOf derivation", boundary)
	}
	if !operationBindable(doc, addressableOperation(t, doc, "v2:publish:files"), BindingSpec) {
		t.Fatal("the Avro-union operation must stay bindable")
	}
}

// The 3.x spelling of the same cell: a Multi Format Schema Object whose
// schema member references the external union document.
func TestLoadDocumentComposesTopLevelAvroUnionAtWrapperSchemaRef(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/File.avsc", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`["null", "string"]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	content := openbindings.TextContent(`asyncapi: 3.0.0
info: {title: Avro union wrapper, version: "1"}
channels:
  files:
    address: files.v1
    messages:
      file:
        payload:
          schemaFormat: application/vnd.apache.avro;version=1.9.0
          schema:
            $ref: "./File.avsc"
operations:
  publishFile:
    action: receive
    channel: {$ref: "#/channels/files"}
    messages: [{$ref: "#/channels/files/messages/file"}]
`)
	doc, err := loadDocument(context.Background(), server.Client(), server.URL+"/root.yaml", content)
	if err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
	message := doc.Channels["files"].Messages["file"]
	if reason := messagePayloadLossReason(doc, message); reason != "" {
		t.Fatalf("loss reason = %q, want none", reason)
	}
	boundary := messagePayloadBoundarySchema(doc, message)
	branches, ok := boundary["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("boundary schema = %#v, want the union's oneOf derivation", boundary)
	}
}

// A non-object external document at an ORDINARY (non-Avro) position keeps
// the object demand.
func TestLoadDocumentRejectsNonObjectExternalDocumentAtStructuralPosition(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/channel.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`["not", "a", "channel"]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	content := openbindings.TextContent(`asyncapi: 3.0.0
info: {title: Bad structural ref, version: "1"}
channels:
  events: {$ref: "./channel.json"}
operations: {}
`)
	_, err := loadDocument(context.Background(), server.Client(), server.URL+"/root.yaml", content)
	if err == nil || !strings.Contains(err.Error(), "not an object") {
		t.Fatalf("err = %v, want the structural object-root refusal", err)
	}
}

// A 2.x document carrying Reference Objects at positions its edition does
// not admit them refuses whole-artifact BEFORE any composition fetch (MC5
// seal-1 finding F-V3-1; the pinned position table lives in the shared
// client, ValidateReferenceAdmission).
func TestLoadDocumentRefusesV2ReferenceObjectAtNonAdmittingPositionBeforeFetch(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"operation position", `asyncapi: 2.4.0
info: {title: Bad, version: "1"}
channels:
  events:
    publish: {$ref: "./ops.yml#/publish"}
`},
		{"whole servers map", `asyncapi: 2.4.0
info: {title: Bad, version: "1"}
servers: {$ref: "./servers.yml"}
channels: {}
`},
		{"string-typed channel description", `asyncapi: 2.4.0
info: {title: Bad, version: "1"}
channels:
  events:
    description: {$ref: "./descriptions.yml#/events"}
    subscribe:
      message: {payload: {type: object}}
`},
		{"servers value before 2.4.0", `asyncapi: 2.2.0
info: {title: Bad, version: "1"}
servers:
  main: {$ref: "./servers.yml#/main"}
channels: {}
`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &countingRejectTransport{}
			client := &http.Client{Transport: transport}
			_, err := loadDocument(context.Background(), client, "https://example.test/root.yaml", openbindings.TextContent(testCase.content))
			if err == nil || !strings.Contains(err.Error(), "does not admit a Reference Object") {
				t.Fatalf("err = %v, want the position-admission refusal", err)
			}
			if transport.calls != 0 {
				t.Fatalf("composition fetched %d documents; admission must refuse first", transport.calls)
			}
		})
	}
}

// The same positions with an admitting counterpart: a servers-map VALUE ref
// is legal from 2.4.0 (Server Object | Reference Object), and the Channel
// Item's own $ref field is legal in every 2.x edition.
func TestLoadDocumentAdmitsV2ReferenceObjectAtAdmittingPositions(t *testing.T) {
	content := openbindings.TextContent(`asyncapi: 2.4.0
info: {title: Fine, version: "1"}
servers:
  main: {$ref: "#/components/x-servers/main"}
channels:
  events:
    subscribe:
      message: {payload: {type: object}}
`)
	client := &http.Client{Transport: &countingRejectTransport{}}
	if _, err := loadDocument(context.Background(), client, "https://example.test/root.yaml", content); err != nil {
		t.Fatalf("loadDocument: %v", err)
	}
}
