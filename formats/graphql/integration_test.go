package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

// testSchema is a minimal GraphQL introspection response for a service with
// one query (user) and one mutation (createUser).
var testSchema = introspectionSchema{
	QueryType:    &typeRef{Name: "Query"},
	MutationType: &typeRef{Name: "Mutation"},
	Types: []fullType{
		{
			Kind: "OBJECT",
			Name: "Query",
			Fields: []field{
				{
					Name:        "user",
					Description: "Get a user by ID",
					Args: []inputValue{
						{Name: "id", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "ID"}}},
					},
					Type: typeRef{Kind: "OBJECT", Name: "User"},
				},
				{
					Name: "users",
					Type: typeRef{Kind: "LIST", OfType: &typeRef{Kind: "OBJECT", Name: "User"}},
				},
			},
		},
		{
			Kind: "OBJECT",
			Name: "Mutation",
			Fields: []field{
				{
					Name: "createUser",
					Args: []inputValue{
						{Name: "name", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "String"}}},
						{Name: "email", Type: typeRef{Kind: "SCALAR", Name: "String"}},
					},
					Type: typeRef{Kind: "OBJECT", Name: "User"},
				},
			},
		},
		{
			Kind: "OBJECT",
			Name: "User",
			Fields: []field{
				{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
				{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "email", Type: typeRef{Kind: "SCALAR", Name: "String"}},
			},
		},
	},
}

