package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// wireLog records every JSON-RPC request body the test server receives, so
// tests can assert what actually rode the wire: which list requests were
// consulted, whether tools/call carried an arguments member or a
// progressToken, which URI a resources/read targeted.
type wireLog struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (l *wireLog) record(b []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bodies = append(l.bodies, append([]byte(nil), b...))
}

// params returns the parsed params object of every logged request with the
// given JSON-RPC method, in arrival order.
func (l *wireLog) params(method string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, b := range l.bodies {
		var msg struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(b, &msg) == nil && msg.Method == method {
			out = append(out, msg.Params)
		}
	}
	return out
}

func (l *wireLog) count(method string) int { return len(l.params(method)) }

// setupConformanceServer builds an MCP server carrying one entity of each
// family plus a progress-capable tool, wrapped so every JSON-RPC request
// body is logged. pageSize > 0 overrides the server's list page size.
func setupConformanceServer(t *testing.T, pageSize int, extraTools ...string) (*httptest.Server, *wireLog) {
	t.Helper()

	var opts *gomcp.ServerOptions
	if pageSize > 0 {
		opts = &gomcp.ServerOptions{PageSize: pageSize}
	}
	server := gomcp.NewServer(&gomcp.Implementation{Name: "conformance-test", Version: "1.0.0"}, opts)

	addEcho := func(name string) {
		gomcp.AddTool(server, &gomcp.Tool{
			Name: name, Description: "returns ok",
		}, func(ctx context.Context, req *gomcp.CallToolRequest, args map[string]any) (*gomcp.CallToolResult, any, error) {
			return &gomcp.CallToolResult{
				Content: []gomcp.Content{&gomcp.TextContent{Text: "ok"}},
			}, nil, nil
		})
	}
	addEcho("probe")
	for _, name := range extraTools {
		addEcho(name)
	}

	type progressIn struct{}
	gomcp.AddTool(server, &gomcp.Tool{
		Name: "progress", Description: "notifies progress when a token is attached",
	}, func(ctx context.Context, req *gomcp.CallToolRequest, args progressIn) (*gomcp.CallToolResult, any, error) {
		token := req.Params.GetProgressToken()
		if token != nil && req.Session != nil {
			for i := 1; i <= 2; i++ {
				_ = req.Session.NotifyProgress(ctx, &gomcp.ProgressNotificationParams{
					ProgressToken: token,
					Progress:      float64(i),
					Total:         2,
				})
			}
		}
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{&gomcp.TextContent{Text: "done"}},
		}, nil, nil
	})

	server.AddPrompt(&gomcp.Prompt{
		Name: "greet", Description: "greeting",
		Arguments: []*gomcp.PromptArgument{{Name: "name", Description: "who"}},
	}, func(ctx context.Context, req *gomcp.GetPromptRequest) (*gomcp.GetPromptResult, error) {
		return &gomcp.GetPromptResult{
			Messages: []*gomcp.PromptMessage{
				{Role: "user", Content: &gomcp.TextContent{Text: "hi " + req.Params.Arguments["name"]}},
			},
		}, nil
	})

	server.AddResource(&gomcp.Resource{Name: "static", URI: "app://static"}, func(ctx context.Context, req *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{
			Contents: []*gomcp.ResourceContents{{URI: "app://static", MIMEType: "text/plain", Text: "s"}},
		}, nil
	})

	// The template handler echoes the concrete URI it was asked to read, so
	// tests can observe the RFC 6570 expansion the invoker performed.
	server.AddResourceTemplate(&gomcp.ResourceTemplate{
		Name: "logs", URITemplate: "file:///logs/{date}",
	}, func(ctx context.Context, req *gomcp.ReadResourceRequest) (*gomcp.ReadResourceResult, error) {
		return &gomcp.ReadResourceResult{
			Contents: []*gomcp.ResourceContents{{URI: req.Params.URI, MIMEType: "text/plain", Text: req.Params.URI}},
		}, nil
	})

	log := &wireLog{}
	mcpHandler := gomcp.NewStreamableHTTPHandler(func(r *http.Request) *gomcp.Server { return server },
		&gomcp.StreamableHTTPOptions{Stateless: true})
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			log.record(body)
		}
		mcpHandler.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(wrapped)
	t.Cleanup(ts.Close)
	return ts, log
}

