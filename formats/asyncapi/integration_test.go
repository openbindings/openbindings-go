package asyncapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

const testSecret = "test-token-123"

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
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %T: %v", err, err)
	}
	return ie.Code
}

// drainOutputs reads the invocation's outputs to EOF or terminal error.
func drainOutputs(t *testing.T, call openbindings.Invocation[any, any]) ([]any, error) {
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
// HTTP fixture: unary send + SSE receive
// ---------------------------------------------------------------------------

// makeAsyncAPISpec builds the HTTP test document. Bearer security is declared
// per-operation (sendMessage, receiveEvents); sendOpenMessage, sendAck, and
// receiveStream carry no security so server-side failures and cancellation
// can be exercised without tripping the CONTEXT_REQUIRED gate.
func makeAsyncAPISpec(baseURL string) *Document {
	return &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test API", Version: "1.0.0"},
		Servers: map[string]Server{
			"test": {
				Host:     strings.TrimPrefix(strings.TrimPrefix(baseURL, "http://"), "https://"),
				Protocol: "http",
			},
		},
		Channels: map[string]Channel{
			"messages": {Address: "/messages"},
			"events":   {Address: "/events"},
			"stream":   {Address: "/stream"},
			"ack":      {Address: "/ack"},
		},
		Operations: map[string]Operation{
			"sendMessage": {
				Action:   "send",
				Channel:  ChannelRef{Ref: "#/channels/messages"},
				Security: []map[string][]string{{"bearer": {}}},
			},
			"sendOpenMessage": {Action: "send", Channel: ChannelRef{Ref: "#/channels/messages"}},
			"sendAck":         {Action: "send", Channel: ChannelRef{Ref: "#/channels/ack"}},
			"receiveEvents": {
				Action:   "receive",
				Channel:  ChannelRef{Ref: "#/channels/events"},
				Security: []map[string][]string{{"bearer": {}}},
			},
			"receiveStream": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/stream"}},
		},
		Components: &Components{
			SecuritySchemes: map[string]SecurityScheme{
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

func httpSource(srv *httptest.Server) openbindings.InvocationSource {
	return openbindings.InvocationSource{Format: FormatToken, Content: makeAsyncAPISpec(srv.URL)}
}

// ---------------------------------------------------------------------------
// CONTEXT_REQUIRED negotiation
// ---------------------------------------------------------------------------

func TestContextRequiredBeforeAnyIO(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	before := requests.Load()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendMessage",
	})
	if err := call.Write(bg(), map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)

	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %v", err)
	}
	details := openbindings.ContextRequiredFrom(ie)
	if details == nil {
		t.Fatalf("expected CONTEXT_REQUIRED with details, got %v", err)
	}
	wantKey := strings.TrimPrefix(srv.URL, "http://")
	if details.Key != wantKey {
		t.Errorf("Key = %q, want %q", details.Key, wantKey)
	}
	if len(details.Alternatives) != 1 || details.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Errorf("alternatives = %+v, want one auth.bearer requirement", details.Alternatives)
	}
	if got := requests.Load(); got != before {
		t.Errorf("challenge must precede any I/O: %d requests dispatched", got-before)
	}
}

func TestContextRequiredOnReceive(t *testing.T) {
	srv, requests := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	before := requests.Load()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/receiveEvents",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", err)
	}
	if got := requests.Load(); got != before {
		t.Errorf("challenge must precede any I/O: %d requests dispatched", got-before)
	}
}

// ---------------------------------------------------------------------------
// Unary send over HTTP
// ---------------------------------------------------------------------------

func TestUnarySendAppliesBearerAndYieldsResponse(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  httpSource(srv),
		Ref:     "#/operations/sendMessage",
		Context: map[string]any{"bearerToken": testSecret},
	})
	if err := call.Write(bg(), map[string]any{"text": "hello"}); err != nil {
		t.Fatal(err)
	}

	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	received, _ := v.(map[string]any)["received"].(map[string]any)
	if received["text"] != "hello" {
		t.Fatalf("got %v", v)
	}

	md, err := call.Header(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(md["content-type"]) != 1 || md["content-type"][0] != "application/json" {
		t.Errorf("header content-type = %v", md["content-type"])
	}
}

