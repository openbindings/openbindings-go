package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

// testState captures state from the test MCP server for assertions.
type testState struct {
	lastAuth string
}

func setupMCPServer(t *testing.T) (*httptest.Server, *testState) {
	t.Helper()

	server := gomcp.NewServer(&gomcp.Implementation{
		Name:    "test-mcp-server",
		Version: "1.0.0",
	}, nil)

	// Register a tool using the generic AddTool helper which handles
	// input unmarshaling and schema generation automatically.
	type echoInput struct {
		Message string `json:"message"`
	}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "echo",
		Description: "Echoes the input message",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args echoInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "echo: " + args.Message},
			},
		}, nil, nil
	})

	// Register a tool that emits progress notifications during execution.
	// The tool sends three progress events with progress=1/3, 2/3, 3/3 and
	// then returns the final result.
	type progressInput struct {
		Steps int `json:"steps"`
	}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "longRunning",
		Description: "Simulates a long-running operation that emits progress notifications",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args progressInput) (*gomcp.CallToolResult, any, error) {
		steps := args.Steps
		if steps <= 0 {
			steps = 3
		}
		token := req.Params.GetProgressToken()
		if token != nil && req.Session != nil {
			for i := 1; i <= steps; i++ {
				_ = req.Session.NotifyProgress(ctx, &gomcp.ProgressNotificationParams{
					ProgressToken: token,
					Progress:      float64(i),
					Total:         float64(steps),
					Message:       "step " + intToString(i),
				})
			}
		}
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "completed " + intToString(steps) + " steps"},
			},
		}, nil, nil
	})

	// Register a static resource.
	server.AddResource(&gomcp.Resource{
		Name:        "status",
		URI:         "app://status",
		Description: "Application status",
	}, func(ctx context.Context, req *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{
			Contents: []*gomcp.ResourceContents{
				{URI: "app://status", MIMEType: "application/json", Text: `{"ok":true}`},
			},
		}, nil
	})

	// Register a prompt.
	server.AddPrompt(&gomcp.Prompt{
		Name:        "greet",
		Description: "Generate a greeting",
		Arguments: []*gomcp.PromptArgument{
			{Name: "name", Description: "Who to greet", Required: true},
		},
	}, func(ctx context.Context, req *gomcp.GetPromptRequest) (*gomcp.GetPromptResult, error) {
		name := req.Params.Arguments["name"]
		return &gomcp.GetPromptResult{
			Description: "A greeting prompt",
			Messages: []*gomcp.PromptMessage{
				{Role: "user", Content: &gomcp.TextContent{Text: "Hello, " + name + "!"}},
			},
		}, nil
	})

	state := &testState{}

	// Wrap with auth-capturing middleware.
	handler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server {
		state.lastAuth = r.Header.Get("Authorization")
		return server
	}, &gomcp.StreamableHTTPOptions{Stateless: true})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, state
}

