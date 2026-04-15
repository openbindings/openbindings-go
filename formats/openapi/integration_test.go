package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

const secret = "test-token-123"

// ---------------------------------------------------------------------------
// Multipart/form-data integration test
// ---------------------------------------------------------------------------

func TestIntegration_MultipartFormData(t *testing.T) {
	var receivedFile []byte
	var receivedDesc string
	var receivedContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" && r.Method == "POST" {
			receivedContentType = r.Header.Get("Content-Type")
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			receivedDesc = r.FormValue("description")
			file, _, err := r.FormFile("file")
			if err != nil {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
				return
			}
			defer file.Close()
			receivedFile, _ = io.ReadAll(file)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "Upload API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": srv.URL}},
		"paths": map[string]any{
			"/upload": map[string]any{
				"post": map[string]any{
					"operationId": "uploadFile",
					"requestBody": map[string]any{
						"required": true,
						"content": map[string]any{
							"multipart/form-data": map[string]any{
								"schema": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"file": map[string]any{
											"type":   "string",
											"format": "binary",
										},
										"description": map[string]any{
											"type": "string",
										},
									},
								},
							},
						},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"status": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	exec := NewExecutor()
	ctx := context.Background()

	specBytes, _ := json.Marshal(spec)

	ch, _ := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1upload/post",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: string(specBytes),
		},
		Input: map[string]any{
			"file":        []byte("binary-content-here"),
			"description": "my upload",
		},
	})

	ev := drainStream(ch)
	if ev == nil {
		t.Fatal("expected event, got none")
	}
	if ev.Error != nil {
		t.Fatalf("unexpected error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	// Verify the server received multipart data
	if !strings.Contains(receivedContentType, "multipart/form-data") {
		t.Errorf("server received Content-Type = %q, want multipart/form-data", receivedContentType)
	}
	if string(receivedFile) != "binary-content-here" {
		t.Errorf("server received file = %q, want %q", string(receivedFile), "binary-content-here")
	}
	if receivedDesc != "my upload" {
		t.Errorf("server received description = %q, want %q", receivedDesc, "my upload")
	}
}

var items = []map[string]any{
	{"id": float64(1), "name": "Alpha"},
	{"id": float64(2), "name": "Bravo"},
}

func makeOpenAPISpec(baseURL string) map[string]any {
	return map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "Test API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": baseURL}},
		"paths": map[string]any{
			"/items": map[string]any{
				"get": map[string]any{
					"operationId": "listItems",
					"summary":     "List all items",
					"security":    []map[string]any{{"bearerAuth": []any{}}},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "array",
										"items": map[string]any{
											"type": "object",
											"properties": map[string]any{
												"id":   map[string]any{"type": "integer"},
												"name": map[string]any{"type": "string"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			"/items/{id}": map[string]any{
				"get": map[string]any{
					"operationId": "getItem",
					"summary":     "Get a single item",
					"security":    []map[string]any{{"bearerAuth": []any{}}},
					"parameters": []map[string]any{
						{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "integer"}},
					},
					"responses": map[string]any{
						"200": map[string]any{
							"description": "OK",
							"content": map[string]any{
								"application/json": map[string]any{
									"schema": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"id":   map[string]any{"type": "integer"},
											"name": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{"type": "http", "scheme": "bearer"},
			},
		},
	}
}

var itemIDPattern = regexp.MustCompile(`^/items/(\d+)$`)

func testHandler(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" && r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(makeOpenAPISpec(baseURL))
			return
		}

		if r.URL.Path == "/items" && r.Method == "GET" {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(items)
			return
		}

		matches := itemIDPattern.FindStringSubmatch(r.URL.Path)
		if matches != nil && r.Method == "GET" {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(401)
				json.NewEncoder(w).Encode(map[string]any{"error": "unauthorized"})
				return
			}
			for _, item := range items {
				if fmt.Sprintf("%v", item["id"]) == matches[1] {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(item)
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
			return
		}

		w.WriteHeader(404)
	}
}

func drainStream(ch <-chan openbindings.StreamEvent) *openbindings.StreamEvent {
	var last *openbindings.StreamEvent
	for ev := range ch {
		ev := ev
		last = &ev
	}
	return last
}

func setupServer() (*httptest.Server, string) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testHandler(srv.URL)(w, r)
	}))
	specURL := srv.URL + "/openapi.json"
	return srv, specURL
}

// synthesizeOBI creates an OBI from the OpenAPI spec served at specURL.
func synthesizeOBI(t *testing.T, specURL string) *openbindings.Interface {
	t.Helper()
	creator := NewCreator()
	ctx := context.Background()
	iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{
			{Format: FormatToken, Location: specURL},
		},
	})
	if err != nil {
		t.Fatalf("synthesizeOBI failed: %v", err)
	}
	return iface
}