func TestServer401MapsToAuthRequired(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	// sendOpenMessage declares no security, so the request dispatches and the
	// server's 401 surfaces operationally.
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendOpenMessage",
	})
	if err := call.Write(bg(), map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) || ie.Code != openbindings.ErrCodeAuthRequired {
		t.Fatalf("expected ERR_AUTH_REQUIRED, got %v", err)
	}
	details, _ := ie.Details.(map[string]any)
	if details["status"] != 401 {
		t.Errorf("details.status = %v, want 401", details["status"])
	}
	body, _ := details["body"].(map[string]any)
	if body["error"] != "unauthorized" {
		t.Errorf("details.body = %v", details["body"])
	}
}

func TestMissingInputOnSend(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendOpenMessage",
	})
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeMissingInput {
		t.Fatalf("expected ERR_MISSING_INPUT, got %v", err)
	}
}

// TestNoInputOperationConvention_HTTPSend verifies the operation-layer
// no-input convention: a call carrying Binding != nil and InputSchema == nil
// sends one empty-object message without waiting for a write — the caller of
// a no-input operation never writes nor closes.
func TestNoInputOperationConvention_HTTPSend(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  httpSource(srv),
		Ref:     "#/operations/sendAck",
		Binding: &openbindings.BindingEntry{Operation: "sendAck", Source: DefaultSourceName, Ref: "#/operations/sendAck"},
		// InputSchema nil → no-input operation; the binding closes input itself.
	})
	// The caller writes nothing and does not close; the binding must still
	// dispatch (drainOutputs reads under a 5s timeout, so a parked binding
	// fails the test instead of hanging it).
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("no-input convention failed: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("a 202 publish ack is not an output, got %v", vals)
	}
}

// TestNoInputOperationConvention_WSSend is the WebSocket variant: the binding
// publishes one empty-object frame and completes with zero outputs.
func TestNoInputOperationConvention_WSSend(t *testing.T) {
	var upgrades atomic.Int32
	frames := make(chan map[string]any, 4)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		for {
			msg, err := readWSJSON(ctx, conn)
			if err != nil {
				return
			}
			frames <- msg
		}
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, nil),
		Ref:     "#/operations/publish",
		Binding: &openbindings.BindingEntry{Operation: "publish", Source: DefaultSourceName, Ref: "#/operations/publish"},
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("no-input convention failed: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("a ws publish yields no outputs, got %v", vals)
	}
	select {
	case f := <-frames:
		if len(f) != 0 {
			t.Errorf("expected one empty-object frame, got %v", f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the empty-object frame")
	}
}

func TestSendAckYieldsZeroOutputs(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
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

// ---------------------------------------------------------------------------
// SSE receive
// ---------------------------------------------------------------------------

func TestSSEReceiveStreamsBareOutputs(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  httpSource(srv),
		Ref:     "#/operations/receiveEvents",
		Context: map[string]any{"bearerToken": testSecret},
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 events, got %d: %v", len(vals), vals)
	}
	for i, want := range []float64{1, 2} {
		if got := vals[i].(map[string]any)["seq"]; got != want {
			t.Errorf("event %d: seq = %v, want %v", i, got, want)
		}
	}
}

// sseEventDoc builds a minimal doc with one receive op (no security) whose
// channel address points at the given path.
func sseEventDoc(baseURL, path string) *Document {
	return &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "SSE Cap Test", Version: "1.0.0"},
		Servers: map[string]Server{
			"test": {Host: strings.TrimPrefix(baseURL, "http://"), Protocol: "http"},
		},
		Channels:   map[string]Channel{"caps": {Address: path}},
		Operations: map[string]Operation{"receiveCaps": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/caps"}}},
	}
}

// TestSSEReceiveCapIsPerEvent verifies the size cap applies per event, not
// cumulatively: a long-lived subscription streaming more than the cap in
// total keeps flowing.
func TestSSEReceiveCapIsPerEvent(t *testing.T) {
	const eventSize = 2 * 1024 * 1024 // 2 MB per event, 6 events = 12 MB total
	payload := strings.Repeat("x", eventSize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 6; i++ {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: sseEventDoc(srv.URL, "/")},
		Ref:    "#/operations/receiveCaps",
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("a >10MB cumulative stream of small events must not error: %v", err)
	}
	if len(vals) != 6 {
		t.Fatalf("expected 6 events, got %d", len(vals))
	}
}

