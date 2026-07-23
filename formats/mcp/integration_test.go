package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
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

	// Register a tool that always reports an application-level error
	// (CallToolResult.isError) rather than a protocol error.
	type failInput struct{}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "alwaysFails",
		Description: "Always reports an application-level tool error",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args failInput) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			IsError: true,
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "the tool exploded"},
			},
		}, nil, nil
	})

	// Register a tool that blocks until its context is cancelled (or a
	// bounded timeout elapses — kept short because in stateless HTTP mode the
	// server cannot always correlate a cancellation notification back to the
	// in-flight request, and httptest.Server.Close waits for the handler).
	type slowInput struct{}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "slow",
		Description: "Blocks until cancelled",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args slowInput) (*gomcp.CallToolResult, any, error) {
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{&gomcp.TextContent{Text: "done"}},
		}, nil, nil
	})

	// Register a tool that emits progress notifications during invocation.
	// The tool sends `steps` progress events and then returns the final
	// result.
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
					Message:       "step " + strconv.Itoa(i),
				})
			}
		}
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "completed " + strconv.Itoa(steps) + " steps"},
			},
		}, nil, nil
	})

	// Register a tool that sends a progress notification with an explicit
	// total of 0. Exercises openbindings.mcp@1 §9.2's presence-preserving
	// progress values: an explicit total:0 must survive into the output
	// value. The go-mcp SDK's typed ProgressNotificationParams.Total (a
	// plain float64 tagged omitempty, "zero means unknown") cannot represent
	// the distinction, so the invoker captures the raw notification params
	// on the wire (see session.observeSSEData) — but NOTE: the go-mcp SERVER
	// side used here also marshals with omitempty, so a Total of 0 set via
	// NotifyProgress is omitted from the wire entirely. This fixture
	// therefore exercises the ABSENT-total half of presence preservation;
	// the explicit-zero half is covered by the raw-SSE test in
	// conformance_test.go, which writes the notification bytes directly.
	type progressZeroInput struct{}
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "progressZeroTotal",
		Description: "Sends a single progress notification with total:0",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args progressZeroInput) (*gomcp.CallToolResult, any, error) {
		token := req.Params.GetProgressToken()
		if token != nil && req.Session != nil {
			_ = req.Session.NotifyProgress(ctx, &gomcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      1,
				Total:         0,
			})
		}
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{&gomcp.TextContent{Text: "done"}},
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

// setupCountingServer starts an MCP server with a single echo tool whose
// HTTP handler counts `initialize` JSON-RPC calls, for session-pooling tests.
func setupCountingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	server := gomcp.NewServer(&gomcp.Implementation{
		Name:    "counting-test",
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

	initCount := &atomic.Int32{}
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
	t.Cleanup(ts.Close)
	return ts, initCount
}

// --- Handle-driving helpers ---

func bg() context.Context { return context.Background() }

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func invocationArgs(url, ref string, bindCtx map[string]any) *openbindings.BindingInvocationArgs {
	return &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Location: url},
		Ref:     ref,
		Context: bindCtx,
	}
}

// drainOutputs reads the invocation's output stream to io.EOF or terminal,
// returning the collected outputs and the terminal error (nil on clean EOF).
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

func codeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a terminal error, got nil")
	}
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %T: %v", err, err)
	}
	return ie.Code
}

// --- Integration Tests ---

func TestIntegration_SynthesizeInterface(t *testing.T) {
	ts, _ := setupMCPServer(t)

	synthesizer := NewSynthesizer()
	iface, err := synthesizer.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    ts.URL,
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

	// Tools: echo, alwaysFails, slow, longRunning, progressZeroTotal; resource: status; prompt: greet.
	if len(iface.Operations) != 7 {
		t.Fatalf("expected 7 operations, got %d: %v", len(iface.Operations), keys(iface.Operations))
	}
	for _, op := range []string{"echo", "longRunning", "status", "greet"} {
		if _, ok := iface.Operations[op]; !ok {
			t.Errorf("expected operation %q", op)
		}
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

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/echo",
		map[string]any{"headers": map[string]any{"Authorization": "Bearer tok_secret"}}))
	if err := call.Write(bg(), map[string]any{"message": "hello world"}); err != nil {
		t.Fatal(err)
	}

	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if got := toolResultText(t, v); got != "echo: hello world" {
		t.Errorf("tool result text = %q, want 'echo: hello world'", got)
	}

	// Verify credentials flowed through.
	if state.lastAuth != "Bearer tok_secret" {
		t.Errorf("auth = %q, want Bearer tok_secret", state.lastAuth)
	}

	// Leading metadata carries the call's HTTP response headers.
	md, err := call.Header(shortCtx(t))
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	if len(md["Content-Type"]) == 0 {
		t.Errorf("expected Content-Type in header metadata, got %v", md)
	}
}