// newTestServer creates an httptest server that handles GraphQL introspection
// and query/mutation execution.
func newTestServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Introspection query
		if strings.Contains(req.Query, "__schema") {
			resp := map[string]any{
				"data": map[string]any{
					"__schema": testSchema,
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Query: user
		if strings.Contains(req.Query, "user(") || strings.Contains(req.Query, "user (") {
			id, _ := req.Variables["id"].(string)
			resp := map[string]any{
				"data": map[string]any{
					"user": map[string]any{
						"id":    id,
						"name":  "Alice",
						"email": "alice@example.com",
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Query: users (no args)
		if strings.Contains(req.Query, "users") {
			resp := map[string]any{
				"data": map[string]any{
					"users": []any{
						map[string]any{"id": "1", "name": "Alice", "email": "alice@example.com"},
						map[string]any{"id": "2", "name": "Bob", "email": "bob@example.com"},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Mutation: createUser
		if strings.Contains(req.Query, "createUser") {
			name, _ := req.Variables["name"].(string)
			email, _ := req.Variables["email"].(string)
			resp := map[string]any{
				"data": map[string]any{
					"createUser": map[string]any{
						"id":    "new-123",
						"name":  name,
						"email": email,
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		// Unknown query
		resp := map[string]any{
			"errors": []map[string]any{
				{"message": "unknown query"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestIntegrationExecuteQuery(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	executor := NewExecutor()
	ctx := context.Background()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
		},
		Ref:   "Query/user",
		Input: map[string]any{"id": "42"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("stream error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	data, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", ev.Data)
	}
	if data["id"] != "42" {
		t.Errorf("user id = %v, want 42", data["id"])
	}
	if data["name"] != "Alice" {
		t.Errorf("user name = %v, want Alice", data["name"])
	}
}

func TestIntegrationExecuteQueryNoArgs(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	executor := NewExecutor()
	ctx := context.Background()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
		},
		Ref: "Query/users",
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("stream error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	data, ok := ev.Data.([]any)
	if !ok {
		t.Fatalf("expected array data, got %T", ev.Data)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 users, got %d", len(data))
	}
}

func TestIntegrationExecuteMutation(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	executor := NewExecutor()
	ctx := context.Background()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
		},
		Ref:   "Mutation/createUser",
		Input: map[string]any{"name": "Charlie", "email": "charlie@example.com"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("stream error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	data, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", ev.Data)
	}
	if data["name"] != "Charlie" {
		t.Errorf("user name = %v, want Charlie", data["name"])
	}
	if data["id"] != "new-123" {
		t.Errorf("user id = %v, want new-123", data["id"])
	}
}

func TestIntegrationExecuteWithSchemaQuery(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	executor := NewExecutor()
	ctx := context.Background()

	// Simulate what happens when the OperationExecutor passes through
	// the InputSchema with a _query const.
	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string"},
			"_query": map[string]any{"type": "string", "const": "query($id: ID!) { user(id: $id) { id name } }"},
		},
	}

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
		},
		Ref:         "Query/user",
		Input:       map[string]any{"id": "99"},
		InputSchema: inputSchema,
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("stream error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	data, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", ev.Data)
	}
	if data["id"] != "99" {
		t.Errorf("user id = %v, want 99", data["id"])
	}
}

func TestIntegrationCreateInterface(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	creator := NewCreator()
	ctx := context.Background()

	iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{{
			Format:   "graphql",
			Location: srv.URL,
		}},
	})
	if err != nil {
		t.Fatalf("CreateInterface error: %v", err)
	}

	// Should have 3 operations: user, users, createUser.
	if len(iface.Operations) != 3 {
		t.Errorf("expected 3 operations, got %d", len(iface.Operations))
	}

	// Verify user operation has _query const in input schema.
	userOp, ok := iface.Operations["user"]
	if !ok {
		t.Fatal("missing user operation")
	}
	if userOp.Input == nil {
		t.Fatal("user operation has no input schema")
	}
	props, _ := userOp.Input["properties"].(map[string]any)
	queryProp, _ := props["_query"].(map[string]any)
	if queryProp == nil {
		t.Fatal("user input schema missing _query property")
	}
	constVal, _ := queryProp["const"].(string)
	if constVal == "" {
		t.Fatal("user _query const is empty")
	}

	// Verify bindings.
	if len(iface.Bindings) != 3 {
		t.Errorf("expected 3 bindings, got %d", len(iface.Bindings))
	}
	userBinding, ok := iface.Bindings["user.graphql"]
	if !ok {
		t.Fatal("missing user.graphql binding")
	}
	if userBinding.Ref != "Query/user" {
		t.Errorf("user binding ref = %q, want Query/user", userBinding.Ref)
	}
}

func TestIntegrationSourceContent(t *testing.T) {
	// Test that the executor can use inline Source.Content instead of
	// making a network introspection call.
	srv := newTestServer()
	defer srv.Close()

	// Build the introspection content as the executor would receive it.
	schemaJSON, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"__schema": testSchema,
		},
	})

	executor := NewExecutor()
	ctx := context.Background()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
			Content:  string(schemaJSON),
		},
		Ref:   "Query/users",
		Input: nil,
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("stream error: %s: %s", ev.Error.Code, ev.Error.Message)
	}

	data, ok := ev.Data.([]any)
	if !ok {
		t.Fatalf("expected array data, got %T", ev.Data)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 users, got %d", len(data))
	}
}

func TestIntegrationAuthRetry(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		// Introspection always works.
		if strings.Contains(req.Query, "__schema") {
			resp := map[string]any{"data": map[string]any{"__schema": testSchema}}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}

		callCount++
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"errors":[{"message":"unauthorized"}]}`))
			return
		}

		// Second call with credentials succeeds.
		resp := map[string]any{
			"data": map[string]any{
				"users": []any{
					map[string]any{"id": "1", "name": "Alice"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	executor := NewExecutor()
	ctx := context.Background()

	promptCalled := false
	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
		},
		Ref: "Query/users",
		Security: []openbindings.SecurityMethod{
			{Type: "bearer", Description: "Bearer token"},
		},
		Callbacks: &openbindings.PlatformCallbacks{
			Prompt: func(ctx context.Context, message string, opts *openbindings.PromptOptions) (string, error) {
				promptCalled = true
				return "test-token", nil
			},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("stream error: %s: %s", ev.Error.Code, ev.Error.Message)
	}
	if !promptCalled {
		t.Error("expected prompt callback to be called for auth resolution")
	}
	if callCount != 2 {
		t.Errorf("expected 2 query calls (first 401, second success), got %d", callCount)
	}
}

// newSubscriptionTestServer creates an httptest server that speaks the
// graphql-transport-ws subprotocol and serves a single subscription
// (`messageStream`) that emits a fixed sequence of three events and then a
// "complete" message. It also serves the introspection query over HTTP POST
// so the same server can be used as the GraphQL endpoint URL.
func newSubscriptionTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	subSchema := introspectionSchema{
		QueryType:        &typeRef{Name: "Query"},
		SubscriptionType: &typeRef{Name: "Subscription"},
		Types: []fullType{
			{
				Kind: "OBJECT",
				Name: "Query",
				Fields: []field{
					{Name: "ping", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				},
			},
			{
				Kind: "OBJECT",
				Name: "Subscription",
				Fields: []field{
					{
						Name: "messageStream",
						Args: []inputValue{
							{Name: "topic", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "String"}}},
						},
						Type: typeRef{Kind: "OBJECT", Name: "Message"},
					},
				},
			},
			{
				Kind: "OBJECT",
				Name: "Message",
				Fields: []field{
					{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
					{Name: "body", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				},
			},
		},
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Subscription path: WebSocket upgrade with the graphql-transport-ws subprotocol.
		if r.Header.Get("Upgrade") == "websocket" {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				Subprotocols: []string{"graphql-transport-ws"},
			})
			if err != nil {
				t.Logf("websocket accept failed: %v", err)
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "test done")

			ctx := r.Context()
			runSubscriptionExchange(t, ctx, conn)
			return
		}

		// Otherwise treat it as a normal HTTP POST for introspection.
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Query, "__schema") {
			http.Error(w, "only introspection supported on HTTP path", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"__schema": subSchema},
		})
	}))
}

// runSubscriptionExchange implements the server side of the
// graphql-transport-ws protocol for the subscription test: read
// connection_init, reply with connection_ack, read subscribe, then send three
// "next" payloads and a "complete" message.
func runSubscriptionExchange(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()

	// Expect connection_init.
	if err := expectClientMessage(ctx, conn, "connection_init"); err != nil {
		t.Logf("server: %v", err)
		return
	}
	if err := writeServerMessage(ctx, conn, map[string]any{"type": "connection_ack"}); err != nil {
		t.Logf("server: %v", err)
		return
	}

	// Expect subscribe.
	subID, err := expectClientSubscribe(ctx, conn)
	if err != nil {
		t.Logf("server: %v", err)
		return
	}

	// Send three next events, then complete.
	for i := 1; i <= 3; i++ {
		payload := map[string]any{
			"data": map[string]any{
				"messageStream": map[string]any{
					"id":   formatInt(i),
					"body": "msg-" + formatInt(i),
				},
			},
		}
		msg := map[string]any{"id": subID, "type": "next", "payload": payload}
		if err := writeServerMessage(ctx, conn, msg); err != nil {
			t.Logf("server: %v", err)
			return
		}
	}
	_ = writeServerMessage(ctx, conn, map[string]any{"id": subID, "type": "complete"})
}

func expectClientMessage(ctx context.Context, conn *websocket.Conn, expectedType string) error {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, raw, err := conn.Read(readCtx)
	if err != nil {
		return err
	}
	var msg struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return err
	}
	if msg.Type != expectedType {
		return &unexpectedMessageError{want: expectedType, got: msg.Type}
	}
	return nil
}

type unexpectedMessageError struct {
	want, got string
}

func (e *unexpectedMessageError) Error() string {
	return "expected " + e.want + ", got " + e.got
}

func expectClientSubscribe(ctx context.Context, conn *websocket.Conn) (string, error) {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, raw, err := conn.Read(readCtx)
	if err != nil {
		return "", err
	}
	var msg struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", err
	}
	if msg.Type != "subscribe" {
		return "", &unexpectedMessageError{want: "subscribe", got: msg.Type}
	}
	if msg.ID == "" {
		return "", &unexpectedMessageError{want: "non-empty id", got: ""}
	}
	return msg.ID, nil
}

func writeServerMessage(ctx context.Context, conn *websocket.Conn, msg any) error {
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(writeCtx, websocket.MessageText, raw)
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// TestIntegrationExecuteSubscription verifies that the GraphQL executor opens
// a graphql-transport-ws WebSocket connection, sends connection_init,
// subscribes, and forwards each "next" payload as a separate stream event.
// Closes cleanly on "complete".
func TestIntegrationExecuteSubscription(t *testing.T) {
	srv := newSubscriptionTestServer(t)
	defer srv.Close()

	executor := NewExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
		},
		Ref:   "Subscription/messageStream",
		Input: map[string]any{"topic": "alerts"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	// Drain the channel until close. Expect three data events and no errors.
	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 stream events from subscription, got %d", len(events))
	}
	for i, ev := range events {
		if ev.Error != nil {
			t.Fatalf("event %d: unexpected stream error: %s: %s", i, ev.Error.Code, ev.Error.Message)
		}
		data, ok := ev.Data.(map[string]any)
		if !ok {
			t.Fatalf("event %d: expected map data, got %T", i, ev.Data)
		}
		ms, ok := data["messageStream"].(map[string]any)
		if !ok {
			t.Fatalf("event %d: expected messageStream object, got %T", i, data["messageStream"])
		}
		wantID := formatInt(i + 1)
		if ms["id"] != wantID {
			t.Errorf("event %d: id = %v, want %s", i, ms["id"], wantID)
		}
		if ms["body"] != "msg-"+wantID {
			t.Errorf("event %d: body = %v, want msg-%s", i, ms["body"], wantID)
		}
	}
}

// TestIntegrationSubscriptionCancellation verifies that cancelling the caller's
// context closes the WebSocket cleanly without leaking the goroutine.
func TestIntegrationSubscriptionCancellation(t *testing.T) {
	// Server that holds the connection open after sending one event,
	// so we can verify the executor closes on context cancellation.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				Subprotocols: []string{"graphql-transport-ws"},
			})
			if err != nil {
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "")
			ctx := r.Context()
			if expectClientMessage(ctx, conn, "connection_init") != nil {
				return
			}
			_ = writeServerMessage(ctx, conn, map[string]any{"type": "connection_ack"})
			subID, err := expectClientSubscribe(ctx, conn)
			if err != nil {
				return
			}
			// Send one event and then hang until the connection drops.
			_ = writeServerMessage(ctx, conn, map[string]any{
				"id":      subID,
				"type":    "next",
				"payload": map[string]any{"data": map[string]any{"messageStream": map[string]any{"id": "1", "body": "first"}}},
			})
			<-ctx.Done()
			return
		}
		// Introspection.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"__schema": introspectionSchema{
					QueryType:        &typeRef{Name: "Query"},
					SubscriptionType: &typeRef{Name: "Subscription"},
					Types: []fullType{
						{Kind: "OBJECT", Name: "Query", Fields: []field{{Name: "ping", Type: typeRef{Kind: "SCALAR", Name: "String"}}}},
						{Kind: "OBJECT", Name: "Subscription", Fields: []field{{Name: "messageStream", Type: typeRef{Kind: "OBJECT", Name: "Message"}}}},
						{Kind: "OBJECT", Name: "Message", Fields: []field{{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}}, {Name: "body", Type: typeRef{Kind: "SCALAR", Name: "String"}}}},
					},
				},
			},
		})
	}))
	defer srv.Close()

	executor := NewExecutor()
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   "graphql",
			Location: srv.URL,
		},
		Ref: "Subscription/messageStream",
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
				return // success: channel closed after cancel
			}
			// Drain any in-flight events.
		case <-timeout:
			t.Fatal("subscription channel did not close within 3s of cancellation")
		}
	}
}

// minimalSubscriptionSchema returns the introspection schema used by the
// subscription error/edge tests below. Same shape as the happy-path test
// (Query/Subscription/Message types) but pulled out for reuse.
func minimalSubscriptionSchema() introspectionSchema {
	return introspectionSchema{
		QueryType:        &typeRef{Name: "Query"},
		SubscriptionType: &typeRef{Name: "Subscription"},
		Types: []fullType{
			{Kind: "OBJECT", Name: "Query", Fields: []field{{Name: "ping", Type: typeRef{Kind: "SCALAR", Name: "String"}}}},
			{
				Kind: "OBJECT",
				Name: "Subscription",
				Fields: []field{
					{Name: "messageStream", Type: typeRef{Kind: "OBJECT", Name: "Message"}},
				},
			},
			{
				Kind: "OBJECT",
				Name: "Message",
				Fields: []field{
					{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
					{Name: "body", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				},
			},
		},
	}
}

// subscriptionErrorTestServer wraps a per-test exchange function with the
// boilerplate for serving introspection over POST and dispatching to a
// WebSocket exchange handler over GET.
func subscriptionErrorTestServer(t *testing.T, exchange func(ctx context.Context, conn *websocket.Conn)) *httptest.Server {
	t.Helper()
	schema := minimalSubscriptionSchema()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				Subprotocols: []string{"graphql-transport-ws"},
			})
			if err != nil {
				t.Logf("ws accept: %v", err)
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "test done")
			exchange(r.Context(), conn)
			return
		}
		// Introspection over POST.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"__schema": schema},
		})
	}))
}

// TestIntegrationSubscription_ErrorMessage verifies that when the server
// sends an "error" message instead of "next" payloads, the executor surfaces
// it as a stream error event with the right message.
func TestIntegrationSubscription_ErrorMessage(t *testing.T) {
	srv := subscriptionErrorTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		if expectClientMessage(ctx, conn, "connection_init") != nil {
			return
		}
		_ = writeServerMessage(ctx, conn, map[string]any{"type": "connection_ack"})
		subID, err := expectClientSubscribe(ctx, conn)
		if err != nil {
			return
		}
		// Send an error message — graphql-transport-ws spec: errors are an
		// array of GraphQLError objects.
		_ = writeServerMessage(ctx, conn, map[string]any{
			"id":      subID,
			"type":    "error",
			"payload": []map[string]any{{"message": "subscription denied", "extensions": map[string]any{"code": "FORBIDDEN"}}},
		})
	})
	defer srv.Close()

	executor := NewExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	if err != nil {
		t.Fatalf("ExecuteBinding: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 stream event (the error), got %d", len(events))
	}
	if events[0].Error == nil {
		t.Fatal("expected stream error event")
	}
	if !strings.Contains(events[0].Error.Message, "subscription denied") {
		t.Errorf("error message = %q, want to contain 'subscription denied'", events[0].Error.Message)
	}
}

// TestIntegrationSubscription_ConnectionDropMidStream verifies that when the
// server abruptly closes the WebSocket after sending one event, the executor
// emits a stream error and closes the channel cleanly.
func TestIntegrationSubscription_ConnectionDropMidStream(t *testing.T) {
	srv := subscriptionErrorTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		if expectClientMessage(ctx, conn, "connection_init") != nil {
			return
		}
		_ = writeServerMessage(ctx, conn, map[string]any{"type": "connection_ack"})
		subID, err := expectClientSubscribe(ctx, conn)
		if err != nil {
			return
		}
		_ = writeServerMessage(ctx, conn, map[string]any{
			"id":   subID,
			"type": "next",
			"payload": map[string]any{
				"data": map[string]any{"messageStream": map[string]any{"id": "1", "body": "first"}},
			},
		})
		// Abruptly close with an abnormal status to simulate a connection drop.
		_ = conn.Close(websocket.StatusInternalError, "simulated server crash")
	})
	defer srv.Close()

	executor := NewExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	if err != nil {
		t.Fatalf("ExecuteBinding: %v", err)
	}

	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	// Expect at least one data event (the first message) and one error event
	// for the abnormal close. The exact ordering and count depends on timing,
	// but the channel must close cleanly.
	if len(events) < 1 {
		t.Fatal("expected at least one event before the connection dropped")
	}
	hasError := false
	for _, ev := range events {
		if ev.Error != nil {
			hasError = true
		}
	}
	if !hasError {
		t.Error("expected a stream error event for the abnormal close")
	}
}

// TestIntegrationSubscription_ConnectionAckTimeout verifies that when the
// server replies with the wrong message type instead of connection_ack,
// the executor returns an error rather than hanging indefinitely.
func TestIntegrationSubscription_ConnectionAckMismatch(t *testing.T) {
	srv := subscriptionErrorTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		if expectClientMessage(ctx, conn, "connection_init") != nil {
			return
		}
		// Send the wrong message type instead of connection_ack.
		_ = writeServerMessage(ctx, conn, map[string]any{"type": "ka"})
	})
	defer srv.Close()

	executor := NewExecutor()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	// The connection_ack failure surfaces either as a returned error from
	// ExecuteBinding (precondition failure) or as the first stream event
	// being an error. Either path is acceptable; both prove the executor
	// doesn't hang.
	if err != nil {
		// Returned error is fine.
		return
	}
	deadline := time.After(5 * time.Second)
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed without an event")
		}
		if ev.Error == nil {
			t.Fatal("expected stream error for connection_ack mismatch, got data event")
		}
	case <-deadline:
		t.Fatal("ExecuteBinding hung waiting for connection_ack")
	}
}

// TestIntegrationCreateInterface_RecursiveType verifies that the GraphQL
// creator's selection-set generation does not infinite-loop on a recursive
// type. The schema declares Node { id, parent: Node }, which is the canonical
// shape that breaks naive selection set generators.
func TestIntegrationCreateInterface_RecursiveType(t *testing.T) {
	recursiveSchema := introspectionSchema{
		QueryType: &typeRef{Name: "Query"},
		Types: []fullType{
			{
				Kind: "OBJECT",
				Name: "Query",
				Fields: []field{
					{Name: "node", Args: []inputValue{{Name: "id", Type: typeRef{Kind: "NON_NULL", OfType: &typeRef{Kind: "SCALAR", Name: "ID"}}}}, Type: typeRef{Kind: "OBJECT", Name: "Node"}},
				},
			},
			{
				Kind: "OBJECT",
				Name: "Node",
				Fields: []field{
					{Name: "id", Type: typeRef{Kind: "SCALAR", Name: "ID"}},
					{Name: "name", Type: typeRef{Kind: "SCALAR", Name: "String"}},
					// Self-referential field — depth-limited selection generator must not loop.
					{Name: "parent", Type: typeRef{Kind: "OBJECT", Name: "Node"}},
					{Name: "children", Type: typeRef{Kind: "LIST", OfType: &typeRef{Kind: "OBJECT", Name: "Node"}}},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"__schema": recursiveSchema},
		})
	}))
	defer srv.Close()

	creator := NewCreator()
	done := make(chan struct{})
	var iface *openbindings.Interface
	var createErr error

	go func() {
		defer close(done)
		iface, createErr = creator.CreateInterface(context.Background(), &openbindings.CreateInput{
			Sources: []openbindings.CreateSource{{Format: "graphql", Location: srv.URL}},
		})
	}()

	select {
	case <-done:
		// Good — finished without hanging.
	case <-time.After(5 * time.Second):
		t.Fatal("CreateInterface hung on recursive schema (cycle safety failure)")
	}

	if createErr != nil {
		t.Fatalf("CreateInterface: %v", createErr)
	}
	if iface == nil {
		t.Fatal("CreateInterface returned nil interface")
	}
	op, ok := iface.Operations["node"]
	if !ok {
		t.Fatal("expected operation 'node' in created interface")
	}
	// The input schema must contain a _query const that the executor can use.
	props, _ := op.Input["properties"].(map[string]any)
	queryProp, _ := props["_query"].(map[string]any)
	if queryProp == nil {
		t.Fatal("node input schema missing _query property")
	}
	queryStr, _ := queryProp["const"].(string)
	if queryStr == "" {
		t.Fatal("_query const is empty")
	}
	// The generated query MUST select at least the scalar fields, and MUST
	// NOT recurse infinitely into the parent field.
	if !strings.Contains(queryStr, "id") {
		t.Errorf("generated query missing 'id' field: %s", queryStr)
	}
	// A naive infinite loop would produce a query thousands of characters long.
	// A depth-limited generator should produce something reasonable.
	if len(queryStr) > 5000 {
		t.Errorf("generated query is suspiciously long (%d chars), suggests cycle was not bounded: %s", len(queryStr), queryStr[:200])
	}
}