// TestSSEReceiveSingleOversizedEventErrors verifies a single event larger
// than the cap terminates with ERR_RESPONSE_ERROR.
func TestSSEReceiveSingleOversizedEventErrors(t *testing.T) {
	payload := strings.Repeat("x", maxResponseBytes+16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", payload)
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: sseEventDoc(srv.URL, "/")},
		Ref:    "#/operations/receiveCaps",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeResponseError {
		t.Fatalf("expected ERR_RESPONSE_ERROR for an oversized event, got %v", err)
	}
}

func TestStopCancelsLiveSubscription(t *testing.T) {
	srv, _ := newHTTPFixture(t)
	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/receiveStream",
	})
	out := call.Outputs()
	first, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["tick"] != float64(1) {
		t.Fatalf("got %v", first)
	}

	out.Stop()
	_, err = out.Read(shortCtx(t))
	if codeOf(t, err) != openbindings.ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", err)
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
		args *openbindings.BindingInvocationArgs
		code string
	}{
		{"unknown operation", &openbindings.BindingInvocationArgs{
			Source: httpSource(srv), Ref: "#/operations/nope",
		}, openbindings.ErrCodeRefNotFound},
		{"empty ref", &openbindings.BindingInvocationArgs{
			Source: httpSource(srv), Ref: "",
		}, openbindings.ErrCodeInvalidRef},
		{"unparsable source", &openbindings.BindingInvocationArgs{
			Source: openbindings.InvocationSource{Format: FormatToken, Content: "not asyncapi"},
			Ref:    "#/operations/sendMessage",
		}, openbindings.ErrCodeSourceLoadFailed},
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
	details, err := binv.PrepareBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: httpSource(srv),
		Ref:    "#/operations/sendMessage",
	})
	if err != nil {
		t.Fatal(err)
	}
	if details == nil {
		t.Fatal("expected details")
	}
	wantKey := strings.TrimPrefix(srv.URL, "http://")
	if details.Key != wantKey {
		t.Errorf("Key = %q, want %q", details.Key, wantKey)
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

	if d, _ := binv.PrepareBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  httpSource(srv),
		Ref:     "#/operations/sendMessage",
		Context: map[string]any{"bearerToken": testSecret},
	}); d != nil {
		t.Errorf("satisfied context: expected nil, got %+v", d)
	}

	if d, _ := binv.PrepareBinding(bg(), &openbindings.BindingInvocationArgs{
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
	if d, err := cold.PrepareBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Location: specURL},
		Ref:    "#/operations/sendMessage",
	}); err != nil || d != nil {
		t.Fatalf("cold cache: expected (nil, nil), got (%+v, %v)", d, err)
	}
	if got := requests.Load(); got != before {
		t.Fatalf("PrepareBinding must never fetch: %d requests dispatched", got-before)
	}

	// Warm the cache through a real invocation, then preflight answers.
	call := cold.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Location: specURL},
		Ref:     "#/operations/sendMessage",
		Context: map[string]any{"bearerToken": testSecret},
	})
	if err := call.Write(bg(), map[string]any{"text": "warm"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openbindings.Single(shortCtx(t), call.Outputs()); err != nil {
		t.Fatal(err)
	}

	warmBefore := requests.Load()
	d, err := cold.PrepareBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Location: specURL},
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

	store := openbindings.NewMemoryStore()
	host := strings.TrimPrefix(srv.URL, "http://")
	if err := store.Set(bg(), host, map[string]any{"bearerToken": testSecret}); err != nil {
		t.Fatal(err)
	}

	op := openbindings.NewOperationInvoker(binv)
	op.ContextResolver = openbindings.StoreContextResolver(store)

	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			// The input schema matters: an operation without one is a
			// no-input operation under the operation-layer convention.
			"sendMessage": {Input: map[string]any{"type": "object"}},
		},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: {Format: FormatToken, Content: makeAsyncAPISpec(srv.URL)},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"sendMessage." + DefaultSourceName: {
				Operation: "sendMessage", Source: DefaultSourceName, Ref: "#/operations/sendMessage",
			},
		},
	}

	call := op.Invoke(bg(), &openbindings.OperationInvocationArgs{
		Interface: iface,
		Operation: "sendMessage",
	})
	if err := call.Write(bg(), map[string]any{"text": "negotiated"}); err != nil {
		t.Fatal(err)
	}
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
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
func makeWSAsyncAPISpec(httpURL string, scheme *SecurityScheme) *Document {
	host := strings.TrimPrefix(strings.TrimPrefix(httpURL, "http://"), "https://")
	server := Server{Host: host, Protocol: "ws"}
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "WS Test API", Version: "1.0.0"},
		Channels: map[string]Channel{
			"stream": {Address: "/ws"},
		},
		Operations: map[string]Operation{
			"subscribe": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/stream"}},
			"publish":   {Action: "send", Channel: ChannelRef{Ref: "#/channels/stream"}},
		},
	}
	if scheme != nil {
		server.Security = []map[string][]string{{"auth": {}}}
		doc.Components = &Components{
			SecuritySchemes: map[string]SecurityScheme{"auth": *scheme},
		}
	}
	doc.Servers = map[string]Server{"wsServer": server}
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

