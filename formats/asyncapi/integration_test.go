package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

const testSecret = "test-token-123"

func makeAsyncAPISpec(baseURL string) *Document {
	return &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "Test API", Version: "1.0.0"},
		Servers: map[string]Server{
			"test": {
				Host:     strings.TrimPrefix(strings.TrimPrefix(baseURL, "http://"), "https://"),
				Protocol: "http",
				Security: []map[string][]string{{"bearer": {}}},
			},
		},
		Channels: map[string]Channel{
			"messages": {Address: "/messages"},
			"events":   {Address: "/events"},
		},
		Operations: map[string]Operation{
			"sendMessage":   {Action: "send", Channel: ChannelRef{Ref: "#/channels/messages"}},
			"receiveEvents": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/events"}},
		},
		Components: &Components{
			SecuritySchemes: map[string]SecurityScheme{
				"bearer": {Type: "http", Scheme: "bearer"},
			},
		},
	}
}

func testHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/messages" && r.Method == "POST" {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"received": body})
		return
	}

	if r.URL.Path == "/events" && r.Method == "GET" {
		if r.Header.Get("Authorization") != "Bearer "+testSecret {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			w.WriteHeader(500)
			return
		}
		fmt.Fprintf(w, "data: {\"msg\":\"hello\"}\n\n")
		flusher.Flush()
		fmt.Fprintf(w, "data: {\"msg\":\"world\"}\n\n")
		flusher.Flush()
		return
	}

	w.WriteHeader(404)
}

func drainStream(ch <-chan openbindings.StreamEvent) []openbindings.StreamEvent {
	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func TestIntegration_SendNoCredentials401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(testHandler))
	defer srv.Close()

	doc := makeAsyncAPISpec(srv.URL)
	store := openbindings.NewMemoryStore()
	ctx := context.Background()

	exec := NewExecutor()
	opExec := openbindings.NewOperationExecutor(exec)
	opExec.ContextStore = store

	client := openbindings.NewInterfaceClient(nil, opExec,
		openbindings.WithContextStore(store),
	)
	client.ResolveInterface(&openbindings.Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]openbindings.Operation{
			"sendMessage":   {},
			"receiveEvents": {},
		},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: {Format: FormatToken, Content: doc},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"sendMessage." + DefaultSourceName:   {Operation: "sendMessage", Source: DefaultSourceName, Ref: "#/operations/sendMessage"},
			"receiveEvents." + DefaultSourceName: {Operation: "receiveEvents", Source: DefaultSourceName, Ref: "#/operations/receiveEvents"},
		},
	})

	ch, err := client.Execute(ctx, "sendMessage", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error == nil || events[0].Error.Code != openbindings.ErrCodeAuthRequired {
		t.Errorf("expected http_401 error, got %+v", events[0])
	}
}

func TestIntegration_SendWithCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(testHandler))
	defer srv.Close()

	doc := makeAsyncAPISpec(srv.URL)
	store := openbindings.NewMemoryStore()
	ctx := context.Background()

	host := strings.TrimPrefix(srv.URL, "http://")
	store.Set(ctx, host, map[string]any{"bearerToken": testSecret})

	exec := NewExecutor()
	opExec := openbindings.NewOperationExecutor(exec)
	opExec.ContextStore = store

	client := openbindings.NewInterfaceClient(nil, opExec,
		openbindings.WithContextStore(store),
	)
	client.ResolveInterface(&openbindings.Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]openbindings.Operation{
			"sendMessage":   {},
			"receiveEvents": {},
		},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: {Format: FormatToken, Content: doc},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"sendMessage." + DefaultSourceName:   {Operation: "sendMessage", Source: DefaultSourceName, Ref: "#/operations/sendMessage"},
			"receiveEvents." + DefaultSourceName: {Operation: "receiveEvents", Source: DefaultSourceName, Ref: "#/operations/receiveEvents"},
		},
	})

	ch, err := client.Execute(ctx, "sendMessage", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %s", events[0].Error.Message)
	}
	m, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", events[0].Data)
	}
	if m["received"] == nil {
		t.Error("expected 'received' field in output")
	}
}

func TestIntegration_SSEReceiveWithCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(testHandler))
	defer srv.Close()

	doc := makeAsyncAPISpec(srv.URL)
	store := openbindings.NewMemoryStore()
	ctx := context.Background()

	host := strings.TrimPrefix(srv.URL, "http://")
	store.Set(ctx, host, map[string]any{"bearerToken": testSecret})

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:   "#/operations/receiveEvents",
		Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := drainStream(ch)
	if len(events) < 1 {
		t.Fatal("expected at least 1 event")
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %s", events[0].Error.Message)
	}
	// The SSE unary path collects events. Check we got data.
	if events[0].Data == nil {
		t.Error("expected data in first event")
	}
}

