package asyncapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go/invoke"

	"github.com/coder/websocket"

	openbindings "github.com/openbindings/openbindings-go"
)

const testSecret = "test-token-123"

// testStore is a minimal in-memory ContextStore for exercising the
// store-backed resolver. The SDK no longer ships a built-in store; consuming
// tools own storage, so tests supply their own.
type testStore map[string]map[string]any

func (s testStore) Get(_ context.Context, key string) (map[string]any, error) {
	return s[key], nil
}

func (s testStore) Set(_ context.Context, key string, value map[string]any) error {
	if value == nil {
		delete(s, key)
		return nil
	}
	s[key] = value
	return nil
}

func (s testStore) Delete(_ context.Context, key string) error {
	delete(s, key)
	return nil
}

// ---------------------------------------------------------------------------
// Handle-driving helpers
// ---------------------------------------------------------------------------

func bg() context.Context { return context.Background() }

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var ie *invoke.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %T: %v", err, err)
	}
	return ie.Code
}

func wsTextContext(bindCtx map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range bindCtx {
		out[key] = value
	}
	configuration := map[string]any{}
	if current, ok := out["configuration"].(map[string]any); ok {
		for key, value := range current {
			configuration[key] = value
		}
	}
	configuration["websocketMessageType"] = "text"
	out["configuration"] = configuration
	return out
}

// drainOutputs reads the invocation's outputs to EOF or terminal error.
func drainOutputs(t *testing.T, call invoke.Invocation[any, any]) ([]any, error) {
	t.Helper()
	out := call.Outputs()
	var vals []any
	for {
		v, err := out.Read(shortCtx(t))
		if err == io.EOF {
			return vals, nil
		}
		if err != nil {
			return vals, err
		}
		vals = append(vals, v)
	}
}

// ---------------------------------------------------------------------------
// HTTP fixture: unary publish + SSE subscription
// ---------------------------------------------------------------------------

// makeAsyncAPISpec builds the HTTP test document. Operation names describe
// the INVOKER's role; the `action` values are the artifact's, declared from
// the described application's perspective (ASYNC-P-02): the ops the tests
// publish to carry action "receive" (the app receives), the ops the tests
// subscribe to carry action "send" (the app sends). Bearer security is
// declared per-operation (sendMessage, receiveEvents); sendOpenMessage,
// sendAck, and receiveStream carry no security so server-side failures and
// cancellation can be exercised without tripping the CONTEXT_REQUIRED gate.
func makeAsyncAPISpec(baseURL string) *document {
	return &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "Test API", Version: "1.0.0"},
		Servers: map[string]server{
			"test": {
				Host:     strings.TrimPrefix(strings.TrimPrefix(baseURL, "http://"), "https://"),
				Protocol: "http",
			},
		},
		Channels: map[string]channel{
			"messages": {Address: "/messages"},
			"events":   {Address: "/events"},
			"stream":   {Address: "/stream"},
			"ack":      {Address: "/ack"},
		},
		Operations: map[string]asyncOperation{
			"sendMessage": {
				Action:   "receive",
				Channel:  channelRef{Ref: "#/channels/messages"},
				Messages: []messageRef{{Ref: "#/components/messages/json"}},
				Reply:    &operationReply{Messages: []messageRef{{Ref: "#/components/messages/json"}}},
				Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}},
				Security: []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}},
			},
			"sendOpenMessage": {
				Action: "receive", Channel: channelRef{Ref: "#/channels/messages"},
				Messages: []messageRef{{Ref: "#/components/messages/json"}},
				Reply:    &operationReply{Messages: []messageRef{{Ref: "#/components/messages/json"}}},
				Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}},
			},
			"sendAck": {
				Action: "receive", Channel: channelRef{Ref: "#/channels/ack"},
				Messages: []messageRef{{Ref: "#/components/messages/json"}},
				Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}},
			},
			"receiveEvents": {
				Action:   "send",
				Channel:  channelRef{Ref: "#/channels/events"},
				Messages: []messageRef{{Ref: "#/components/messages/json"}},
				Security: []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}},
			},
			"receiveStream": {Action: "send", Channel: channelRef{Ref: "#/channels/stream"}, Messages: []messageRef{{Ref: "#/components/messages/json"}}},
		},
		Components: &components{
			Messages: map[string]message{
				"json": {Name: "json", ContentType: "application/json"},
			},
			SecuritySchemes: map[string]securityScheme{
				"bearer": {Type: "http", Scheme: "bearer"},
			},
		},
	}
}