func wsSource(srv *httptest.Server, scheme *SecurityScheme) openbindings.InvocationSource {
	return openbindings.InvocationSource{Format: FormatToken, Content: makeWSAsyncAPISpec(srv.URL, scheme)}
}

// ---------------------------------------------------------------------------
// WebSocket receive
// ---------------------------------------------------------------------------

// TestWebSocketBearerInFirstFrame verifies the WebSocket auth convention:
// the bearer token is sent in the body of the first frame after the upgrade,
// because browsers cannot set custom WebSocket upgrade headers.
func TestWebSocketBearerInFirstFrame(t *testing.T) {
	tokenCh := make(chan string, 1)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		first, err := readWSJSON(ctx, conn)
		if err != nil {
			return
		}
		tok, _ := first["bearerToken"].(string)
		tokenCh <- tok
		_ = writeWSJSON(ctx, conn, map[string]any{"id": "1", "msg": "first"})
		_ = writeWSJSON(ctx, conn, map[string]any{"id": "2", "msg": "second"})
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "test-bearer-xyz"},
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 stream events, got %d: %v", len(vals), vals)
	}
	select {
	case tok := <-tokenCh:
		if tok != "test-bearer-xyz" {
			t.Errorf("server received bearerToken = %q, want test-bearer-xyz", tok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the first frame")
	}
}

// TestWebSocketBearerFirstFrameRequiresDeclaredScheme verifies the gating of
// the first-frame bearer convention: with no bearer-family scheme declared,
// the token must NOT be volunteered into the message stream. The server
// echoes every frame back, so the first output proves which frame arrived
// first.
func TestWebSocketBearerFirstFrameRequiresDeclaredScheme(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		for {
			msg, err := readWSJSON(ctx, conn)
			if err != nil {
				return
			}
			if err := writeWSJSON(ctx, conn, map[string]any{"data": msg}); err != nil {
				return
			}
		}
	})

	binv := NewInvoker()
	defer binv.Close()

	// No declared security (wsSource scheme nil) + bearerToken in context.
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, nil),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "secret-tok"},
	})
	out := call.Outputs()
	if err := call.Write(bg(), map[string]any{"hello": true}); err != nil {
		t.Fatal(err)
	}
	first, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := first.(map[string]any)
	if _, leaked := got["bearerToken"]; leaked {
		t.Fatal("bearer token volunteered without a declared bearer scheme")
	}
	if got["hello"] != true {
		t.Fatalf("first echoed frame = %v, want the hello control frame", got)
	}
	out.Stop()
}

// TestWebSocketBearerFirstFrameOncePerConnection verifies the auth frame is
// sent once per pooled connection, not once per invocation reusing it.
func TestWebSocketBearerFirstFrameOncePerConnection(t *testing.T) {
	var upgrades atomic.Int32
	frames := make(chan map[string]any, 16)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		for {
			msg, err := readWSJSON(ctx, conn)
			if err != nil {
				return
			}
			frames <- msg
		}
	})

	binv := NewInvoker()
	defer binv.Close()
	source := wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"})
	bindCtx := map[string]any{"bearerToken": "tok"}

	sub1 := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: source, Ref: "#/operations/subscribe", Context: bindCtx,
	})
	if err := sub1.Write(bg(), map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	got := collectFrames(t, frames, 2) // auth frame, then n=1
	if tok, _ := got[0]["bearerToken"].(string); tok != "tok" {
		t.Fatalf("first frame on a fresh socket must be the auth frame, got %v", got[0])
	}
	sub1.Outputs().Stop()

	// Second subscription reuses the pooled (already authenticated) socket:
	// no second auth frame may precede its control frame.
	sub2 := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: source, Ref: "#/operations/subscribe", Context: bindCtx,
	})
	if err := sub2.Write(bg(), map[string]any{"n": 2}); err != nil {
		t.Fatal(err)
	}
	next := collectFrames(t, frames, 1)
	if _, hasToken := next[0]["bearerToken"]; hasToken {
		t.Fatal("auth frame re-sent on a reused connection")
	}
	if n, _ := next[0]["n"].(float64); n != 2 {
		t.Fatalf("frame = %v, want the n=2 control frame", next[0])
	}
	sub2.Outputs().Stop()

	if c := upgrades.Load(); c != 1 {
		t.Errorf("expected both subscriptions to share 1 upgrade, got %d", c)
	}
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

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, &SecurityScheme{Type: "apiKey", In: "query", Name: "api_key"}),
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