// ---------------------------------------------------------------------------
// WebSocket integration tests
//
// AsyncAPI 3.x supports WebSocket as a transport, with bidirectional
// streaming patterns. The asyncapi-go library models WS as
// "send one input message containing the operation arguments + bearer token,
// then read a stream of server-pushed messages until the connection closes
// or the caller cancels."
//
// These tests use httptest + nhooyr.io/websocket to spin up a real WebSocket
// server and exercise the executor end-to-end. They cover:
//   - Bearer token sent in the first message body (the spec convention,
//     because browsers can't set custom WebSocket upgrade headers)
//   - Query-param apiKey appended to the WebSocket URL (regression test for
//     a bug fixed earlier in the v0.1.0 polish cycle where credentials in
//     the query were silently dropped)
//   - Multi-event streaming with clean cancellation
//   - Send-action over WebSocket
// ---------------------------------------------------------------------------

// makeWSAsyncAPISpec returns an AsyncAPI 3.x document configured with a
// WebSocket server, given the host:port of an httptest.Server. The protocol
// is "ws" (not "wss") because httptest uses plain HTTP. Operations and
// channels are named the same as the HTTP test fixture for consistency.
func makeWSAsyncAPISpec(httpURL string, securityScheme SecurityScheme) *Document {
	host := strings.TrimPrefix(strings.TrimPrefix(httpURL, "http://"), "https://")
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "WS Test API", Version: "1.0.0"},
		Servers: map[string]Server{
			"wsServer": {
				Host:     host,
				Protocol: "ws",
				Security: []map[string][]string{{"auth": {}}},
			},
		},
		Channels: map[string]Channel{
			"stream": {Address: "/ws"},
		},
		Operations: map[string]Operation{
			"subscribe": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/stream"}},
			"publish":   {Action: "send", Channel: ChannelRef{Ref: "#/channels/stream"}},
		},
		Components: &Components{
			SecuritySchemes: map[string]SecurityScheme{
				"auth": securityScheme,
			},
		},
	}
	return doc
}

// wsTestServer returns an httptest.Server that upgrades GET /ws to a
// WebSocket and dispatches to the supplied exchange function. The exchange
// receives the upgraded connection and the http.Request (so it can inspect
// query parameters and headers from the upgrade request) and is responsible
// for reading client messages and writing server messages.
func wsTestServer(t *testing.T, exchange func(ctx context.Context, conn *websocket.Conn, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// TestIntegration_WebSocketBearerInFirstMessageBody verifies the spec
// convention for WebSocket auth: the bearer token is sent in the body of the
// first message after the upgrade, because browsers cannot set custom
// WebSocket upgrade headers. The test server reads the first message,
// asserts it carries a bearerToken field, and then streams two events.
func TestIntegration_WebSocketBearerInFirstMessageBody(t *testing.T) {
	receivedToken := ""
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		// Read the first message which carries auth.
		first, err := readWSJSON(ctx, conn)
		if err != nil {
			t.Errorf("read first message: %v", err)
			return
		}
		if tok, ok := first["bearerToken"].(string); ok {
			receivedToken = tok
		}
		// Stream two events back.
		_ = writeWSJSON(ctx, conn, map[string]any{"id": "1", "msg": "first"})
		_ = writeWSJSON(ctx, conn, map[string]any{"id": "2", "msg": "second"})
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "test-bearer-xyz"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	events := drainStream(ch)
	if receivedToken != "test-bearer-xyz" {
		t.Errorf("server received bearerToken = %q, want %q", receivedToken, "test-bearer-xyz")
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 stream events, got %d", len(events))
	}
}

// TestIntegration_WebSocketQueryParamApiKey is the regression test for the
// bug where query-param credentials populated by applyHTTPContext were
// dropped because subscribeWS dialed with the original wsURL string instead
// of the URL the request had been mutated to. This is the test the original
// bug would have failed on.
func TestIntegration_WebSocketQueryParamApiKey(t *testing.T) {
	receivedKey := ""
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		// The query param should be present on the WebSocket upgrade URL.
		receivedKey = r.URL.Query().Get("api_key")
		// Drain any first message and send a response.
		_, _ = readWSJSON(ctx, conn)
		_ = writeWSJSON(ctx, conn, map[string]any{"ok": true})
	})
	defer srv.Close()

	// Configure the spec to put the apiKey in the query.
	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{
		Type: "apiKey",
		In:   "query",
		Name: "api_key",
	})

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"apiKey": "secret-key-abc"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}
	_ = drainStream(ch)

	if receivedKey != "secret-key-abc" {
		t.Errorf("query-param apiKey not propagated to WebSocket URL: got %q, want %q", receivedKey, "secret-key-abc")
	}
}