// newHTTPFixture starts the HTTP test server and returns it with a request
// counter for zero-I/O assertions.
func newHTTPFixture(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)

		switch {
		case r.URL.Path == "/messages" && r.Method == "POST":
			if r.Header.Get("Authorization") != "Bearer "+testSecret {
				w.WriteHeader(401)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"received": body})

		case r.URL.Path == "/ack" && r.Method == "POST":
			w.WriteHeader(202)

		case r.URL.Path == "/events" && r.Method == "GET":
			if r.Header.Get("Authorization") != "Bearer "+testSecret {
				w.WriteHeader(401)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: {\"seq\":1}\n\n")
			flusher.Flush()
			fmt.Fprintf(w, "data: {\"seq\":2}\n\n")
			flusher.Flush()

		case r.URL.Path == "/stream" && r.Method == "GET":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: {\"tick\":1}\n\n")
			flusher.Flush()
			<-r.Context().Done() // never ends; the client cancels

		case r.URL.Path == "/spec.json" && r.Method == "GET":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(makeAsyncAPISpec(srv.URL))

		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func httpSource(srv *httptest.Server) invoke.InvocationSource {
	return invoke.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(makeAsyncAPISpec(srv.URL))}
}

// ---------------------------------------------------------------------------
// CONTEXT_REQUIRED negotiation
// ---------------------------------------------------------------------------

func TestContextRequiredBeforeAnyIO(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	before := requests.Load()
	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendMessage",
	})
	if err := call.Write(bg(), map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)

	var ie *invoke.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %v", err)
	}
	details := invoke.ContextRequiredFrom(ie)
	if details == nil {
		t.Fatalf("expected CONTEXT_REQUIRED with details, got %v", err)
	}
	if details.Target != srv.URL {
		t.Errorf("Target = %q, want %q", details.Target, srv.URL)
	}
	if len(details.Alternatives) != 1 || details.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Errorf("alternatives = %+v, want one auth.bearer requirement", details.Alternatives)
	}
	if got := requests.Load(); got != before {
		t.Errorf("challenge must precede any I/O: %d requests dispatched", got-before)
	}
}

func TestExcludedHTTPSubscriptionPrecedesCredentialNegotiation(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	before := requests.Load()
	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/receiveEvents",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeRefused {
		t.Fatalf("expected standalone HTTP send exclusion, got %v", err)
	}
	if got := requests.Load(); got != before {
		t.Errorf("challenge must precede any I/O: %d requests dispatched", got-before)
	}
}

