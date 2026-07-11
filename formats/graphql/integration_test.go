package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"nhooyr.io/websocket"
)

// ---------------------------------------------------------------------------
// Handle-driving helpers
// ---------------------------------------------------------------------------

// driveOutputs writes input (when non-nil), closes the input side, and
// collects every output until clean close or a terminal error.
func driveOutputs(ctx context.Context, call openbindings.Invocation[any, any], input any) ([]any, *openbindings.InvocationError) {
	if input != nil {
		_ = call.Write(ctx, input)
	}
	_ = call.Close()
	out := call.Outputs()
	var vals []any
	for {
		v, err := out.Read(ctx)
		if errors.Is(err, io.EOF) {
			return vals, nil
		}
		if err != nil {
			var ie *openbindings.InvocationError
			errors.As(err, &ie)
			return vals, ie
		}
		vals = append(vals, v)
	}
}

// driveSingle is the canonical unary pattern (write, close, Single): unlike
// draining, Single asserts exactly-one, so a zero- or multi-output unary
// invocation fails the test instead of passing silently.
func driveSingle(t *testing.T, call openbindings.Invocation[any, any], input any) (any, *openbindings.InvocationError) {
	t.Helper()
	ctx := context.Background()
	if input != nil {
		_ = call.Write(ctx, input)
	}
	_ = call.Close()
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		var ie *openbindings.InvocationError
		if !errors.As(err, &ie) {
			t.Fatalf("expected *InvocationError, got %T: %v", err, err)
		}
		return nil, ie
	}
	return out, nil
}

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
// and query/mutation invocation.
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

func TestIntegrationInvokeQuery(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Query/user",
	})
	out, ierr := driveSingle(t, call, map[string]any{"id": "42"})
	if ierr != nil {
		t.Fatalf("stream error: %s: %s", ierr.Code, ierr.Message)
	}
	data, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", out)
	}
	if data["id"] != "42" {
		t.Errorf("user id = %v, want 42", data["id"])
	}
	if data["name"] != "Alice" {
		t.Errorf("user name = %v, want Alice", data["name"])
	}
}

func TestIntegrationInvokeQueryNoArgs(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Query/users",
	})
	out, ierr := driveSingle(t, call, nil)
	if ierr != nil {
		t.Fatalf("stream error: %s: %s", ierr.Code, ierr.Message)
	}
	data, ok := out.([]any)
	if !ok {
		t.Fatalf("expected array data, got %T", out)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 users, got %d", len(data))
	}
}

func TestIntegrationInvokeMutation(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Mutation/createUser",
	})
	out, ierr := driveSingle(t, call, map[string]any{"name": "Charlie", "email": "charlie@example.com"})
	if ierr != nil {
		t.Fatalf("stream error: %s: %s", ierr.Code, ierr.Message)
	}
	data, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", out)
	}
	if data["name"] != "Charlie" {
		t.Errorf("user name = %v, want Charlie", data["name"])
	}
	if data["id"] != "new-123" {
		t.Errorf("user id = %v, want new-123", data["id"])
	}
}

func TestIntegrationInvokeWithSchemaQuery(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	// Simulate the OperationInvoker passing through an InputSchema with a
	// prebuilt _query const (no introspection needed).
	inputSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":     map[string]any{"type": "string"},
			"_query": map[string]any{"type": "string", "const": "query($id: ID!) { user(id: $id) { id name } }"},
		},
	}

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:         "Query/user",
		InputSchema: inputSchema,
	})
	out, ierr := driveSingle(t, call, map[string]any{"id": "99"})
	if ierr != nil {
		t.Fatalf("stream error: %s: %s", ierr.Code, ierr.Message)
	}
	data, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", out)
	}
	if data["id"] != "99" {
		t.Errorf("user id = %v, want 99", data["id"])
	}
}

func TestIntegrationSynthesizeInterface(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	synthesizer := NewSynthesizer()
	ctx := context.Background()

	iface, err := synthesizer.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			Format:   "graphql",
			Location: srv.URL,
		}},
	})
	if err != nil {
		t.Fatalf("SynthesizeInterface error: %v", err)
	}
	if _, ok := iface.Operations["user"]; !ok {
		t.Error("expected operation 'user'")
	}
	if _, ok := iface.Operations["createUser"]; !ok {
		t.Error("expected operation 'createUser'")
	}
}