func TestIntegration_ExecuteResource(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	// Resource reads take no input: no Write needed.
	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "resources/app://status", nil))
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}

	// Revision 1 preserves the complete protocol result and declared resource
	// item; it does not reinterpret text merely because mimeType says JSON.
	item := resourceResultItem(t, v)
	if item["uri"] != "app://status" || item["mimeType"] != "application/json" || item["text"] != `{"ok":true}` {
		t.Fatalf("resource item was not preserved: %v", item)
	}
}

func TestIntegration_ExecutePrompt(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "prompts/greet", nil))
	if err := call.Write(bg(), map[string]any{"name": "Alice"}); err != nil {
		t.Fatal(err)
	}

	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected map response, got %T", v)
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

// --- Pre-dispatch failures (fire BEFORE any network side effect) ---

func TestIntegration_InvalidRef_NoNetworkIO(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	invoker := NewInvoker()
	defer invoker.Close()

	cases := []struct {
		name string
		ref  string
	}{
		{"no prefix", "bad-ref"},
		{"empty ref", ""},
		{"empty name", "tools/"},
		{"unknown entity type", "widgets/foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, tc.ref, nil))
			vals, err := drainOutputs(t, call)
			if len(vals) != 0 {
				t.Fatalf("expected no outputs, got %v", vals)
			}
			if codeOf(t, err) != openbindings.ErrCodeInvalidRef {
				t.Errorf("code = %q, want ERR_INVALID_REF", codeOf(t, err))
			}
		})
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("invalid refs must fail before network I/O; server saw %d requests", n)
	}
}

func TestIntegration_SourceConfigError(t *testing.T) {
	invoker := NewInvoker()
	defer invoker.Close()

	for _, tc := range []struct {
		name     string
		location string
	}{
		{"missing location", ""},
		{"non-HTTP location", "ftp://mcp.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := invoker.InvokeBinding(bg(), invocationArgs(tc.location, "tools/echo", nil))
			_, err := drainOutputs(t, call)
			if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
				t.Errorf("code = %q, want ERR_SOURCE_CONFIG_ERROR", codeOf(t, err))
			}
		})
	}
}

func TestIntegration_NonObjectToolInput(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/echo", nil))
	if err := call.Write(bg(), "not an object"); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if len(vals) != 0 {
		t.Fatalf("expected no outputs, got %v", vals)
	}
	if codeOf(t, err) != openbindings.ErrCodeValidationFailed {
		t.Errorf("code = %q, want ERR_VALIDATION_FAILED", codeOf(t, err))
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("invalid input must fail before network I/O; server saw %d requests", n)
	}
}

// --- Error mapping ---

func TestIntegration_AuthRequired(t *testing.T) {
	ts, _ := setupMCPServer(t)

	// Front the MCP server with a 401 gate.
	target, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // stream SSE bodies through immediately
	gated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(gated.Close)

	invoker := NewInvoker()
	defer invoker.Close()

	// No credentials: the 401 surfaces as terminal ERR_AUTH_REQUIRED.
	call := invoker.InvokeBinding(bg(), invocationArgs(gated.URL, "resources/app://status", nil))
	_, derr := drainOutputs(t, call)
	if codeOf(t, derr) != openbindings.ErrCodeAuthRequired {
		t.Errorf("code = %q, want ERR_AUTH_REQUIRED", codeOf(t, derr))
	}

	// With credentials the same call succeeds.
	call = invoker.InvokeBinding(bg(), invocationArgs(gated.URL, "resources/app://status",
		map[string]any{"headers": map[string]any{"Authorization": "Bearer tok"}}))
	if _, err := openbindings.Single(shortCtx(t), call.Outputs()); err != nil {
		t.Fatalf("authorized call failed: %v", err)
	}
}