// TestRealAsyncAPI30SecurityListParsesAndChallenges is an end-to-end
// regression test for the AsyncAPI 3.0 security shape, parsed from raw JSON
// text (not constructed via Go struct literals) so it exercises the same
// json.Unmarshal path a real source document takes. Per the AsyncAPI 3.0
// spec, `security` is a flat LIST of Security Scheme Objects or Reference
// Objects — before the fix, the document model declared it as
// []map[string][]string (OpenAPI/2.x-style requirement maps), which cannot
// even unmarshal a real 3.0 security declaration: json.Unmarshal fails with
// "cannot unmarshal string into Go value of type []string" for BOTH the
// $ref form and the inline-scheme form, so loadDocument returned a hard
// error for any real-world 3.0 document declaring security this way.
func TestRealAsyncAPI30SecurityListParsesAndChallenges(t *testing.T) {
	docJSON := `{
		"asyncapi": "3.0.0",
		"info": {"title": "t", "version": "1.0.0"},
		"servers": {"prod": {"host": "api.example.com", "protocol": "https"}},
		"channels": {"c": {"address": "/c"}},
		"operations": {
			"refScheme": {
				"action": "send",
				"channel": {"$ref": "#/channels/c"},
				"security": [{"$ref": "#/components/securitySchemes/bearer"}]
			},
			"inlineScheme": {
				"action": "send",
				"channel": {"$ref": "#/channels/c"},
				"security": [{"type": "http", "scheme": "bearer"}]
			}
		},
		"components": {
			"securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}}
		}
	}`

	binv := NewInvoker()
	defer binv.Close()

	for _, opRef := range []string{"#/operations/refScheme", "#/operations/inlineScheme"} {
		t.Run(opRef, func(t *testing.T) {
			details, err := binv.PrepareBinding(bg(), &invoke.BindingInvocationArgs{
				Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(docJSON)},
				Ref:    opRef,
			})
			if err != nil {
				t.Fatalf("document failed to parse (the real bug this guards against): %v", err)
			}
			if details == nil {
				t.Fatal("expected a CONTEXT_REQUIRED challenge for the declared bearer security, got nil")
			}
			if details.Target != "https://api.example.com" {
				t.Errorf("Target = %q, want https://api.example.com", details.Target)
			}
			if len(details.Alternatives) != 1 || details.Alternatives[0].Requirements[0].Type != "auth.bearer" {
				t.Errorf("alternatives = %+v, want one auth.bearer requirement", details.Alternatives)
			}

			// A bearer token in context satisfies the challenge.
			ok, err := binv.PrepareBinding(bg(), &invoke.BindingInvocationArgs{
				Source:  invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(docJSON)},
				Ref:     opRef,
				Context: map[string]any{"bearerToken": "t"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if ok != nil {
				t.Errorf("expected nil once the bearer token is supplied, got %+v", ok)
			}
		})
	}
}

// TestChannelWithoutAddressIsRefusedPreDispatch pins the address
// configuration point's refusal (ASYNC-P-04): an absent or null channel
// `address` with no consumer-supplied address is a PRE-DISPATCH refusal —
// this specification does not assume the channel key is an address, never a
// guess. (Flipped from the pre-conformance channel-name fallback this test
// used to pin.) A consumer-supplied concrete address at the configuration
// point proceeds.
func TestChannelWithoutAddressIsRefusedPreDispatch(t *testing.T) {
	var requests atomic.Int32
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		gotPath = r.URL.Path
		w.WriteHeader(202)
	}))
	defer srv.Close()

	docJSON := fmt.Sprintf(`{
		"asyncapi": "3.0.0",
		"info": {"title": "t", "version": "1.0.0"},
		"servers": {"test": {"host": %q, "protocol": "http"}},
		"channels": {"notify": {"messages": {"json": {"name": "json", "contentType": "application/json"}}}},
		"operations": {
			"notifyOp": {
				"action": "receive",
				"channel": {"$ref": "#/channels/notify"},
				"bindings": {"http": {"method": "POST"}}
			}
		}
	}`, strings.TrimPrefix(srv.URL, "http://"))

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(docJSON)},
		Ref:    "#/operations/notifyOp",
	})
	if err := call.Write(bg(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	// R1a: AsyncAPI's absent (runtime-generated) address is resolvable by
	// consumer supply — a config.value CONTEXT_REQUIRED (address point,
	// non-durable), not a terminal ERR_SOURCE_CONFIG_ERROR.
	req := assertConfigValue(t, err, "address", "")
	if req.Durable == nil || *req.Durable {
		t.Errorf("a runtime-generated address must be non-durable, got durable=%v", req.Durable)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("the refusal is pre-dispatch: %d requests dispatched", got)
	}

	// The consumer may supply the concrete address at the configuration
	// point; the publish then dispatches to exactly that address.
	call = binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(docJSON)},
		Ref:     "#/operations/notifyOp",
		Context: map[string]any{"configuration": map[string]any{"address": "/inbox"}},
	})
	if err := call.Write(bg(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := drainOutputs(t, call); err != nil {
		t.Fatalf("publish with a supplied address failed: %v", err)
	}
	if gotPath != "/inbox" {
		t.Errorf("request path = %q, want the consumer-supplied /inbox", gotPath)
	}
}

// ---------------------------------------------------------------------------
// Unary send over HTTP
// ---------------------------------------------------------------------------

func TestUnarySendAppliesBearerAndYieldsResponse(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  httpSource(srv),
		Ref:     "#/operations/sendMessage",
		Context: map[string]any{"bearerToken": testSecret},
	})
	if err := call.Write(bg(), map[string]any{"text": "hello"}); err != nil {
		t.Fatal(err)
	}

	v, err := invoke.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	received, _ := v.(map[string]any)["received"].(map[string]any)
	if received["text"] != "hello" {
		t.Fatalf("got %v", v)
	}

}

func TestServer401IsStructuralAndProtocolBlind(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	// sendOpenMessage declares no security, so the request dispatches and the
	// server's 401 surfaces operationally.
	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendOpenMessage",
	})
	if err := call.Write(bg(), map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	var ie *invoke.InvocationError
	if !errors.As(err, &ie) || ie.Code != invoke.ErrCodeExecutionFailed {
		t.Fatalf("expected ERR_EXECUTION_FAILED, got %v", err)
	}
	if ie.HasData() {
		t.Fatalf("HTTP evidence leaked into abstract data: %#v", ie.Data)
	}
}