func TestIntegration_NoCredentials401(t *testing.T) {
	srv, specURL := setupServer()
	defer srv.Close()

	store := openbindings.NewMemoryStore()
	exec := NewExecutor()
	opExec := openbindings.NewOperationExecutor(exec)

	client := openbindings.NewInterfaceClient(nil, opExec, openbindings.WithContextStore(store))

	iface := synthesizeOBI(t, specURL)
	client.ResolveInterface(iface)

	if client.State() != openbindings.StateBound {
		t.Fatalf("state = %q, want %q", client.State(), openbindings.StateBound)
	}

	ctx := context.Background()
	ch, err := client.Execute(ctx, "listItems", nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	ev := drainStream(ch)
	if ev == nil {
		t.Fatal("expected at least one event, got none")
	}
	if ev.Error == nil {
		t.Fatal("expected error event, got data")
	}
	if ev.Error.Code != openbindings.ErrCodeAuthRequired {
		t.Errorf("error code = %q, want %q", ev.Error.Code, openbindings.ErrCodeAuthRequired)
	}
}

func TestIntegration_PreStoredCredentialsSucceed(t *testing.T) {
	srv, specURL := setupServer()
	defer srv.Close()

	store := openbindings.NewMemoryStore()
	ctx := context.Background()

	// Pre-store credentials under the normalized server key
	contextKey := openbindings.NormalizeContextKey(srv.URL)
	if err := store.Set(ctx, contextKey, map[string]any{"bearerToken": secret}); err != nil {
		t.Fatalf("store.Set failed: %v", err)
	}

	exec := NewExecutor()
	opExec := openbindings.NewOperationExecutor(exec)
	client := openbindings.NewInterfaceClient(nil, opExec, openbindings.WithContextStore(store))

	iface := synthesizeOBI(t, specURL)
	client.ResolveInterface(iface)

	if client.State() != openbindings.StateBound {
		t.Fatalf("state = %q, want %q", client.State(), openbindings.StateBound)
	}

	// First call: listItems should succeed
	ch, err := client.Execute(ctx, "listItems", nil)
	if err != nil {
		t.Fatalf("Execute listItems failed: %v", err)
	}
	ev := drainStream(ch)
	if ev == nil {
		t.Fatal("expected event, got none")
	}
	if ev.Error != nil {
		t.Fatalf("unexpected error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	// Verify the data matches expected items
	data, err := json.Marshal(ev.Data)
	if err != nil {
		t.Fatalf("failed to marshal response data: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("failed to unmarshal response as array: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d items, want 2", len(got))
	}

	// Second call: same operation reuses credentials
	ch2, err := client.Execute(ctx, "listItems", nil)
	if err != nil {
		t.Fatalf("Execute listItems (2nd) failed: %v", err)
	}
	ev2 := drainStream(ch2)
	if ev2 == nil || ev2.Error != nil {
		t.Fatal("second listItems call should succeed")
	}

	// Different operation: getItem with path parameter
	ch3, err := client.Execute(ctx, "getItem", map[string]any{"id": 1})
	if err != nil {
		t.Fatalf("Execute getItem failed: %v", err)
	}
	ev3 := drainStream(ch3)
	if ev3 == nil {
		t.Fatal("expected event for getItem, got none")
	}
	if ev3.Error != nil {
		t.Fatalf("getItem unexpected error: %s: %s", ev3.Error.Code, ev3.Error.Message)
	}

	itemData, err := json.Marshal(ev3.Data)
	if err != nil {
		t.Fatalf("failed to marshal getItem response: %v", err)
	}
	var item map[string]any
	if err := json.Unmarshal(itemData, &item); err != nil {
		t.Fatalf("failed to unmarshal getItem response: %v", err)
	}
	if item["name"] != "Alpha" {
		t.Errorf("item name = %v, want %q", item["name"], "Alpha")
	}
}

func TestIntegration_IsolatedStoresDontShareCredentials(t *testing.T) {
	srv, specURL := setupServer()
	defer srv.Close()

	ctx := context.Background()
	store1 := openbindings.NewMemoryStore()
	store2 := openbindings.NewMemoryStore()

	// Only store1 has credentials
	contextKey := openbindings.NormalizeContextKey(srv.URL)
	if err := store1.Set(ctx, contextKey, map[string]any{"bearerToken": secret}); err != nil {
		t.Fatalf("store1.Set failed: %v", err)
	}

	exec1 := NewExecutor()
	opExec1 := openbindings.NewOperationExecutor(exec1)
	client1 := openbindings.NewInterfaceClient(nil, opExec1, openbindings.WithContextStore(store1))

	exec2 := NewExecutor()
	opExec2 := openbindings.NewOperationExecutor(exec2)
	client2 := openbindings.NewInterfaceClient(nil, opExec2, openbindings.WithContextStore(store2))

	// Synthesize OBI from the spec and resolve both clients
	iface := synthesizeOBI(t, specURL)
	client1.ResolveInterface(iface)
	client2.ResolveInterface(iface)

	// Client 1 should succeed (has credentials)
	ch1, err := client1.Execute(ctx, "listItems", nil)
	if err != nil {
		t.Fatalf("client1 Execute failed: %v", err)
	}
	ev1 := drainStream(ch1)
	if ev1 == nil || ev1.Error != nil {
		t.Fatal("client1 should succeed with stored credentials")
	}

	// Client 2 should get 401 (no credentials)
	ch2, err := client2.Execute(ctx, "listItems", nil)
	if err != nil {
		t.Fatalf("client2 Execute failed: %v", err)
	}
	ev2 := drainStream(ch2)
	if ev2 == nil {
		t.Fatal("expected event from client2, got none")
	}
	if ev2.Error == nil {
		t.Fatal("client2 should fail without credentials")
	}
	if ev2.Error.Code != openbindings.ErrCodeAuthRequired {
		t.Errorf("client2 error code = %q, want %q", ev2.Error.Code, openbindings.ErrCodeAuthRequired)
	}
}

// ---------------------------------------------------------------------------
// Server-Sent Events (SSE) integration tests
// ---------------------------------------------------------------------------

// sseSpec returns an OpenAPI doc that declares a single endpoint returning
// `text/event-stream`. The path/method are constants the SSE tests share.
func sseSpec(serverURL string) string {
	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "SSE API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": serverURL}},
		"paths": map[string]any{
			"/events": map[string]any{
				"get": map[string]any{
					"operationId": "subscribeEvents",
					"responses": map[string]any{
						"200": map[string]any{
							"description": "Stream of events",
							"content": map[string]any{
								"text/event-stream": map[string]any{
									"schema": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"id":  map[string]any{"type": "string"},
											"msg": map[string]any{"type": "string"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	bytes, _ := json.Marshal(spec)
	return string(bytes)
}

func TestIntegration_SSEResponse_StreamsEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The executor must advertise SSE in its Accept header.
		if accept := r.Header.Get("Accept"); !strings.Contains(accept, "text/event-stream") {
			t.Errorf("Accept header = %q, expected to contain text/event-stream", accept)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// Three JSON events plus one with extra metadata fields.
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"msg\":\"first\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"2\",\"msg\":\"second\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "event: progress\nid: 42\ndata: {\"id\":\"3\",\"msg\":\"third\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, ": this is a comment, should be ignored\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	exec := NewExecutor()
	ctx := context.Background()

	ch, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1events/get",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: sseSpec(srv.URL),
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 SSE events, got %d", len(events))
	}

	// Event 1: simple data-only payload — emitted as the parsed JSON object directly.
	first, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("event 0: expected map data, got %T", events[0].Data)
	}
	if first["id"] != "1" || first["msg"] != "first" {
		t.Errorf("event 0 data = %+v, want id=1 msg=first", first)
	}

	// Event 2: simple data-only payload.
	second, ok := events[1].Data.(map[string]any)
	if !ok {
		t.Fatalf("event 1: expected map data, got %T", events[1].Data)
	}
	if second["id"] != "2" || second["msg"] != "second" {
		t.Errorf("event 1 data = %+v, want id=2 msg=second", second)
	}

	// Event 3: has event name + id, so wrapped in {data, event, id}.
	third, ok := events[2].Data.(map[string]any)
	if !ok {
		t.Fatalf("event 2: expected wrapped map, got %T", events[2].Data)
	}
	if third["event"] != "progress" {
		t.Errorf("event 2 event name = %v, want progress", third["event"])
	}
	if third["id"] != "42" {
		t.Errorf("event 2 id = %v, want 42", third["id"])
	}
	innerData, ok := third["data"].(map[string]any)
	if !ok {
		t.Fatalf("event 2 inner data: expected map, got %T", third["data"])
	}
	if innerData["msg"] != "third" {
		t.Errorf("event 2 inner msg = %v, want third", innerData["msg"])
	}
}

func TestIntegration_SSEResponse_NotSSE_StaysUnary(t *testing.T) {
	// Server returns plain JSON (not SSE) — the executor should fall through
	// to the unary path and emit a single event.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"only","msg":"hi"}`)
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1events/get",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: sseSpec(srv.URL),
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}
	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 unary event, got %d", len(events))
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", events[0].Data)
	}
	if data["msg"] != "hi" {
		t.Errorf("data msg = %v, want hi", data["msg"])
	}
}

func TestIntegration_SSEResponse_MultilineData(t *testing.T) {
	// Per the SSE spec, multiple `data:` lines for one event are joined with \n.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: line one\ndata: line two\ndata: line three\n\n")
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, _ := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1events/get",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: sseSpec(srv.URL),
		},
	})
	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event from multiline data, got %d", len(events))
	}
	// The combined data is plain text (not JSON), so it should pass through as a string.
	str, ok := events[0].Data.(string)
	if !ok {
		t.Fatalf("expected string data, got %T", events[0].Data)
	}
	if str != "line one\nline two\nline three" {
		t.Errorf("data = %q, want \"line one\\nline two\\nline three\"", str)
	}
}

// TestIntegration_SSEResponse_MidStreamClose verifies that when the server
// abruptly closes the connection after some events, the executor emits the
// events received before the close and then closes the channel cleanly.
// No goroutine leak, no panic, no hung channel.
func TestIntegration_SSEResponse_MidStreamClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		// Two complete events, then drop the connection.
		_, _ = io.WriteString(w, "data: {\"id\":\"1\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"2\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Hijack and close to simulate an abrupt connection drop without
		// sending the trailing blank line that some servers would.
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1events/get",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: sseSpec(srv.URL),
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	// Drain. The channel must close on its own — no hang.
	timeout := time.After(3 * time.Second)
	var events []openbindings.StreamEvent
loop:
	for {
		select {
		case ev, open := <-ch:
			if !open {
				break loop
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatal("SSE channel did not close within 3s of mid-stream connection drop")
		}
	}

	// We should have received the two events sent before the close.
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events before drop, got %d", len(events))
	}
}

// TestIntegration_SSEResponse_MalformedLines verifies that malformed SSE
// lines are handled gracefully per the W3C spec, which is lenient: lines
// without a colon are treated as a field with an empty value, lines starting
// with a colon are comments and ignored, and unknown field names are dropped.
func TestIntegration_SSEResponse_MalformedLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A mix of valid and malformed lines:
		// - line with no colon (treated as field name with empty value, ignored if not "data")
		// - comment lines (starting with :)
		// - unknown field name (silently ignored)
		// - normal data event after the noise
		_, _ = io.WriteString(w, ": this is a comment\n")
		_, _ = io.WriteString(w, "garbage-line-no-colon\n")
		_, _ = io.WriteString(w, "unknown-field: value\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"survivor\"}\n\n")
		_, _ = io.WriteString(w, ": another comment\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"second\"}\n\n")
	}))
	defer srv.Close()

	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1events/get",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: sseSpec(srv.URL),
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Should have exactly two valid data events. Malformed lines are noise.
	if len(events) != 2 {
		t.Fatalf("expected 2 valid events from malformed stream, got %d", len(events))
	}
	first, _ := events[0].Data.(map[string]any)
	if first["id"] != "survivor" {
		t.Errorf("first event id = %v, want survivor", first["id"])
	}
}