// deadEndServer is a non-MCP HTTP server that counts requests: anything that
// must refuse before network I/O proves it by leaving the counter at zero.
func deadEndServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	return ts, &requests
}

func pinArgs(url, ref string, pin map[string]any, bindCtx map[string]any) *openbindings.BindingInvocationArgs {
	args := invocationArgs(url, ref, bindCtx)
	args.Source.Content = mustContent(pin)
	return args
}

// ---------------------------------------------------------------------------
// Item 1 — pinned listings (MCP-D-01, §3/§5/§6)
// ---------------------------------------------------------------------------

// An invalid pin is refused loudly BEFORE any network I/O: stray members
// (pagination carriage included) and malformed entity arrays are all outside
// the pin grammar.
func TestPin_InvalidRefusedLoudly(t *testing.T) {
	ts, requests := deadEndServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	cases := []struct {
		name string
		pin  any
	}{
		{"stray nextCursor", map[string]any{
			"tools":      []any{map[string]any{"name": "probe"}},
			"nextCursor": "page2",
		}},
		{"stray _meta", map[string]any{
			"tools": []any{map[string]any{"name": "probe"}},
			"_meta": map[string]any{},
		}},
		{"arbitrary stray member", map[string]any{
			"tools":  []any{map[string]any{"name": "probe"}},
			"extras": []any{},
		}},
		{"non-object content", "not a listing"},
		{"array content", []any{}},
		{"member not an array", map[string]any{"tools": map[string]any{"name": "probe"}}},
		{"entry not an object", map[string]any{"tools": []any{"probe"}}},
		{"entry missing identity", map[string]any{"tools": []any{map[string]any{"description": "no name"}}}},
		{"entry identity not a string", map[string]any{"resources": []any{map[string]any{"uri": 7}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := invocationArgs(ts.URL, "tools/probe", nil)
			args.Source.Content = mustContent(tc.pin)
			call := invoker.InvokeBinding(bg(), args)
			vals, err := drainOutputs(t, call)
			if len(vals) != 0 {
				t.Fatalf("expected no outputs, got %v", vals)
			}
			if codeOf(t, err) != openbindings.ErrCodeSourceLoadFailed {
				t.Errorf("code = %q, want ERR_SOURCE_LOAD_FAILED", codeOf(t, err))
			}
		})
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("invalid pins must be refused before network I/O; server saw %d requests", n)
	}
}

// A pin displaces the list requests entirely (§6 content primacy, MCP-P-02):
// ref resolution is answered from the pin and dispatch proceeds against it.
func TestPin_DisplacesListRequests(t *testing.T) {
	ts, log := setupConformanceServer(t, 0)

	invoker := NewInvoker()
	defer invoker.Close()

	pin := map[string]any{"tools": []any{map[string]any{"name": "probe"}}}
	call := invoker.InvokeBinding(bg(), pinArgs(ts.URL, "tools/probe", pin, nil))
	if err := call.Write(bg(), map[string]any{"q": "1"}); err != nil {
		t.Fatal(err)
	}
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v != "ok" {
		t.Errorf("output = %v, want ok", v)
	}
	if n := log.count("tools/list"); n != 0 {
		t.Errorf("a pinned listing must displace list requests; server saw %d tools/list calls", n)
	}
	if n := log.count("tools/call"); n != 1 {
		t.Errorf("expected 1 tools/call, got %d", n)
	}
}

// With a pin, unresolvable and ambiguous refs are refused offline — before
// any connection (§7, MCP-P-02).
func TestPin_ResolutionRefusalsAreOffline(t *testing.T) {
	ts, requests := deadEndServer(t)

	invoker := NewInvoker()
	defer invoker.Close()

	cases := []struct {
		name        string
		ref         string
		pin         map[string]any
		wantMessage string
	}{
		{
			"ref matches nothing",
			"tools/missing",
			map[string]any{"tools": []any{map[string]any{"name": "probe"}}},
			"matches no",
		},
		{
			"ambiguous tool name",
			"tools/dup",
			map[string]any{"tools": []any{map[string]any{"name": "dup"}, map[string]any{"name": "dup"}}},
			"ambiguous",
		},
		{
			"ambiguous template",
			"resourceTemplates/file:///x/{id}",
			map[string]any{"resourceTemplates": []any{
				map[string]any{"uriTemplate": "file:///x/{id}"},
				map[string]any{"uriTemplate": "file:///x/{id}"},
			}},
			"ambiguous",
		},
		{
			"empty pin resolves nothing",
			"prompts/greet",
			map[string]any{},
			"matches no",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := invoker.InvokeBinding(bg(), pinArgs(ts.URL, tc.ref, tc.pin, nil))
			_, err := drainOutputs(t, call)
			if codeOf(t, err) != openbindings.ErrCodeRefNotFound {
				t.Fatalf("code = %q, want ERR_REF_NOT_FOUND", codeOf(t, err))
			}
			if !strings.Contains(err.Error(), tc.wantMessage) {
				t.Errorf("message %q should contain %q", err.Error(), tc.wantMessage)
			}
		})
	}

	if n := requests.Load(); n != 0 {
		t.Errorf("pin resolution refusals must fire before network I/O; server saw %d requests", n)
	}
}