func TestIntegrationSourceContent(t *testing.T) {
	// The invoker uses inline Source.Content instead of a network introspection.
	srv := newTestServer()
	defer srv.Close()

	schemaJSON, _ := json.Marshal(map[string]any{
		"data": map[string]any{"__schema": testSchema},
	})

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL, Content: string(schemaJSON)},
		Ref:    "Query/users",
	})
	out, ierr := driveSingle(t, call, nil)
	if ierr != nil {
		t.Fatalf("stream error: %s: %s", ierr.Code, ierr.Message)
	}
	data, ok := out.([]any)
	if !ok {
		t.Fatalf("expected array data, got %T", out)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 users, got %d", len(data))
	}
}

// authServer requires a bearer token on query calls (introspection is open).
func authServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "__schema") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"__schema": testSchema}})
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"errors":[{"message":"unauthorized"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"users": []any{map[string]any{"id": "1", "name": "Alice"}}},
		})
	}))
}

// TestIntegrationNoCredentials verifies that a 401 from the service (no
// credentials in context) surfaces as a terminal ERR_AUTH_REQUIRED. GraphQL
// declares no in-band security, so the challenge is the server's, not a
// pre-dispatch CONTEXT_REQUIRED.
func TestIntegrationNoCredentials(t *testing.T) {
	srv := authServer()
	defer srv.Close()

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Query/users",
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeAuthRequired {
		t.Fatalf("expected ERR_AUTH_REQUIRED, got %v", ierr)
	}
}

// TestIntegrationBearerContext verifies that a bearerToken supplied in the
// binding context is applied as an Authorization header and the call succeeds.
func TestIntegrationBearerContext(t *testing.T) {
	srv := authServer()
	defer srv.Close()

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:     "Query/users",
		Context: map[string]any{"bearerToken": "test-token"},
	})
	out, ierr := driveSingle(t, call, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	if _, ok := out.([]any); !ok {
		t.Fatalf("expected array data, got %T", out)
	}
}

// newSubscriptionTestServer creates an httptest server that speaks the
// graphql-transport-ws subprotocol and serves a single subscription
// (`messageStream`) that emits three events and then a "complete" message.
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
		if r.Header.Get("Upgrade") == "websocket" {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				Subprotocols: []string{"graphql-transport-ws"},
			})
			if err != nil {
				t.Logf("websocket accept failed: %v", err)
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "test done")
			runSubscriptionExchange(t, r.Context(), conn)
			return
		}

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

func runSubscriptionExchange(t *testing.T, ctx context.Context, conn *websocket.Conn) {
	t.Helper()

	if err := expectClientMessage(ctx, conn, "connection_init"); err != nil {
		t.Logf("server: %v", err)
		return
	}
	if err := writeServerMessage(ctx, conn, map[string]any{"type": "connection_ack"}); err != nil {
		t.Logf("server: %v", err)
		return
	}
	subID, err := expectClientSubscribe(ctx, conn)
	if err != nil {
		t.Logf("server: %v", err)
		return
	}
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

// TestIntegrationInvokeSubscription verifies that the GraphQL invoker opens a
// graphql-transport-ws WebSocket, subscribes, and emits each "next" payload as
// a separate output, closing cleanly on "complete".
func TestIntegrationInvokeSubscription(t *testing.T) {
	srv := newSubscriptionTestServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	events, ierr := driveOutputs(ctx, call, map[string]any{"topic": "alerts"})
	if ierr != nil {
		t.Fatalf("unexpected stream error: %s: %s", ierr.Code, ierr.Message)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 stream events from subscription, got %d", len(events))
	}
	for i, ev := range events {
		data, ok := ev.(map[string]any)
		if !ok {
			t.Fatalf("event %d: expected map data, got %T", i, ev)
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

// TestIntegrationSubscriptionCancellation verifies that cancelling the
// invocation closes the WebSocket cleanly.
func TestIntegrationSubscriptionCancellation(t *testing.T) {
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
			_ = writeServerMessage(ctx, conn, map[string]any{
				"id":      subID,
				"type":    "next",
				"payload": map[string]any{"data": map[string]any{"messageStream": map[string]any{"id": "1", "body": "first"}}},
			})
			<-ctx.Done()
			return
		}
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

	ctx, cancel := context.WithCancel(context.Background())
	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	_ = call.Close()

	out := call.Outputs()
	if _, err := out.Read(context.Background()); err != nil {
		t.Fatalf("first event error: %v", err)
	}

	cancel()
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := out.Read(context.Background()); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case <-done:
		// Stream terminated (EOF or ERR_CANCELLED) — no hang.
	case <-time.After(3 * time.Second):
		t.Fatal("subscription stream did not terminate within 3s of cancellation")
	}
}

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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"__schema": schema},
		})
	}))
}