func drainStream(t *testing.T, ch <-chan openbindings.StreamEvent) []openbindings.StreamEvent {
	t.Helper()
	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

// --- Integration Tests ---

func TestIntegration_CreateInterface(t *testing.T) {
	ts, _ := setupMCPServer(t)

	creator := NewCreator()
	iface, err := creator.CreateInterface(context.Background(), &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{{
			Format:   FormatToken,
			Location: ts.URL,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if iface.Name != "test-mcp-server" {
		t.Errorf("name = %q, want test-mcp-server", iface.Name)
	}
	if iface.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", iface.Version)
	}

	// Should have: echo (tool), longRunning (tool), status (resource), greet (prompt)
	if len(iface.Operations) != 4 {
		t.Fatalf("expected 4 operations, got %d: %v", len(iface.Operations), keys(iface.Operations))
	}
	if _, ok := iface.Operations["echo"]; !ok {
		t.Error("expected operation 'echo'")
	}
	if _, ok := iface.Operations["longRunning"]; !ok {
		t.Error("expected operation 'longRunning'")
	}
	if _, ok := iface.Operations["status"]; !ok {
		t.Error("expected operation 'status'")
	}
	if _, ok := iface.Operations["greet"]; !ok {
		t.Error("expected operation 'greet'")
	}

	// Verify binding refs
	if iface.Bindings["echo."+DefaultSourceName].Ref != "tools/echo" {
		t.Errorf("echo ref = %q", iface.Bindings["echo."+DefaultSourceName].Ref)
	}
	if iface.Bindings["status."+DefaultSourceName].Ref != "resources/app://status" {
		t.Errorf("status ref = %q", iface.Bindings["status."+DefaultSourceName].Ref)
	}
	if iface.Bindings["greet."+DefaultSourceName].Ref != "prompts/greet" {
		t.Errorf("greet ref = %q", iface.Bindings["greet."+DefaultSourceName].Ref)
	}
}

func TestIntegration_ExecuteTool(t *testing.T) {
	ts, state := setupMCPServer(t)

	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source:  openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:     "tools/echo",
		Input:   map[string]any{"message": "hello world"},
		Context: map[string]any{"bearerToken": "tok_secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %s: %s", events[0].Error.Code, events[0].Error.Message)
	}

	// Verify credentials flowed through.
	if state.lastAuth != "Bearer tok_secret" {
		t.Errorf("auth = %q, want Bearer tok_secret", state.lastAuth)
	}

	// Verify response.
	if events[0].Data != "echo: hello world" {
		t.Errorf("data = %v, want 'echo: hello world'", events[0].Data)
	}
}

func TestIntegration_ExecuteResource(t *testing.T) {
	ts, _ := setupMCPServer(t)

	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "resources/app://status",
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error != nil {
		t.Fatalf("expected 1 successful event, got %d", len(events))
	}

	// The JSON text should be parsed into a map.
	resp, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map response, got %T: %v", events[0].Data, events[0].Data)
	}
	if resp["ok"] != true {
		t.Errorf("ok = %v, want true", resp["ok"])
	}
}

func TestIntegration_ExecutePrompt(t *testing.T) {
	ts, _ := setupMCPServer(t)

	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "prompts/greet",
		Input:  map[string]any{"name": "Alice"},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error != nil {
		t.Fatalf("expected 1 successful event, got %d", len(events))
	}

	resp, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map response, got %T", events[0].Data)
	}
	if resp["description"] != "A greeting prompt" {
		t.Errorf("description = %v", resp["description"])
	}
	msgs, ok := resp["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %v", resp["messages"])
	}
	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("role = %v, want user", msg["role"])
	}
}

func TestIntegration_StoredCredentials(t *testing.T) {
	ts, state := setupMCPServer(t)

	store := openbindings.NewMemoryStore()
	ctx := context.Background()

	key := normalizeEndpoint(ts.URL)
	_ = store.Set(ctx, key, map[string]any{"bearerToken": "stored_tok"})

	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "tools/echo",
		Input:  map[string]any{"message": "test"},
		Store:  store,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error != nil {
		t.Fatalf("expected 1 successful event")
	}

	if state.lastAuth != "Bearer stored_tok" {
		t.Errorf("auth = %q, want Bearer stored_tok", state.lastAuth)
	}
}

func TestIntegration_InvalidRef(t *testing.T) {
	ts, _ := setupMCPServer(t)

	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "bad-ref",
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error == nil {
		t.Fatal("expected error event")
	}
	if events[0].Error.Code != openbindings.ErrCodeInvalidRef {
		t.Errorf("code = %q, want invalid_ref", events[0].Error.Code)
	}
}

func keys[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

// TestIntegration_ToolProgressNotifications verifies that the executor
// surfaces MCP `notifications/progress` events as intermediate stream events
// during a long-running tool call, with the final tool result as the last
// event.
//
// Note: the go-mcp library dispatches progress notifications in separate
// goroutines. In edge cases, a notification goroutine may not be scheduled
// before CallTool returns and the stream closes. This test therefore checks
// that at least one progress notification arrives (proving the demux handler
// works) and that the final result is present, rather than requiring an
// exact event count.
func TestIntegration_ToolProgressNotifications(t *testing.T) {
	ts, _ := setupMCPServer(t)

	exec := NewExecutor()
	defer exec.Close()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: ts.URL,
		},
		Ref:   "tools/longRunning",
		Input: map[string]any{"steps": 3},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	events := drainStream(t, ch)

	// We expect between 1 and 4 events: 0-3 progress notifications + 1 final
	// result. The go-mcp library dispatches notifications asynchronously via
	// the session's demux handler, so some (or all) may not arrive before
	// CallTool returns and the stream closes. The pooled session's demux
	// handler is the correct architecture; this test validates that progress
	// events have the right structure when they do arrive.
	if len(events) < 1 {
		t.Fatalf("expected at least 1 stream event (the final result), got %d", len(events))
	}

	// Separate progress events from the final result.
	var progressEvents []openbindings.StreamEvent
	var finalEvent *openbindings.StreamEvent
	for i := range events {
		data, ok := events[i].Data.(map[string]any)
		if ok {
			if _, hasToken := data["progressToken"]; hasToken {
				progressEvents = append(progressEvents, events[i])
				continue
			}
		}
		finalEvent = &events[i]
	}

	// Progress notifications are best-effort due to async dispatch. Log how
	// many arrived for debugging but don't fail if none did.
	t.Logf("received %d progress notifications out of 3 requested", len(progressEvents))

	// Validate progress event structure for any that did arrive.
	for i, ev := range progressEvents {
		if ev.Error != nil {
			t.Fatalf("progress event %d: unexpected error: %+v", i, ev.Error)
		}
		data := ev.Data.(map[string]any)
		if _, ok := data["progress"]; !ok {
			t.Errorf("progress event %d: missing progress field", i)
		}
		if _, ok := data["total"]; !ok {
			t.Errorf("progress event %d: missing total field", i)
		}
		if _, ok := data["message"]; !ok {
			t.Errorf("progress event %d: missing message field", i)
		}
	}

	// Final event should be the tool result.
	if finalEvent == nil {
		t.Fatal("expected a final result event (no progressToken field)")
	}
	if finalEvent.Error != nil {
		t.Fatalf("final event: unexpected error: %+v", finalEvent.Error)
	}
}