func TestMissingInputOnSend(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendOpenMessage",
	})
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeRefused {
		t.Fatalf("expected ERR_REFUSED, got %v", err)
	}
}

// TestNoInputOperationRefused_HTTPPublish verifies the presence rule
// (ASYNC-P-03): a publish invocation requires an input value — the input IS
// the message, and this family defines no empty message. An operation-layer
// call for an operation declaring no input (Binding != nil, InputSchema ==
// nil) is refused before dispatch, never substituted with an empty object.
func TestNoInputOperationRefused_HTTPPublish(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	before := requests.Load()
	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  httpSource(srv),
		Ref:     "#/operations/sendAck",
		Binding: &openbindings.BindingEntry{Operation: "sendAck", Source: DefaultSourceName, Ref: "#/operations/sendAck"},
		// InputSchema nil → no-input operation; publish has no empty message.
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeRefused {
		t.Fatalf("expected ERR_REFUSED for a no-input publish (pre-dispatch), got %v", err)
	}
	if got := requests.Load(); got != before {
		t.Errorf("the refusal is pre-dispatch: %d requests dispatched", got-before)
	}
}

// TestNoInputOperationRefused_WSPublish is the WebSocket variant: the
// refusal fires before any socket is dialed.
func TestNoInputOperationRefused_WSPublish(t *testing.T) {
	var upgrades atomic.Int32
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		<-ctx.Done()
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, nil),
		Ref:     "#/operations/publish",
		Binding: &openbindings.BindingEntry{Operation: "publish", Source: DefaultSourceName, Ref: "#/operations/publish"},
		Context: wsTextContext(nil),
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeRefused {
		t.Fatalf("expected ERR_REFUSED for a no-input ws publish (pre-dial), got %v", err)
	}
	if c := upgrades.Load(); c != 0 {
		t.Errorf("the refusal is pre-dispatch: %d upgrades dialed", c)
	}
}

// TestWSPublishZeroMessagesRefused verifies the streaming face of the same
// presence rule: closing input with zero messages sent fails loudly — a
// publish that published nothing is not a success.
func TestWSPublishZeroMessagesRefused(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		<-ctx.Done()
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, nil),
		Ref:     "#/operations/publish",
		Context: wsTextContext(nil),
	})
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeRefused {
		t.Fatalf("expected ERR_REFUSED for a zero-message publish (pre-send), got %v", err)
	}
}

func TestSendAckYieldsZeroOutputs(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendAck",
	})
	if err := call.Write(bg(), map[string]any{"cmd": "go"}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 0 {
		t.Fatalf("a 202 publish ack is not an output, got %v", vals)
	}
}

// sseEventDoc builds a minimal standalone HTTP `send` operation. Revision 1
// uses it to prove that an event-looking endpoint is still excluded rather
// than being inferred as an SSE subscription.
func sseEventDoc(baseURL, path string) *document {
	return &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "SSE Cap Test", Version: "1.0.0"},
		Servers: map[string]server{
			"test": {Host: strings.TrimPrefix(baseURL, "http://"), Protocol: "http"},
		},
		Channels:   map[string]channel{"caps": {Address: path}},
		Operations: map[string]asyncOperation{"receiveCaps": {Action: "send", Channel: channelRef{Ref: "#/channels/caps"}}},
	}
}

func TestHTTPSubscriptionDoesNotInferSSE(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: excluded\n\n")
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(sseEventDoc(srv.URL, "/"))},
		Ref:    "#/operations/receiveCaps",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != invoke.ErrCodeRefused {
		t.Fatalf("expected standalone HTTP send exclusion, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("excluded HTTP send dispatched %d requests", requests.Load())
	}
}

// ---------------------------------------------------------------------------
// Wiring failures (pre-side-effect)
// ---------------------------------------------------------------------------

func TestWiringErrors(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	cases := []struct {
		name string
		args *invoke.BindingInvocationArgs
		code string
	}{
		{"unknown operation", &invoke.BindingInvocationArgs{
			Source: httpSource(srv), Ref: "#/operations/nope",
		}, invoke.ErrCodeRefNotFound},
		{"empty ref", &invoke.BindingInvocationArgs{
			Source: httpSource(srv), Ref: "",
		}, invoke.ErrCodeInvalidRef},
		{"unparsable source", &invoke.BindingInvocationArgs{
			Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent("not asyncapi")},
			Ref:    "#/operations/sendMessage",
		}, invoke.ErrCodeSourceLoadFailed},
	}
	before := requests.Load()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := binv.InvokeBinding(bg(), tc.args)
			_, err := drainOutputs(t, call)
			if codeOf(t, err) != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
	if got := requests.Load(); got != before {
		t.Errorf("wiring failures must not dispatch requests: %d dispatched", got-before)
	}
}