// TestIntegration_UnknownTool_RefusedBeforeDispatch: updated for
// openbindings.mcp@1 §7 (MCP-P-02) — this test previously pinned the
// non-conformant blind dispatch, where an unknown tool surfaced as the
// server's JSON-RPC error. Resolution against the exhausted listing now
// precedes dispatch for every entity: a ref matching nothing makes the
// binding unresolvable and is refused as ERR_REF_NOT_FOUND, and no
// tools/call is ever sent.
func TestIntegration_UnknownTool_RefusedBeforeDispatch(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/no_such_tool", nil))
	if err := call.Write(bg(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if len(vals) != 0 {
		t.Fatalf("expected no outputs, got %v", vals)
	}
	if codeOf(t, err) != openbindings.ErrCodeRefNotFound {
		t.Fatalf("code = %q, want ERR_REF_NOT_FOUND", codeOf(t, err))
	}
}

func TestIntegration_ToolApplicationError(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/alwaysFails", nil))
	if err := call.Write(bg(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if len(vals) != 0 {
		t.Fatalf("application-level tool error must not emit outputs, got %v", vals)
	}
	if codeOf(t, err) != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("code = %q, want ERR_EXECUTION_FAILED", codeOf(t, err))
	}
	if err.Error() != "the tool exploded" {
		t.Errorf("message = %q, want the tool's error text", err.Error())
	}
}

func TestIntegration_ConnectFailed(t *testing.T) {
	invoker := NewInvoker()
	defer invoker.Close()

	// A port that nothing listens on.
	call := invoker.InvokeBinding(bg(), invocationArgs("http://127.0.0.1:1", "resources/app://status", nil))
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeConnectFailed {
		t.Errorf("code = %q, want ERR_CONNECT_FAILED", codeOf(t, err))
	}
}

func TestIntegration_Cancel(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/slow", nil))
	if err := call.Write(bg(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(150*time.Millisecond, call.Cancel)
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeCancelled {
		t.Errorf("code = %q, want ERR_CANCELLED", codeOf(t, err))
	}
}

// --- Progress streaming ---

// TestIntegration_ToolProgressNotifications verifies that, when progress is
// SOLICITED (openbindings.mcp@1 §9.3's `solicit` configuration point — the
// per-invocation opt-in here; the default is off, see the solicit tests in
// conformance_test.go), the invoker emits MCP `notifications/progress`
// events as outputs ahead of the final tool result, with the result
// guaranteed last (§9.2, MCP-P-04). Updated for the solicit default: this
// test previously relied on the non-conformant always-solicit behavior.
//
// Note: the go-mcp library dispatches progress notifications asynchronously.
// In edge cases, a notification may not be processed before the tool call
// returns and the invocation closes, so the exact progress count is not
// asserted; structure and ordering are.
func TestIntegration_ToolProgressNotifications(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/longRunning",
		map[string]any{"configuration": map[string]any{"solicit": true}}))
	if err := call.Write(bg(), map[string]any{"steps": 3}); err != nil {
		t.Fatal(err)
	}

	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected terminal error: %v", err)
	}
	if len(vals) < 1 {
		t.Fatalf("expected at least the final result output, got %d", len(vals))
	}

	// The final result is always the LAST output. Progress events precede it.
	// A progress event is the map shape the invoker emits: {progress, total?,
	// message?}. The internal progressToken is correlation plumbing and is
	// deliberately NOT part of the output (TS parity), so the final tool result
	// (a non-progress value) is reliably distinguished by the absence of a
	// "progress" key. go-mcp dispatches notifications asynchronously, so the
	// exact count is not asserted; the shape of whatever arrives is.
	final := vals[len(vals)-1]
	if m, ok := final.(map[string]any); ok {
		if _, isProgress := m["progress"]; isProgress {
			t.Fatalf("last output must be the tool result, got progress event %v", m)
		}
	}
	progress := vals[:len(vals)-1]
	t.Logf("received %d progress notifications out of 3 requested", len(progress))
	for i, ev := range progress {
		data, ok := ev.(map[string]any)
		if !ok {
			t.Fatalf("progress event %d: expected map, got %T", i, ev)
		}
		// progress is REQUIRED on every progress event.
		if _, ok := data["progress"]; !ok {
			t.Errorf("progress event %d: missing required progress field: %v", i, data)
		}
		// progressToken is correlation plumbing, NOT part of the output.
		if _, hasToken := data["progressToken"]; hasToken {
			t.Errorf("progress event %d: must NOT carry progressToken, got %v", i, data)
		}
		// total/message are present only when the notification provided them.
		// The test server's longRunning tool always sends both (total=3,
		// message="step N"), so when present they must carry those values; but
		// the shape never requires their presence.
		if total, ok := data["total"]; ok {
			if tf, isNum := total.(float64); !isNum || tf != 3 {
				t.Errorf("progress event %d: total = %v, want 3", i, total)
			}
		}
		if msg, ok := data["message"]; ok {
			if ms, isStr := msg.(string); !isStr || !strings.HasPrefix(ms, "step ") {
				t.Errorf("progress event %d: message = %v, want \"step N\"", i, msg)
			}
		}
	}
}

// TestIntegration_ToolProgressZeroTotal: updated for openbindings.mcp@1
// §9.2's presence-preserving progress values — the "named gap" this test
// previously pinned (an explicit total:0 silently dropped by go-mcp's typed
// omitempty struct) is CLOSED: the invoker now emits the raw wire params.
// What this end-to-end fixture can still exercise is only the absent half
// of presence preservation, because the go-mcp SERVER used here also
// marshals Total with omitempty: its NotifyProgress(total: 0) puts no
// "total" member on the wire at all, and an absent total must stay absent.
// The explicit-zero half (a wire "total":0 surviving into the output value)
// is proven at the wire seam by TestSolicit_RawPresencePreserved in
// conformance_test.go.
func TestIntegration_ToolProgressZeroTotal(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/progressZeroTotal",
		map[string]any{"configuration": map[string]any{"solicit": true}}))
	if err := call.Write(bg(), map[string]any{}); err != nil {
		t.Fatal(err)
	}

	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected terminal error: %v", err)
	}
	for i, ev := range vals {
		data, ok := ev.(map[string]any)
		if !ok || data["progress"] == nil {
			continue // the final tool result, not a progress event
		}
		if _, hasTotal := data["total"]; hasTotal {
			t.Errorf("progress event %d: the server's wire notification carried no total member (go-mcp server-side omitempty), so an absent total must stay absent, got %v", i, data)
		}
	}
}