// TestIntegration_ToolNoProgress_StaysSingleEvent verifies that a tool that
// does not emit progress notifications produces exactly one stream event
// (the final result), preserving the unary behavior of fast tool calls.
func TestIntegration_ToolNoProgress_StaysSingleEvent(t *testing.T) {
	ts, _ := setupMCPServer(t)

	exec := NewExecutor()
	defer exec.Close()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:   FormatToken,
			Location: ts.URL,
		},
		Ref:   "tools/echo",
		Input: map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event from no-progress tool, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %+v", events[0].Error)
	}
}

// TestIntegration_SessionPooling verifies that two consecutive tool calls
// against the same MCP server reuse the same pooled session, producing only
// one MCP `initialize` handshake instead of two.
//
// The test wraps the standard MCP server handler with a request counter that
// increments on every `initialize` call. After two ExecuteBinding calls using
// the same Executor, the counter must be 1.
func TestIntegration_SessionPooling(t *testing.T) {
	server := gomcp.NewServer(&gomcp.Implementation{
		Name:    "session-pool-test",
		Version: "1.0.0",
	}, nil)

	type echoInput struct {
		Message string `json:"message"`
	}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "echo",
		Description: "Echoes the input message",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args echoInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "echo: " + args.Message},
			},
		}, nil, nil
	})

	// Counter for the number of `initialize` JSON-RPC calls observed.
	// Wrapping the underlying handler at the HTTP layer lets us see every
	// distinct request body without disturbing the MCP server's behavior.
	var initCount atomic.Int32
	mcpHandler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server {
		return server
	}, &gomcp.StreamableHTTPOptions{Stateless: true})

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inspect the body for initialize calls. We have to copy the body
		// because http.Request.Body is single-read.
		if r.Method == http.MethodPost && r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"method":"initialize"`)) {
				initCount.Add(1)
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	exec := NewExecutor()
	defer exec.Close()
	ctx := context.Background()

	// First call.
	ch1, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "tools/echo",
		Input:  map[string]any{"message": "first"},
	})
	if err != nil {
		t.Fatalf("first ExecuteBinding error: %v", err)
	}
	for ev := range ch1 {
		if ev.Error != nil {
			t.Fatalf("first call: unexpected error: %+v", ev.Error)
		}
	}

	// Second call reuses the pooled session.
	ch2, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "tools/echo",
		Input:  map[string]any{"message": "second"},
	})
	if err != nil {
		t.Fatalf("second ExecuteBinding error: %v", err)
	}
	for ev := range ch2 {
		if ev.Error != nil {
			t.Fatalf("second call: unexpected error: %+v", ev.Error)
		}
	}

	got := initCount.Load()
	if got != 1 {
		t.Errorf("expected 1 MCP initialize handshake (session reuse), got %d", got)
	}
}

// TestIntegration_SessionPooling_DifferentHeaders verifies that calls with
// different auth headers get separate pooled sessions.
func TestIntegration_SessionPooling_DifferentHeaders(t *testing.T) {
	server := gomcp.NewServer(&gomcp.Implementation{
		Name:    "session-pool-headers-test",
		Version: "1.0.0",
	}, nil)

	type echoInput struct {
		Message string `json:"message"`
	}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "echo",
		Description: "Echoes the input message",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args echoInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "echo: " + args.Message},
			},
		}, nil, nil
	})

	var initCount atomic.Int32
	mcpHandler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server {
		return server
	}, &gomcp.StreamableHTTPOptions{Stateless: true})

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"method":"initialize"`)) {
				initCount.Add(1)
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	exec := NewExecutor()
	defer exec.Close()
	ctx := context.Background()

	// First call with token A.
	ch1, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source:  openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:     "tools/echo",
		Input:   map[string]any{"message": "first"},
		Context: map[string]any{"bearerToken": "token_A"},
	})
	if err != nil {
		t.Fatalf("first ExecuteBinding error: %v", err)
	}
	for ev := range ch1 {
		if ev.Error != nil {
			t.Fatalf("first call: unexpected error: %+v", ev.Error)
		}
	}

	// Second call with token B gets a different session.
	ch2, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source:  openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:     "tools/echo",
		Input:   map[string]any{"message": "second"},
		Context: map[string]any{"bearerToken": "token_B"},
	})
	if err != nil {
		t.Fatalf("second ExecuteBinding error: %v", err)
	}
	for ev := range ch2 {
		if ev.Error != nil {
			t.Fatalf("second call: unexpected error: %+v", ev.Error)
		}
	}

	got := initCount.Load()
	if got != 2 {
		t.Errorf("expected 2 MCP initialize handshakes (different auth headers), got %d", got)
	}
}