// TestIntegrationSubscription_ErrorMessage verifies that a server "error"
// message terminates the invocation with ERR_EXECUTION_FAILED carrying the
// message.
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
		_ = writeServerMessage(ctx, conn, map[string]any{
			"id":      subID,
			"type":    "error",
			"payload": []map[string]any{{"message": "subscription denied", "extensions": map[string]any{"code": "FORBIDDEN"}}},
		})
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	events, ierr := driveOutputs(ctx, call, nil)
	if len(events) != 0 {
		t.Fatalf("expected no outputs before the error, got %d", len(events))
	}
	if ierr == nil || ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("expected ERR_EXECUTION_FAILED, got %v", ierr)
	}
	if !strings.Contains(ierr.Message, "subscription denied") {
		t.Errorf("error message = %q, want to contain 'subscription denied'", ierr.Message)
	}
}

// TestIntegrationSubscription_ConnectionDropMidStream verifies that an abrupt
// WebSocket close after one event delivers the event then a terminal stream
// error.
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
		_ = conn.Close(websocket.StatusInternalError, "simulated server crash")
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	events, ierr := driveOutputs(ctx, call, nil)
	if len(events) < 1 {
		t.Fatal("expected at least the first event before the connection dropped")
	}
	if ierr == nil || ierr.Code != openbindings.ErrCodeStreamError {
		t.Fatalf("expected a terminal ERR_STREAM_ERROR for the abnormal close, got %v", ierr)
	}
}

// TestIntegrationSubscription_ConnectionAckMismatch verifies that a wrong
// handshake reply terminates the invocation rather than hanging.
func TestIntegrationSubscription_ConnectionAckMismatch(t *testing.T) {
	srv := subscriptionErrorTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		if expectClientMessage(ctx, conn, "connection_init") != nil {
			return
		}
		_ = writeServerMessage(ctx, conn, map[string]any{"type": "ka"})
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	done := make(chan *openbindings.InvocationError, 1)
	go func() {
		_, ierr := driveSingle(t, call, nil)
		done <- ierr
	}()
	select {
	case ierr := <-done:
		if ierr == nil {
			t.Fatal("expected a terminal error for connection_ack mismatch")
		}
		if ierr.Code != openbindings.ErrCodeConnectFailed {
			t.Errorf("error code = %q, want %q", ierr.Code, openbindings.ErrCodeConnectFailed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("invocation hung waiting for connection_ack")
	}
}

// TestIntegrationSynthesizeInterface_RecursiveType verifies that selection-set
// generation does not infinite-loop on a recursive type.
func TestIntegrationSynthesizeInterface_RecursiveType(t *testing.T) {
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

	synthesizer := NewSynthesizer()
	done := make(chan struct{})
	var iface *openbindings.Interface
	var createErr error

	go func() {
		defer close(done)
		iface, createErr = synthesizer.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
			Sources: []openbindings.SynthesizeSource{{Format: "graphql", Location: srv.URL}},
		})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SynthesizeInterface hung on recursive schema (cycle safety failure)")
	}

	if createErr != nil {
		t.Fatalf("SynthesizeInterface: %v", createErr)
	}
	if iface == nil {
		t.Fatal("SynthesizeInterface returned nil interface")
	}
	op, ok := iface.Operations["node"]
	if !ok {
		t.Fatal("expected operation 'node' in created interface")
	}
	props, _ := op.Input["properties"].(map[string]any)
	queryProp, _ := props["_query"].(map[string]any)
	if queryProp == nil {
		t.Fatal("node input schema missing _query property")
	}
	queryStr, _ := queryProp["const"].(string)
	if queryStr == "" {
		t.Fatal("_query const is empty")
	}
	if !strings.Contains(queryStr, "id") {
		t.Errorf("generated query missing 'id' field: %s", queryStr)
	}
	if len(queryStr) > 5000 {
		t.Errorf("generated query is suspiciously long (%d chars): %s", len(queryStr), queryStr[:200])
	}
}