// TestIntegration_WebSocketStreamingMultipleEvents verifies that the
// executor forwards each server-pushed WebSocket message as a separate
// stream event in arrival order.
func TestIntegration_WebSocketStreamingMultipleEvents(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		_, _ = readWSJSON(ctx, conn)
		for i := 1; i <= 5; i++ {
			_ = writeWSJSON(ctx, conn, map[string]any{"seq": i, "msg": fmt.Sprintf("event-%d", i)})
		}
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "tok"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	events := drainStream(ch)
	if len(events) != 5 {
		t.Fatalf("expected 5 stream events, got %d", len(events))
	}
	for i, ev := range events {
		if ev.Error != nil {
			t.Errorf("event %d: unexpected error: %s", i, ev.Error.Message)
			continue
		}
		data, _ := ev.Data.(map[string]any)
		seq, _ := data["seq"].(float64)
		if int(seq) != i+1 {
			t.Errorf("event %d: seq = %v, want %d", i, seq, i+1)
		}
	}
}

// TestIntegration_WebSocketCancellation verifies that cancelling the
// caller's context closes the WebSocket cleanly without leaving the goroutine
// hanging or panicking. The test server holds the connection open after
// sending one event, simulating a long-running subscription.
func TestIntegration_WebSocketCancellation(t *testing.T) {
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		_, _ = readWSJSON(ctx, conn)
		_ = writeWSJSON(ctx, conn, map[string]any{"id": "1"})
		// Hold the connection open until the request context is done
		// (the client cancellation propagates here through the HTTP
		// hijacker's request context).
		<-ctx.Done()
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})

	exec := NewExecutor()
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/subscribe",
		Context: map[string]any{"bearerToken": "tok"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	// Receive the first event.
	ev, ok := <-ch
	if !ok {
		t.Fatal("channel closed before first event")
	}
	if ev.Error != nil {
		t.Fatalf("first event error: %s", ev.Error.Message)
	}

	// Cancel and verify the channel closes within a reasonable time.
	cancel()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return // success: channel closed cleanly
			}
		case <-timeout:
			t.Fatal("WebSocket channel did not close within 3s of cancellation")
		}
	}
}

// TestIntegration_WebSocketSendAction verifies that an AsyncAPI send operation
// with a Reply defined over WebSocket uses connection pooling: the input is
// sent as a message on the pooled connection, and one reply is read back.
// Auth is on the upgrade headers (not in the message body).
func TestIntegration_WebSocketSendAction(t *testing.T) {
	var clientPayload map[string]any
	var upgradeAuth string
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgradeAuth = r.Header.Get("Authorization")
		first, err := readWSJSON(ctx, conn)
		if err != nil {
			t.Errorf("read send payload: %v", err)
			return
		}
		clientPayload = first
		// Echo back a reply.
		_ = writeWSJSON(ctx, conn, map[string]any{"ack": true, "command": first["command"]})
	})
	defer srv.Close()

	doc := makeWSAsyncAPISpec(srv.URL, SecurityScheme{Type: "http", Scheme: "bearer"})
	// Add a Reply to the publish operation so it reads one response.
	pubOp := doc.Operations["publish"]
	pubOp.Reply = &OperationReply{}
	doc.Operations["publish"] = pubOp

	exec := NewExecutor()
	defer exec.Close()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: doc,
		},
		Ref:     "#/operations/publish",
		Input:   map[string]any{"command": "do-thing", "value": 42},
		Context: map[string]any{"bearerToken": "tok"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	events := drainStream(ch)

	// Auth should be on the upgrade request, not in the message body.
	if upgradeAuth != "Bearer tok" {
		t.Errorf("upgrade Authorization = %q, want %q", upgradeAuth, "Bearer tok")
	}

	// Server should have received the input fields (no bearerToken in body).
	if clientPayload == nil {
		t.Fatal("server did not receive any client payload")
	}
	if clientPayload["command"] != "do-thing" {
		t.Errorf("server payload command = %v, want do-thing", clientPayload["command"])
	}
	if v, _ := clientPayload["value"].(float64); int(v) != 42 {
		t.Errorf("server payload value = %v, want 42", clientPayload["value"])
	}
	if _, hasBearerToken := clientPayload["bearerToken"]; hasBearerToken {
		t.Error("pooled send should not include bearerToken in the message body")
	}

	// Client should have received the reply.
	if len(events) != 1 {
		t.Fatalf("expected 1 reply event, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %s", events[0].Error.Message)
	}
	reply, _ := events[0].Data.(map[string]any)
	if reply["ack"] != true {
		t.Errorf("reply ack = %v, want true", reply["ack"])
	}
}