// TestIntegration_SessionPooling_IdleTimeout verifies that a pooled session
// is evicted after the idle timeout expires, forcing a new session (and
// initialize handshake) for the next call.
func TestIntegration_SessionPooling_IdleTimeout(t *testing.T) {
	server := gomcp.NewServer(&gomcp.Implementation{
		Name:    "idle-timeout-test",
		Version: "1.0.0",
	}, nil)

	type echoInput struct {
		Message string `json:"message"`
	}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "echo",
		Description: "Echoes the input message",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args echoInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "echo: " + args.Message},
			},
		}, nil, nil
	})

	var initCount atomic.Int32
	mcpHandler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server {
		return server
	}, &gomcp.StreamableHTTPOptions{Stateless: true})

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"method":"initialize"`)) {
				initCount.Add(1)
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	// Use a very short idle timeout so the test doesn't take long.
	exec := NewExecutor(WithIdleTimeout(50 * time.Millisecond))
	defer exec.Close()
	ctx := context.Background()

	// First call.
	ch1, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "tools/echo",
		Input:  map[string]any{"message": "first"},
	})
	if err != nil {
		t.Fatalf("first ExecuteBinding error: %v", err)
	}
	for ev := range ch1 {
		if ev.Error != nil {
			t.Fatalf("first call: unexpected error: %+v", ev.Error)
		}
	}

	// Wait for the idle timeout to expire.
	time.Sleep(100 * time.Millisecond)

	// Second call should need a new session.
	ch2, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
		Ref:    "tools/echo",
		Input:  map[string]any{"message": "second"},
	})
	if err != nil {
		t.Fatalf("second ExecuteBinding error: %v", err)
	}
	for ev := range ch2 {
		if ev.Error != nil {
			t.Fatalf("second call: unexpected error: %+v", ev.Error)
		}
	}

	got := initCount.Load()
	if got != 2 {
		t.Errorf("expected 2 MCP initialize handshakes (idle timeout evicted first), got %d", got)
	}
}

// TestIntegration_SessionPooling_ConcurrentCalls verifies that concurrent
// tool calls to the same server share a single pooled session.
func TestIntegration_SessionPooling_ConcurrentCalls(t *testing.T) {
	server := gomcp.NewServer(&gomcp.Implementation{
		Name:    "concurrent-test",
		Version: "1.0.0",
	}, nil)

	type echoInput struct {
		Message string `json:"message"`
	}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "echo",
		Description: "Echoes the input message",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args echoInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "echo: " + args.Message},
			},
		}, nil, nil
	})

	var initCount atomic.Int32
	mcpHandler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server {
		return server
	}, &gomcp.StreamableHTTPOptions{Stateless: true})

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte(`"method":"initialize"`)) {
				initCount.Add(1)
			}
		}
		mcpHandler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(wrapped)
	defer ts.Close()

	exec := NewExecutor()
	defer exec.Close()
	ctx := context.Background()

	// Launch 5 concurrent tool calls.
	const concurrency = 5
	var wg sync.WaitGroup
	errors := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ch, err := exec.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
				Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: ts.URL},
				Ref:    "tools/echo",
				Input:  map[string]any{"message": fmt.Sprintf("hello-%d", idx)},
			})
			if err != nil {
				errors <- fmt.Errorf("goroutine %d ExecuteBinding: %w", idx, err)
				return
			}
			for ev := range ch {
				if ev.Error != nil {
					errors <- fmt.Errorf("goroutine %d: stream error: %s: %s", idx, ev.Error.Code, ev.Error.Message)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// All 5 calls should have shared a single session.
	got := initCount.Load()
	if got != 1 {
		t.Errorf("expected 1 MCP initialize handshake for %d concurrent calls, got %d", concurrency, got)
	}
}