// ---------------------------------------------------------------------------
// PrepareBinding (side-effect-free preflight)
// ---------------------------------------------------------------------------

func TestPrepareBindingReportsBearerRequirement(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	before := requests.Load()
	details, err := binv.PrepareBinding(bg(), &invoke.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendMessage",
	})
	if err != nil {
		t.Fatal(err)
	}
	if details == nil {
		t.Fatal("expected details")
	}
	if details.Target != srv.URL {
		t.Errorf("Target = %q, want %q", details.Target, srv.URL)
	}
	if len(details.Alternatives) != 1 || details.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Errorf("alternatives = %+v", details.Alternatives)
	}
	if got := requests.Load(); got != before {
		t.Errorf("PrepareBinding must be side-effect-free: %d requests dispatched", got-before)
	}
}

func TestPrepareBindingNilWhenSatisfiedOrUndeclared(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	if d, _ := binv.PrepareBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  httpSource(srv),
		Ref:     "#/operations/sendMessage",
		Context: map[string]any{"bearerToken": testSecret},
	}); d != nil {
		t.Errorf("satisfied context: expected nil, got %+v", d)
	}

	if d, _ := binv.PrepareBinding(bg(), &invoke.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendOpenMessage",
	}); d != nil {
		t.Errorf("no declared security: expected nil, got %+v", d)
	}
}

func TestPrepareBindingNeverFetches(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	specURL := srv.URL + "/spec.json"

	// Cold cache + location-only source: not knowable without I/O -> nil.
	cold := NewInvoker()
	defer cold.Close()
	before := requests.Load()
	if d, err := cold.PrepareBinding(bg(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Location: specURL},
		Ref:    "#/operations/sendMessage",
	}); err != nil || d != nil {
		t.Fatalf("cold cache: expected (nil, nil), got (%+v, %v)", d, err)
	}
	if got := requests.Load(); got != before {
		t.Fatalf("PrepareBinding must never fetch: %d requests dispatched", got-before)
	}

	// Warm the cache through a real invocation, then preflight answers.
	call := cold.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  invoke.InvocationSource{BindingSpec: BindingSpec, Location: specURL},
		Ref:     "#/operations/sendMessage",
		Context: map[string]any{"bearerToken": testSecret},
	})
	if err := call.Write(bg(), map[string]any{"text": "warm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke.Single(shortCtx(t), call.Outputs()); err != nil {
		t.Fatal(err)
	}

	warmBefore := requests.Load()
	d, err := cold.PrepareBinding(bg(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Location: specURL},
		Ref:    "#/operations/sendMessage",
	})
	if err != nil || d == nil {
		t.Fatalf("warm cache: expected details, got (%+v, %v)", d, err)
	}
	if got := requests.Load(); got != warmBefore {
		t.Errorf("warm-cache preflight must not fetch: %d requests dispatched", got-warmBefore)
	}
}

// ---------------------------------------------------------------------------
// End-to-end through the operation invoker (resolve-and-retry)
// ---------------------------------------------------------------------------

func TestOperationInvokerResolvesChallengeFromStore(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	store := testStore{}
	host := strings.TrimPrefix(srv.URL, "http://")
	if err := store.Set(bg(), host, map[string]any{"bearerToken": testSecret}); err != nil {
		t.Fatal(err)
	}

	op := invoke.NewOperationInvoker(binv)
	op.ContextResolver = invoke.StoreContextResolver(store)

	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			// The input schema matters: an operation without one is a
			// no-input operation under the operation-layer convention.
			"sendMessage": {Input: map[string]any{"type": "object"}},
		},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: {BindingSpec: BindingSpec, Content: mustContent(makeAsyncAPISpec(srv.URL))},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"sendMessage." + DefaultSourceName: {
				Operation: "sendMessage", Source: DefaultSourceName, Ref: "#/operations/sendMessage",
			},
		},
	}

	call := invoke.Invoke(bg(), op, iface,
		invoke.NewOperationSignature[any, any]("sendMessage"))
	if err := call.Write(bg(), map[string]any{"text": "negotiated"}); err != nil {
		t.Fatal(err)
	}
	v, err := invoke.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	received, _ := v.(map[string]any)["received"].(map[string]any)
	if received["text"] != "negotiated" {
		t.Fatalf("got %v", v)
	}
}