// TestNewInvokerWithClient verifies that an Invoker created with a custom
// HTTP client uses that client for outbound requests (inline schema content
// skips introspection, so the single request is the query POST).
func TestNewInvokerWithClient(t *testing.T) {
	var requestCount int
	custom := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requestCount++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":{"users":[{"id":"1","name":"Ada"}]}}`)),
			Request:    req,
		}, nil
	})}

	schemaJSON, _ := json.Marshal(map[string]any{"data": map[string]any{"__schema": testSchema}})
	call := NewInvokerWithClient(custom).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: "http://example.test/graphql", Content: string(schemaJSON)},
		Ref:    "Query/users",
	})
	out, ierr := driveSingle(t, call, nil)
	if ierr != nil {
		t.Fatalf("stream error: %s: %s", ierr.Code, ierr.Message)
	}
	if requestCount != 1 {
		t.Errorf("expected custom transport to be called exactly once, got %d", requestCount)
	}
	if _, ok := out.([]any); !ok {
		t.Errorf("expected list output through the custom client, got %T", out)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------------------
// B3.5 format-parity batch
// ---------------------------------------------------------------------------

// TestIntegrationInvokeMutation_Accepts2xxStatus verifies that a 201 Created
// response with a valid GraphQL body succeeds — GraphQL-over-HTTP responses
// are legitimately any 2xx (TS parity; TS already accepts any 2xx).
func TestIntegrationInvokeMutation_Accepts2xxStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "__schema") {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"__schema": testSchema}})
			return
		}
		// A 201 Created is a legitimate GraphQL-over-HTTP success status.
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"createUser": map[string]any{"id": "new-123", "name": "Charlie", "email": "charlie@example.com"}},
		})
	}))
	defer srv.Close()

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Mutation/createUser",
	})
	out, ierr := driveSingle(t, call, map[string]any{"name": "Charlie", "email": "charlie@example.com"})
	if ierr != nil {
		t.Fatalf("expected a 201 response to classify as success, got stream error: %s: %s", ierr.Code, ierr.Message)
	}
	data, ok := out.(map[string]any)
	if !ok || data["id"] != "new-123" {
		t.Fatalf("expected createUser data, got %#v", out)
	}
}

// TestIntegrationInvokeQuery_OversizedResponseRefused verifies the Go
// response-size cap (10MB) refuses an oversized query response rather than
// buffering it unbounded.
func TestIntegrationInvokeQuery_OversizedResponseRefused(t *testing.T) {
	huge := strings.Repeat("x", int(maxResponseBytes)+16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ping":"`))
		_, _ = w.Write([]byte(huge))
		_, _ = w.Write([]byte(`"}}`))
	}))
	defer srv.Close()

	// A prebuilt _query const skips introspection entirely, isolating the
	// cap check to the query-dispatch response.
	inputSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"_query": map[string]any{"type": "string", "const": "query { ping }"}},
	}
	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:         "Query/ping",
		InputSchema: inputSchema,
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil {
		t.Fatal("expected an error for an oversized response, got none")
	}
	if ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Errorf("error code = %q, want %q", ierr.Code, openbindings.ErrCodeExecutionFailed)
	}
	if !strings.Contains(ierr.Message, "byte limit") {
		t.Errorf("error message = %q, want to contain %q", ierr.Message, "byte limit")
	}
}