// TestIntegration_ToolNoProgress_SingleOutput verifies that a tool that does
// not emit progress notifications produces exactly one output (the result).
func TestIntegration_ToolNoProgress_SingleOutput(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/echo", nil))
	if err := call.Write(bg(), map[string]any{"message": "hello"}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected terminal error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected exactly 1 output from no-progress tool, got %d", len(vals))
	}
}

// --- Session pooling ---

// TestIntegration_SessionPooling verifies that two consecutive tool calls
// against the same MCP server reuse the same pooled session, producing only
// one MCP `initialize` handshake instead of two.
func TestIntegration_SessionPooling(t *testing.T) {
	ts, initCount := setupCountingServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	for _, msg := range []string{"first", "second"} {
		call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/echo", nil))
		if err := call.Write(bg(), map[string]any{"message": msg}); err != nil {
			t.Fatal(err)
		}
		if _, err := openbindings.Single(shortCtx(t), call.Outputs()); err != nil {
			t.Fatalf("%s call: %v", msg, err)
		}
	}

	if got := initCount.Load(); got != 1 {
		t.Errorf("expected 1 MCP initialize handshake (session reuse), got %d", got)
	}
}

// TestIntegration_SessionPooling_DifferentHeaders verifies that calls with
// different auth headers get separate pooled sessions.
func TestIntegration_SessionPooling_DifferentHeaders(t *testing.T) {
	ts, initCount := setupCountingServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	for _, token := range []string{"token_A", "token_B"} {
		call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/echo",
			map[string]any{"headers": map[string]any{"Authorization": "Bearer " + token}}))
		if err := call.Write(bg(), map[string]any{"message": token}); err != nil {
			t.Fatal(err)
		}
		if _, err := openbindings.Single(shortCtx(t), call.Outputs()); err != nil {
			t.Fatalf("%s call: %v", token, err)
		}
	}

	if got := initCount.Load(); got != 2 {
		t.Errorf("expected 2 MCP initialize handshakes (different auth headers), got %d", got)
	}
}