// ---------------------------------------------------------------------------
// WebSocket fixture
// ---------------------------------------------------------------------------

// makeWSAsyncAPISpec returns an AsyncAPI 3.x document configured with a
// WebSocket server, given the host:port of an httptest.Server. scheme nil
// means no declared security.
func makeWSAsyncAPISpec(httpURL string, scheme *securityScheme) *document {
	host := strings.TrimPrefix(strings.TrimPrefix(httpURL, "http://"), "https://")
	srv := server{Host: host, Protocol: "ws"}
	doc := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "WS Test API", Version: "1.0.0"},
		Channels: map[string]channel{
			"stream": {Address: "/ws"},
		},
		Operations: map[string]asyncOperation{
			// Names describe the invoker's role; actions are the artifact's
			// (app-perspective, ASYNC-P-02): invoking `send` subscribes,
			// invoking `receive` publishes.
			"subscribe": {Action: "send", Channel: channelRef{Ref: "#/channels/stream"}, Messages: []messageRef{{Ref: "#/components/messages/json"}}},
			"publish":   {Action: "receive", Channel: channelRef{Ref: "#/channels/stream"}, Messages: []messageRef{{Ref: "#/components/messages/json"}}},
		},
		Components: &components{
			Messages: map[string]message{"json": {Name: "json", ContentType: "application/json"}},
		},
	}
	if scheme != nil {
		srv.Security = []securityRequirement{{Ref: "#/components/securitySchemes/auth"}}
		doc.Components.SecuritySchemes = map[string]securityScheme{"auth": *scheme}
	}
	doc.Servers = map[string]server{"wsServer": srv}
	return doc
}

// wsTestServer returns an httptest.Server that upgrades GET /ws to a
// WebSocket and dispatches to the supplied exchange function.
func wsTestServer(t *testing.T, exchange func(ctx context.Context, conn *websocket.Conn, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("websocket accept failed: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "test done")
		exchange(r.Context(), conn, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readWSJSON reads a single WebSocket text message and decodes it as JSON.
func readWSJSON(ctx context.Context, conn *websocket.Conn) (map[string]any, error) {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, raw, err := conn.Read(readCtx)
	if err != nil {
		return nil, err
	}
	var msg map[string]any
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// writeWSJSON writes a JSON-encoded message to a WebSocket as a text frame.
func writeWSJSON(ctx context.Context, conn *websocket.Conn, msg any) error {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(writeCtx, websocket.MessageText, raw)
}

func wsSource(srv *httptest.Server, scheme *securityScheme) invoke.InvocationSource {
	return invoke.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(makeWSAsyncAPISpec(srv.URL, scheme))}
}

func TestOpenBindingsBridgePreservesWebSocketReplyValues(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, _ *http.Request) {
		value, err := readWSJSON(ctx, conn)
		if err != nil {
			return
		}
		_ = writeWSJSON(ctx, conn, map[string]any{"accepted": value["id"]})
		_ = conn.Close(websocket.StatusNormalClosure, "done")
	})
	host := strings.TrimPrefix(srv.URL, "http://")
	document := map[string]any{
		"asyncapi":           "3.1.0",
		"info":               map[string]any{"title": "Reply bridge", "version": "1"},
		"defaultContentType": "application/json",
		"servers":            map[string]any{"test": map[string]any{"host": host, "protocol": "ws"}},
		"channels": map[string]any{"commands": map[string]any{
			"address": "/ws",
			"messages": map[string]any{
				"Command": map[string]any{"payload": map[string]any{"type": "object"}},
				"Result":  map[string]any{"payload": map[string]any{"type": "object"}},
			},
		}},
		"operations": map[string]any{"submit": map[string]any{
			"action":   "receive",
			"channel":  map[string]any{"$ref": "#/channels/commands"},
			"messages": []any{map[string]any{"$ref": "#/channels/commands/messages/Command"}},
			"reply": map[string]any{
				"channel":  map[string]any{"$ref": "#/channels/commands"},
				"messages": []any{map[string]any{"$ref": "#/channels/commands/messages/Result"}},
			},
		}},
	}
	invoker := NewInvoker()
	defer invoker.Close()
	call := invoker.InvokeBinding(shortCtx(t), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(document)},
		Ref:    "#/operations/submit", Context: wsTextContext(nil),
	})
	if err := call.Write(shortCtx(t), map[string]any{"id": 91}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	values, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].(map[string]any)["accepted"] != float64(91) {
		t.Fatalf("outputs = %#v", values)
	}
}

// ---------------------------------------------------------------------------
// WebSocket receive
// ---------------------------------------------------------------------------