// TestNewExecutorWithClient verifies that an Executor created with a custom
// HTTP client uses that client for outbound requests, allowing tests and
// applications to substitute transport behavior without reaching into
// package-level globals.
func TestNewExecutorWithClient(t *testing.T) {
	var requestCount int
	customTransport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		// Return a canned 200 response without making a real network call.
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"custom":"client"}`)),
			Request:    req,
		}, nil
	})
	customClient := &http.Client{Transport: customTransport}

	exec := NewExecutorWithClient(customClient)
	// sseSpec is convenient because it gives us a valid OpenAPI doc with
	// one GET /events endpoint; we don't actually care about the URL.
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1events/get",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: sseSpec("http://example.test"),
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if requestCount != 1 {
		t.Errorf("expected custom transport to be called exactly once, got %d", requestCount)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", events[0].Data)
	}
	if data["custom"] != "client" {
		t.Errorf("expected response from custom client, got %+v", data)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// TestIntegration_SSEResponse_Cancellation verifies that cancelling the
// caller's context closes the SSE channel cleanly without leaking the
// scanner goroutine. The server holds the connection open after sending
// one event, simulating a long-running subscription.
func TestIntegration_SSEResponse_Cancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)

		_, _ = io.WriteString(w, "data: {\"id\":\"first\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Hold the connection open until the client disconnects.
		<-r.Context().Done()
	}))
	defer srv.Close()

	exec := NewExecutor()
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Ref: "#/paths/~1events/get",
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: sseSpec(srv.URL),
		},
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
		t.Fatalf("first event error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	// Cancel and verify the channel closes within a reasonable time.
	cancel()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return // success
			}
		case <-timeout:
			t.Fatal("SSE channel did not close within 3s of cancellation")
		}
	}
}