// TestWebSocketStreamingMultipleEvents verifies each server-pushed frame is
// one bare output in arrival order, and a clean server close ends the stream.
func TestWebSocketStreamingMultipleEvents(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		_, _ = readWSJSON(ctx, conn) // bearer first-frame
		for i := 1; i <= 5; i++ {
			_ = writeWSJSON(ctx, conn, map[string]any{"seq": i})
		}
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"}),
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
		_, _ = readWSJSON(ctx, conn)
		_ = writeWSJSON(ctx, conn, map[string]any{"id": "1"})
		<-ctx.Done() // hold the connection open; the client cancels
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "tok"},
	})
	out := call.Outputs()
	if _, err := out.Read(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	out.Stop()
	_, err := out.Read(shortCtx(t))
	if codeOf(t, err) != openbindings.ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", err)
	}
}

// TestWebSocketServerErrorFrame verifies an {"error": ...} frame is a
// terminal ERR_STREAM_ERROR carrying the server's message.
func TestWebSocketServerErrorFrame(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		_, _ = readWSJSON(ctx, conn)
		_ = writeWSJSON(ctx, conn, map[string]any{"data": map[string]any{"ok": true}})
		_ = writeWSJSON(ctx, conn, map[string]any{"error": map[string]any{"message": "boom"}})
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source:  wsSource(srv, &SecurityScheme{Type: "http", Scheme: "bearer"}),
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "tok"},
	})
	vals, err := drainOutputs(t, call)
	if len(vals) != 1 {
		t.Fatalf("the {data} frame before the error must be delivered, got %v", vals)
	}
	if vals[0].(map[string]any)["ok"] != true {
		t.Fatalf("data frame must unwrap to its payload, got %v", vals[0])
	}
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) || ie.Code != openbindings.ErrCodeStreamError {
		t.Fatalf("expected ERR_STREAM_ERROR, got %v", err)
	}
	if ie.Message != "boom" {
		t.Errorf("Message = %q, want boom", ie.Message)
	}
}

// TestWebSocketBidiControlFrames verifies the receive binding is
// bidi-capable: caller inputs are forwarded as socket frames, and closing
// input does NOT end the subscription.
func TestWebSocketBidiControlFrames(t *testing.T) {
	inputClosed := make(chan struct{})
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		control, err := readWSJSON(ctx, conn) // no bearer context: first frame is the control frame
		if err != nil {
			return
		}
		_ = writeWSJSON(ctx, conn, map[string]any{"ack": control["subscribe"]})
		select {
		case <-inputClosed:
		case <-ctx.Done():
			return
		}
		_ = writeWSJSON(ctx, conn, map[string]any{"final": true})
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: wsSource(srv, nil),
		Ref:    "#/operations/subscribe",
	})

	if err := call.Write(bg(), map[string]any{"subscribe": "orders"}); err != nil {
		t.Fatal(err)
	}
	out := call.Outputs()
	ack, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if ack.(map[string]any)["ack"] != "orders" {
		t.Fatalf("got %v", ack)
	}

	// Close the input side; the subscription must keep flowing.
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	close(inputClosed)

	final, err := out.Read(shortCtx(t))
	if err != nil {
		t.Fatalf("closing input must not end the subscription: %v", err)
	}
	if final.(map[string]any)["final"] != true {
		t.Fatalf("got %v", final)
	}
	if _, err := out.Read(shortCtx(t)); err != io.EOF {
		t.Fatalf("expected clean EOF after server close, got %v", err)
	}
}