// TestWebSocketBearerRidesUpgradeRequest verifies §9.5 (ASYNC-P-07): for
// WebSocket connections a declared bearer credential rides the upgrade
// request. A server-streaming subscription has no input lane, so credentials
// cannot be volunteered as an in-band message.
func TestWebSocketBearerRidesUpgradeRequest(t *testing.T) {
	authCh := make(chan string, 1)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		authCh <- r.Header.Get("Authorization")
		_ = writeWSJSON(ctx, conn, map[string]any{"hello": true})
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, &securityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "test-bearer-xyz"},
	})
	out := call.Outputs()
	first, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := first.(map[string]any)
	if got["hello"] != true {
		t.Fatalf("first server event = %v, want hello", got)
	}
	select {
	case auth := <-authCh:
		if auth != "Bearer test-bearer-xyz" {
			t.Errorf("upgrade Authorization = %q, want %q", auth, "Bearer test-bearer-xyz")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw the upgrade")
	}
	out.Stop()
}

// TestWebSocketNoInventedAuthWithoutDeclaredScheme verifies an undeclared
// bearer token is not volunteered onto the upgrade request.
func TestWebSocketNoInBandAuthWithoutDeclaredScheme(t *testing.T) {
	authCh := make(chan string, 1)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		authCh <- r.Header.Get("Authorization")
		_ = writeWSJSON(ctx, conn, map[string]any{"hello": true})
	})

	binv := NewInvoker()
	defer binv.Close()

	// No declared security (wsSource scheme nil) + bearerToken in context.
	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, nil),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "secret-tok"},
	})
	out := call.Outputs()
	first, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := first.(map[string]any)
	if got["hello"] != true {
		t.Fatalf("first server event = %v, want hello", got)
	}
	select {
	case auth := <-authCh:
		if auth != "" {
			t.Fatalf("undeclared bearer token reached upgrade Authorization: %q", auth)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw the upgrade")
	}
	out.Stop()
}

// TestWebSocketQueryParamApiKey is the regression test for query-param
// credentials populated by applyHTTPContext reaching the dialed WebSocket
// URL (browsers cannot set upgrade headers, so a query apiKey is the only
// way to authenticate a browser WebSocket).
func TestWebSocketQueryParamApiKey(t *testing.T) {
	keyCh := make(chan string, 1)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		keyCh <- r.URL.Query().Get("api_key")
		_ = writeWSJSON(ctx, conn, map[string]any{"ok": true})
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, &securityScheme{Type: "apiKey", In: "query", Name: "api_key"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"apiKey": "secret-key-abc"},
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 event, got %d", len(vals))
	}
	select {
	case key := <-keyCh:
		if key != "secret-key-abc" {
			t.Errorf("query-param apiKey not propagated: got %q, want secret-key-abc", key)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw the upgrade")
	}
}

// TestWebSocketQueryParamApiKey_NamedViaApiKeysMap verifies the R2.d ruling:
// an apiKey scheme's credential resolves from the scheme-scoped
// context.apiKeys[name] entry (name = the securitySchemes key,
// makeWSAsyncAPISpec always names it "auth") when the plain "apiKey"
// convenience field is absent — the same wire placement as
// TestWebSocketQueryParamApiKey, reached through the named lookup instead.
func TestWebSocketQueryParamApiKey_NamedViaApiKeysMap(t *testing.T) {
	keyCh := make(chan string, 1)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		keyCh <- r.URL.Query().Get("api_key")
		_ = writeWSJSON(ctx, conn, map[string]any{"ok": true})
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, &securityScheme{Type: "apiKey", In: "query", Name: "api_key"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"apiKeys": map[string]any{"auth": "named-secret-xyz"}},
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 event, got %d", len(vals))
	}
	select {
	case key := <-keyCh:
		if key != "named-secret-xyz" {
			t.Errorf("named apiKeys entry not propagated: got %q, want named-secret-xyz", key)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw the upgrade")
	}
}

// TestWebSocketStreamingMultipleEvents verifies each server-pushed frame is
// one bare output in arrival order, and a clean server close ends the stream.
func TestWebSocketStreamingMultipleEvents(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		for i := 1; i <= 5; i++ {
			_ = writeWSJSON(ctx, conn, map[string]any{"seq": i})
		}
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, &securityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "tok"},
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 5 {
		t.Fatalf("expected 5 events, got %d: %v", len(vals), vals)
	}
	for i, v := range vals {
		if seq, _ := v.(map[string]any)["seq"].(float64); int(seq) != i+1 {
			t.Errorf("event %d: seq = %v, want %d", i, v, i+1)
		}
	}
}