// TestIntegrationInvokeQueryNoArgs_NoInputRecipe verifies the no-input
// recipe for a zero-argument field resolved via introspection: the caller
// never writes nor closes, so the binding itself must close input before
// reading — otherwise ReadInput parks forever (the operation-layer no-input
// convention every other format honors via a schema/args-driven fallback;
// TS parity).
func TestIntegrationInvokeQueryNoArgs_NoInputRecipe(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Query/users",
	})
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		t.Fatalf("no-input recipe should dispatch without a caller write/close, got: %v", err)
	}
	data, ok := out.([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("expected 2 users, got %#v", out)
	}
}

// TestIntegrationInvokeQuery_PrebuiltQueryNoVariables_NoInputRecipe is the
// prebuilt-_query counterpart: an input schema whose only property is
// `_query` (schemaDeclaresVariables == false) must also dispatch without a
// caller write/close.
func TestIntegrationInvokeQuery_PrebuiltQueryNoVariables_NoInputRecipe(t *testing.T) {
	srv := newTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	inputSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"_query": map[string]any{"type": "string", "const": "query { users { id name email } }"}},
	}
	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:         "Query/users",
		InputSchema: inputSchema,
	})
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		t.Fatalf("no-input recipe should dispatch without a caller write/close, got: %v", err)
	}
	data, ok := out.([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("expected 2 users, got %#v", out)
	}
}

// TestIntegrationInvokeBinding_MissingLocation verifies a pre-dispatch
// missing-location check fires a clean ErrCodeSourceLoadFailed before any
// network I/O, matching the TS invoker's precheck.
func TestIntegrationInvokeBinding_MissingLocation(t *testing.T) {
	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql"}, // no Location
		Ref:    "Query/ping",
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil {
		t.Fatal("expected an error for a missing location, got none")
	}
	if ierr.Code != openbindings.ErrCodeSourceLoadFailed {
		t.Errorf("error code = %q, want %q", ierr.Code, openbindings.ErrCodeSourceLoadFailed)
	}
	const want = "GraphQL source requires a location (endpoint URL)"
	if ierr.Message != want {
		t.Errorf("error message = %q, want %q", ierr.Message, want)
	}
}

// TestIntegrationSubscription_CloseWithoutComplete verifies that a CLEAN
// WebSocket close (StatusNormalClosure) without a terminal `complete` frame
// still surfaces as a terminal error with a stable, library-independent
// message — a silent success here would mask a server that vanished
// mid-subscription (TS parity: "WebSocket closed before subscription
// complete").
func TestIntegrationSubscription_CloseWithoutComplete(t *testing.T) {
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
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	events, ierr := driveOutputs(ctx, call, nil)
	if len(events) < 1 {
		t.Fatal("expected at least the first event before the connection closed")
	}
	if ierr == nil || ierr.Code != openbindings.ErrCodeStreamError {
		t.Fatalf("expected a terminal ERR_STREAM_ERROR for the close-without-complete, got %v", ierr)
	}
	const want = "WebSocket closed before subscription complete"
	if ierr.Message != want {
		t.Errorf("error message = %q, want %q", ierr.Message, want)
	}
}

// TestIntegrationSubscription_ClosedDuringHandshake verifies a WebSocket
// close BEFORE connection_ack surfaces a stable, library-independent
// ERR_CONNECT_FAILED message rather than a leaked transport error string
// (TS parity: "WebSocket closed during handshake").
func TestIntegrationSubscription_ClosedDuringHandshake(t *testing.T) {
	srv := subscriptionErrorTestServer(t, func(ctx context.Context, conn *websocket.Conn) {
		// Close immediately, before ever reading connection_init or
		// replying with connection_ack.
		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: "graphql", Location: srv.URL},
		Ref:    "Subscription/messageStream",
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeConnectFailed {
		t.Fatalf("expected ERR_CONNECT_FAILED for a handshake-phase close, got %v", ierr)
	}
	const want = "WebSocket closed during handshake"
	if ierr.Message != want {
		t.Errorf("error message = %q, want %q", ierr.Message, want)
	}
}