// A pinned static resource resolves offline and dispatches directly.
func TestPin_StaticResourceRead(t *testing.T) {
	ts, log := setupConformanceServer(t, 0)

	invoker := NewInvoker()
	defer invoker.Close()

	pin := map[string]any{"resources": []any{map[string]any{"uri": "app://static", "name": "static"}}}
	call := invoker.InvokeBinding(bg(), pinArgs(ts.URL, "resources/app://static", pin, nil))
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 1 || items[0] != "s" {
		t.Fatalf("output = %T %v, want [s]", v, v)
	}
	if n := log.count("resources/list"); n != 0 {
		t.Errorf("pin must displace resources/list; server saw %d", n)
	}
	if n := log.count("resources/templates/list"); n != 0 {
		t.Errorf("pin must displace resources/templates/list; server saw %d", n)
	}
}

// A pinned resource template resolves offline; the input expands the
// template before resources/read (items 1+7 together).
func TestPin_TemplateExpansion(t *testing.T) {
	ts, log := setupConformanceServer(t, 0)

	invoker := NewInvoker()
	defer invoker.Close()

	pin := map[string]any{"resourceTemplates": []any{map[string]any{"uriTemplate": "file:///logs/{date}"}}}
	call := invoker.InvokeBinding(bg(), pinArgs(ts.URL, "resourceTemplates/file:///logs/{date}", pin, nil))
	if err := call.Write(bg(), map[string]any{"date": "2026-07-15"}); err != nil {
		t.Fatal(err)
	}
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 1 || items[0] != "file:///logs/2026-07-15" {
		t.Fatalf("output = %T %v, want the expanded URI echoed back", v, v)
	}
	if n := log.count("resources/templates/list"); n != 0 {
		t.Errorf("pin must displace resources/templates/list; server saw %d", n)
	}
	reads := log.params("resources/read")
	if len(reads) != 1 || reads[0]["uri"] != "file:///logs/2026-07-15" {
		t.Errorf("resources/read must target the RFC 6570 expansion, got %v", reads)
	}
}

// ---------------------------------------------------------------------------
// Item 2 — pagination exhaustion (MCP-P-02, §3)
// ---------------------------------------------------------------------------