// TestIntegration_SessionPooling_IdleTimeout verifies that a pooled session
// is evicted after the idle timeout expires, forcing a new session (and
// initialize handshake) for the next call.
func TestIntegration_SessionPooling_IdleTimeout(t *testing.T) {
	ts, initCount := setupCountingServer(t)

	// Use a very short idle timeout so the test doesn't take long.
	invoker := NewInvoker(WithIdleTimeout(50 * time.Millisecond))
	defer invoker.Close()

	for _, msg := range []string{"first", "second"} {
		call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/echo", nil))
		if err := call.Write(bg(), map[string]any{"message": msg}); err != nil {
			t.Fatal(err)
		}
		if _, err := openbindings.Single(shortCtx(t), call.Outputs()); err != nil {
			t.Fatalf("%s call: %v", msg, err)
		}
		// Wait for the idle timeout to expire between calls.
		time.Sleep(100 * time.Millisecond)
	}

	if got := initCount.Load(); got != 2 {
		t.Errorf("expected 2 MCP initialize handshakes (idle timeout evicted first), got %d", got)
	}
}

// TestIntegration_SessionPooling_ConcurrentCalls verifies that concurrent
// tool calls to the same server share a single pooled session.
func TestIntegration_SessionPooling_ConcurrentCalls(t *testing.T) {
	ts, initCount := setupCountingServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	const concurrency = 5
	var wg sync.WaitGroup
	errCh := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/echo", nil))
			if err := call.Write(bg(), map[string]any{"message": fmt.Sprintf("hello-%d", idx)}); err != nil {
				errCh <- fmt.Errorf("goroutine %d write: %w", idx, err)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := openbindings.Single(ctx, call.Outputs()); err != nil {
				errCh <- fmt.Errorf("goroutine %d: %w", idx, err)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	// All 5 calls should have shared a single session.
	if got := initCount.Load(); got != 1 {
		t.Errorf("expected 1 MCP initialize handshake for %d concurrent calls, got %d", concurrency, got)
	}
}

// --- helpers ---

func keys[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestIntegration_NoInputConvention verifies the operation-layer no-input
// convention: a binding driven for an operation that declares no input
// (Binding set, InputSchema nil) closes input on entry and dispatches with
// empty arguments. The caller writes nothing and never closes — a missing
// convention would park ReadInput forever.
func TestIntegration_NoInputConvention(t *testing.T) {
	ts, _ := setupMCPServer(t)

	invoker := NewInvoker(WithClientVersion("test"))
	args := invocationArgs(ts.URL, "tools/alwaysFails", nil)
	args.Binding = &openbindings.BindingEntry{Operation: "alwaysFails", Source: "s", Ref: "tools/alwaysFails"}
	// InputSchema deliberately left nil → no-input operation.

	call := invoker.InvokeBinding(bg(), args)
	// The caller never writes nor closes; the binding must run regardless.
	_, err := openbindings.Single(shortCtx(t), call.Outputs())
	// alwaysFails reports an application error; what matters here is that the
	// invocation TERMINATED (no deadlock), not its verdict.
	if err != nil && codeOf(t, err) != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("expected completion or ERR_EXECUTION_FAILED, got %v", err)
	}
}