// TestWebSocketStopCancelsSubscription verifies that abandoning the output
// stream cancels the invocation without stranding goroutines.
func TestWebSocketStopCancelsSubscription(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		_ = writeWSJSON(ctx, conn, map[string]any{"id": "1"})
		<-ctx.Done() // hold the connection open; the client cancels
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, &securityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "tok"},
	})
	out := call.Outputs()
	if _, err := out.Read(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	out.Stop()
	_, err := out.Read(shortCtx(t))
	if codeOf(t, err) != invoke.ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", err)
	}
}

// TestWebSocketServerErrorFrame verifies an {"error": ...} frame is a
// terminal ERR_STREAM_ERROR carrying the server's message.
func TestWebSocketServerErrorFrame(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		_ = writeWSJSON(ctx, conn, map[string]any{"data": map[string]any{"ok": true}})
		_ = writeWSJSON(ctx, conn, map[string]any{"error": map[string]any{"message": "boom"}})
	})

	binv := NewInvoker()
	defer binv.Close()

	// The {"data"}/{"error"} convention left the builtin (round-4
	// unification): a consumer whose stream speaks it attaches an
	// OutputDecoder — a returned error is terminal, which IS the override
	// channel for error-frame conventions.
	conventionDecoder := func(_ invoke.InvokeSite, raw invoke.RawResult) (any, error) {
		var parsed map[string]any
		if err := json.Unmarshal(raw.Body, &parsed); err != nil {
			return nil, err
		}
		if errVal, has := parsed["error"]; has && errVal != nil {
			return nil, invoke.NewInvocationErrorWithData(
				invoke.ErrCodeStreamError,
				map[string]any{"error": errVal},
			)
		}
		if dataVal, has := parsed["data"]; has {
			return dataVal, nil
		}
		return parsed, nil
	}
	args := &invoke.BindingInvocationArgs{
		Source:  wsSource(srv, &securityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "tok"},
	}
	// Hooks ride the args on the binding-layer path (what the operation
	// invoker's fill does for embedders).
	hooked := invoke.NewOperationInvoker(binv)
	hooked.OutputDecoder = conventionDecoder
	call := hooked.InvokeBinding(bg(), args)
	vals, err := drainOutputs(t, call)
	if len(vals) != 1 {
		t.Fatalf("the {data} frame before the error must be delivered, got %v", vals)
	}
	if vals[0].(map[string]any)["ok"] != true {
		t.Fatalf("data frame must unwrap to its payload, got %v", vals[0])
	}
	var ie *invoke.InvocationError
	if !errors.As(err, &ie) || ie.Code != invoke.ErrCodeStreamError {
		t.Fatalf("expected ERR_STREAM_ERROR, got %v", err)
	}
	if !ie.HasData() || !reflect.DeepEqual(ie.Data, map[string]any{"error": map[string]any{"message": "boom"}}) {
		t.Errorf("Data = %#v, want application-authored error value", ie.Data)
	}
}

// TestWebSocketSubscriptionHasNoInput verifies the documented server-stream
// boundary: once established, a `send` operation accepts no caller values.
func TestWebSocketSubscriptionHasNoInput(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		_ = writeWSJSON(ctx, conn, map[string]any{"ready": true})
		<-ctx.Done()
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: wsSource(srv, nil),
		Ref:    "#/operations/subscribe",
	})

	out := call.Outputs()
	ready, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if ready.(map[string]any)["ready"] != true {
		t.Fatalf("got %v", ready)
	}
	if err := call.Write(bg(), map[string]any{"not": "admitted"}); err == nil {
		t.Fatal("server-streaming subscription accepted an input value")
	}
	out.Stop()
}

// TestNewInvokerWithClient verifies that an Invoker created with a custom
// HTTP client uses that client for outbound requests.
func TestNewInvokerWithClient(t *testing.T) {
	var requestCount int
	custom := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"received":true}`)),
			Request:    req,
		}, nil
	})}
	binv := NewInvokerWithClient(custom)
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(makeAsyncAPISpec("http://example.test"))},
		Ref:    "#/operations/sendOpenMessage",
	})
	if err := call.Write(bg(), map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	outs, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if requestCount != 1 {
		t.Errorf("expected custom transport to be called exactly once, got %d", requestCount)
	}
	if len(outs) != 1 {
		t.Fatalf("expected one output through the custom client, got %d", len(outs))
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