// With a page size of 1, resolving a tool that sorts onto the last page
// requires following nextCursor to exhaustion; a first-page-only
// implementation would refuse a perfectly resolvable ref.
func TestResolution_PaginationExhausted(t *testing.T) {
	// Tools: probe, progress, zzz-last → three pages at size 1.
	ts, log := setupConformanceServer(t, 1, "zzz-last")

	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/zzz-last", nil))
	if err := call.Write(bg(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v != "ok" {
		t.Errorf("output = %v, want ok", v)
	}
	if n := log.count("tools/list"); n < 3 {
		t.Errorf("expected the listing to be followed to exhaustion (>= 3 pages), saw %d tools/list calls", n)
	}
}

// ---------------------------------------------------------------------------
// Item 3 — progress solicitation configuration point (§9.2/§9.3, MCP-P-04/05)
// ---------------------------------------------------------------------------

// The solicit point is consulted per-invocation configuration →
// consumer-level configuration → default OFF. The wire tells the truth:
// solicited calls carry _meta.progressToken, unsolicited calls carry none.
func TestSolicit_ConsultationOrder(t *testing.T) {
	cases := []struct {
		name      string
		opts      []InvokerOption
		bindCtx   map[string]any
		wantToken bool
	}{
		{"default off", nil, nil, false},
		{"per-invocation opt-in", nil,
			map[string]any{"configuration": map[string]any{"solicit": true}}, true},
		{"consumer-level opt-in", []InvokerOption{WithSolicitProgress(true)}, nil, true},
		{"per-invocation overrides consumer", []InvokerOption{WithSolicitProgress(true)},
			map[string]any{"configuration": map[string]any{"solicit": false}}, false},
		{"non-bool override declines and falls through", nil,
			map[string]any{"configuration": map[string]any{"solicit": "yes"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, log := setupConformanceServer(t, 0)
			invoker := NewInvoker(tc.opts...)
			defer invoker.Close()

			call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "tools/progress", tc.bindCtx))
			if err := call.Write(bg(), map[string]any{}); err != nil {
				t.Fatal(err)
			}
			vals, err := drainOutputs(t, call)
			if err != nil {
				t.Fatalf("unexpected terminal error: %v", err)
			}

			calls := log.params("tools/call")
			if len(calls) != 1 {
				t.Fatalf("expected 1 tools/call, got %d", len(calls))
			}
			meta, _ := calls[0]["_meta"].(map[string]any)
			_, hasToken := meta["progressToken"]
			if hasToken != tc.wantToken {
				t.Errorf("progressToken on the wire = %v, want %v (params %v)", hasToken, tc.wantToken, calls[0])
			}

			if !tc.wantToken {
				// Not solicited: the stream is the result value alone.
				if len(vals) != 1 {
					t.Fatalf("unsolicited stream must be exactly the result, got %d outputs: %v", len(vals), vals)
				}
				if vals[0] != "done" {
					t.Errorf("result = %v, want done", vals[0])
				}
			} else {
				// Solicited: the result is last; anything before it is a
				// progress value {progress, total?, message?}. go-mcp
				// dispatches notifications asynchronously, so the exact
				// count is not asserted.
				if len(vals) < 1 || vals[len(vals)-1] != "done" {
					t.Fatalf("the result must terminate the stream, got %v", vals)
				}
				for i, ev := range vals[:len(vals)-1] {
					m, ok := ev.(map[string]any)
					if !ok {
						t.Fatalf("progress value %d: expected map, got %T", i, ev)
					}
					if _, ok := m["progress"]; !ok {
						t.Errorf("progress value %d: missing progress member: %v", i, m)
					}
					if _, ok := m["progressToken"]; ok {
						t.Errorf("progress value %d: progressToken must be removed: %v", i, m)
					}
					if total, ok := m["total"]; ok && total != 2.0 {
						t.Errorf("progress value %d: total = %v, want 2", i, total)
					}
				}
			}
		})
	}
}

// TestSolicit_RawPresencePreserved proves the presence-preserving progress
// value at the wire seam (§9.2, MCP-P-04): the raw notification params are
// captured byte-exactly off the event stream, so an explicit "total": 0 and
// an explicit "message": "" SURVIVE — the go-mcp typed structs would drop
// both — while progressToken is removed and unregistered tokens are
// discarded (defined disposal).
func TestSolicit_RawPresencePreserved(t *testing.T) {
	s := &mcpSession{
		progressHandlers: make(map[string]*progressRegistration),
		rawProgress:      make(map[string][]map[string]any),
	}
	s.registerProgress("tok", func(context.Context, *gomcp.ProgressNotificationClientRequest) {})

	notify := func(params string) []byte {
		return []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":` + params + `}`)
	}

	// Explicit zero/empty members survive; progressToken is removed.
	s.observeSSEData(notify(`{"progressToken":"tok","progress":1,"total":0,"message":""}`))
	out := s.popRawProgress("tok")
	if out == nil {
		t.Fatal("expected a queued raw progress value")
	}
	if total, ok := out["total"]; !ok || total != 0.0 {
		t.Errorf("explicit total:0 must survive presence-preserving decode, got %v", out)
	}
	if msg, ok := out["message"]; !ok || msg != "" {
		t.Errorf("explicit message:\"\" must survive, got %v", out)
	}
	if _, ok := out["progressToken"]; ok {
		t.Errorf("progressToken is correlation plumbing and must be removed, got %v", out)
	}

	// Absent members stay absent.
	s.observeSSEData(notify(`{"progressToken":"tok","progress":2}`))
	out = s.popRawProgress("tok")
	if out == nil {
		t.Fatal("expected a queued raw progress value")
	}
	if _, ok := out["total"]; ok {
		t.Errorf("absent total must stay absent, got %v", out)
	}

	// FIFO order within a token.
	s.observeSSEData(notify(`{"progressToken":"tok","progress":3}`))
	s.observeSSEData(notify(`{"progressToken":"tok","progress":4}`))
	if out = s.popRawProgress("tok"); out["progress"] != 3.0 {
		t.Errorf("raw progress must pop FIFO, got %v", out)
	}
	if out = s.popRawProgress("tok"); out["progress"] != 4.0 {
		t.Errorf("raw progress must pop FIFO, got %v", out)
	}

	// Unregistered tokens are not queued (unsolicited/late → discarded).
	s.observeSSEData(notify(`{"progressToken":"other","progress":9}`))
	if got := s.popRawProgress("other"); got != nil {
		t.Errorf("unregistered token must not queue, got %v", got)
	}

	// Unregistration purges the queue: late correlated notifications after
	// the result are discarded.
	s.observeSSEData(notify(`{"progressToken":"tok","progress":5}`))
	s.unregisterProgress("tok")
	if got := s.popRawProgress("tok"); got != nil {
		t.Errorf("unregisterProgress must purge the raw queue, got %v", got)
	}

	// Requests (with an id) and other methods are ignored.
	s.registerProgress("tok", func(context.Context, *gomcp.ProgressNotificationClientRequest) {})
	s.observeSSEData([]byte(`{"jsonrpc":"2.0","id":7,"method":"notifications/progress","params":{"progressToken":"tok","progress":1}}`))
	s.observeSSEData([]byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"progressToken":"tok"}}`))
	if got := s.popRawProgress("tok"); got != nil {
		t.Errorf("non-notification or non-progress payloads must not queue, got %v", got)
	}
}

// The SSE observer reassembles data payloads across arbitrary read-chunk
// boundaries, CRLF line endings, multi-line data fields, and ignores
// non-data fields — while passing the byte stream through unmodified.
func TestSSEObserver_Scanning(t *testing.T) {
	stream := "event: message\r\n" +
		"data: {\"a\":1}\r\n" +
		"\r\n" +
		": comment\n" +
		"id: 3\n" +
		"data: line1\n" +
		"data: line2\n" +
		"\n" +
		"data:{\"b\":2}\n" +
		"\n"

	var observed []string
	o := &sseObserver{
		body:    io.NopCloser(iotest.OneByteReader(strings.NewReader(stream))),
		observe: func(data []byte) { observed = append(observed, string(data)) },
	}
	passed, err := io.ReadAll(o)
	if err != nil {
		t.Fatal(err)
	}
	if string(passed) != stream {
		t.Error("the observer must pass bytes through unmodified")
	}
	want := []string{`{"a":1}`, "line1\nline2", `{"b":2}`}
	if len(observed) != len(want) {
		t.Fatalf("observed %d payloads %v, want %v", len(observed), observed, want)
	}
	for i := range want {
		if observed[i] != want[i] {
			t.Errorf("payload %d = %q, want %q", i, observed[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Item 5 — absent input omits arguments; string-typed refusals (§9.1, MCP-P-03)
// ---------------------------------------------------------------------------

// An absent input value omits the arguments member ENTIRELY on tools/call
// and prompts/get — never arguments: {} and never arguments: null.
func TestInput_AbsentArgumentsOmitted(t *testing.T) {
	cases := []struct {
		name   string
		ref    string
		method string
		drive  func(t *testing.T, call openbindings.Invocation[any, any])
	}{
		{"tool, caller closes without writing", "tools/probe", "tools/call",
			func(t *testing.T, call openbindings.Invocation[any, any]) { _ = call.Close() }},
		{"prompt, caller closes without writing", "prompts/greet", "prompts/get",
			func(t *testing.T, call openbindings.Invocation[any, any]) { _ = call.Close() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, log := setupConformanceServer(t, 0)
			invoker := NewInvoker()
			defer invoker.Close()

			call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, tc.ref, nil))
			tc.drive(t, call)
			if _, err := openbindings.Single(shortCtx(t), call.Outputs()); err != nil {
				t.Fatalf("invocation failed: %v", err)
			}

			reqs := log.params(tc.method)
			if len(reqs) != 1 {
				t.Fatalf("expected 1 %s, got %d", tc.method, len(reqs))
			}
			if _, has := reqs[0]["arguments"]; has {
				t.Errorf("absent input must omit the arguments member entirely, wire params: %v", reqs[0])
			}
		})
	}
}

// The no-input operation convention (Binding set, InputSchema nil) also
// dispatches with the arguments member omitted.
func TestInput_NoInputOperationOmitsArguments(t *testing.T) {
	ts, log := setupConformanceServer(t, 0)
	invoker := NewInvoker()
	defer invoker.Close()

	args := invocationArgs(ts.URL, "tools/probe", nil)
	args.Binding = &openbindings.BindingEntry{Operation: "probe", Source: "s", Ref: "tools/probe"}
	call := invoker.InvokeBinding(bg(), args)
	if _, err := openbindings.Single(shortCtx(t), call.Outputs()); err != nil {
		t.Fatalf("invocation failed: %v", err)
	}
	reqs := log.params("tools/call")
	if len(reqs) != 1 {
		t.Fatalf("expected 1 tools/call, got %d", len(reqs))
	}
	if _, has := reqs[0]["arguments"]; has {
		t.Errorf("no-input operation must omit the arguments member, wire params: %v", reqs[0])
	}
}

// A supplied input that is not an object — JSON null included — and a
// non-string prompt argument are refused loudly, never coerced, before the
// entity request is dispatched.
func TestInput_SuppliedShapeRefusals(t *testing.T) {
	cases := []struct {
		name   string
		ref    string
		method string
		input  any
	}{
		{"tool null input", "tools/probe", "tools/call", nil},
		{"tool string input", "tools/probe", "tools/call", "not an object"},
		{"prompt null input", "prompts/greet", "prompts/get", nil},
		{"prompt array input", "prompts/greet", "prompts/get", []any{"x"}},
		// Previously the invoker stringified non-string prompt arguments
		// (fmt.Sprintf coercion) — payload coercion that §9.1/MCP-P-03
		// forbids: prompt arguments are string-typed and never coerced.
		{"prompt non-string member", "prompts/greet", "prompts/get", map[string]any{"name": 42}},
		{"prompt bool member", "prompts/greet", "prompts/get", map[string]any{"name": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, log := setupConformanceServer(t, 0)
			invoker := NewInvoker()
			defer invoker.Close()

			call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, tc.ref, nil))
			if err := call.Write(bg(), tc.input); err != nil {
				t.Fatal(err)
			}
			vals, err := drainOutputs(t, call)
			if len(vals) != 0 {
				t.Fatalf("expected no outputs, got %v", vals)
			}
			if codeOf(t, err) != openbindings.ErrCodeValidationFailed {
				t.Errorf("code = %q, want ERR_VALIDATION_FAILED", codeOf(t, err))
			}
			if n := log.count(tc.method); n != 0 {
				t.Errorf("refusal must precede dispatch; server saw %d %s requests", n, tc.method)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Item 7 — RFC 6570 template expansion (§9.1, MCP-P-03), live listing
// ---------------------------------------------------------------------------

func TestTemplate_LiveExpansion(t *testing.T) {
	ts, log := setupConformanceServer(t, 0)
	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "resourceTemplates/file:///logs/{date}", nil))
	if err := call.Write(bg(), map[string]any{"date": "2026-07-15"}); err != nil {
		t.Fatal(err)
	}
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 1 || items[0] != "file:///logs/2026-07-15" {
		t.Fatalf("output = %T %v, want the expanded URI echoed back", v, v)
	}
	// The template was resolved against the live, exhausted template listing.
	if n := log.count("resources/templates/list"); n < 1 {
		t.Errorf("expected the template listing to be consulted, saw %d calls", n)
	}
	reads := log.params("resources/read")
	if len(reads) != 1 || reads[0]["uri"] != "file:///logs/2026-07-15" {
		t.Errorf("resources/read must target the RFC 6570 expansion, not the raw template, got %v", reads)
	}
}

// A declared variable the input does not supply follows RFC 6570's
// undefined-value expansion; an absent input expands with all variables
// undefined.
func TestTemplate_AbsentInputExpandsUndefined(t *testing.T) {
	ts, log := setupConformanceServer(t, 0)
	invoker := NewInvoker()
	defer invoker.Close()

	call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "resourceTemplates/file:///logs/{date}", nil))
	_ = call.Close() // absent input
	v, err := openbindings.Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	items, ok := v.([]any)
	if !ok || len(items) != 1 || items[0] != "file:///logs/" {
		t.Fatalf("output = %T %v, want file:///logs/ (undefined-value expansion)", v, v)
	}
	reads := log.params("resources/read")
	if len(reads) != 1 || reads[0]["uri"] != "file:///logs/" {
		t.Errorf("resources/read must target the undefined-value expansion, got %v", reads)
	}
}

// Template-variable refusals fire before resources/read is dispatched: a
// non-string variable (never coerced), an undeclared variable, and a
// non-object input.
func TestTemplate_InputRefusals(t *testing.T) {
	cases := []struct {
		name  string
		input any
	}{
		{"non-string variable", map[string]any{"date": 42}},
		{"undeclared variable", map[string]any{"date": "x", "extra": "y"}},
		{"non-object input", "2026-07-15"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, log := setupConformanceServer(t, 0)
			invoker := NewInvoker()
			defer invoker.Close()

			call := invoker.InvokeBinding(bg(), invocationArgs(ts.URL, "resourceTemplates/file:///logs/{date}", nil))
			if err := call.Write(bg(), tc.input); err != nil {
				t.Fatal(err)
			}
			vals, err := drainOutputs(t, call)
			if len(vals) != 0 {
				t.Fatalf("expected no outputs, got %v", vals)
			}
			if codeOf(t, err) != openbindings.ErrCodeValidationFailed {
				t.Errorf("code = %q, want ERR_VALIDATION_FAILED", codeOf(t, err))
			}
			if n := log.count("resources/read"); n != 0 {
				t.Errorf("refusal must precede dispatch; server saw %d resources/read requests", n)
			}
		})
	}
}

// mustContent marshals a Go value into the raw-JSON content carriage
// (Source.Content presence semantics: raw JSON, nil = absent).
func mustContent(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
