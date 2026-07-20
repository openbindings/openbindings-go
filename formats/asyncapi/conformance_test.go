package asyncapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/coder/websocket"
)

// Conformance tests for the openbindings.asyncapi@1 remainder: the server
// and address configuration points (ASYNC-P-04), protocol-bindings honoring
// (ASYNC-P-02), SSE establishment and WHATWG event framing (§8), and the
// §9.1/§9.3 encode/decode lanes (ASYNC-P-03, ASYNC-P-05).

// hostOf strips the scheme from an httptest server URL.
func hostOf(srv *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")
}

// ---------------------------------------------------------------------------
// Address parameters (ASYNC-P-04)
// ---------------------------------------------------------------------------

// paramDoc declares a parameterized channel address: {roomId} has no
// default, {lane} declares one.
func paramDoc(srv *httptest.Server) *document {
	return &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers:  map[string]server{"test": {Host: hostOf(srv), Protocol: "http"}},
		Channels: map[string]channel{
			"rooms": {
				Address: "/rooms/{roomId}/{lane}",
				Parameters: map[string]parameter{
					"roomId": {},
					"lane":   {Default: "main"},
				},
			},
		},
		Operations: map[string]asyncOperation{
			"post": {Action: "receive", Channel: channelRef{Ref: "#/channels/rooms"}},
		},
	}
}

// TestAddressParameterExpansion verifies ASYNC-P-04's address point:
// `{name}` expressions expand from consumer-supplied parameter values, else
// the declared parameter's default — and an expression left unresolved
// after defaults is a pre-dispatch refusal: literal braces never reach the
// wire.
func TestAddressParameterExpansion(t *testing.T) {
	var requests atomic.Int32
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		gotPath = r.URL.Path
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	binv := NewInvoker()
	defer binv.Close()

	publish := func(bindCtx map[string]any) error {
		call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(paramDoc(srv))},
			Ref:     "#/operations/post",
			Context: bindCtx,
		})
		if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
			return err
		}
		if err := call.Close(); err != nil {
			return err
		}
		_, err := drainOutputs(t, call)
		return err
	}

	// Supplied roomId + defaulted lane.
	if err := publish(map[string]any{"configuration": map[string]any{
		"address": map[string]any{"parameters": map[string]any{"roomId": "general"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/rooms/general/main" {
		t.Errorf("path = %q, want /rooms/general/main (supplied value + declared default)", gotPath)
	}

	// A supplied value overrides a declared default.
	if err := publish(map[string]any{"configuration": map[string]any{
		"address": map[string]any{"parameters": map[string]any{"roomId": "ops", "lane": "audit"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/rooms/ops/audit" {
		t.Errorf("path = %q, want /rooms/ops/audit", gotPath)
	}

	// Unresolved after defaults: pre-dispatch refusal, braces never dialed.
	before := requests.Load()
	err := publish(nil)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR for an unresolved address parameter, got %v", err)
	}
	if got := requests.Load(); got != before {
		t.Errorf("the refusal is pre-dispatch: %d requests dispatched", got-before)
	}
}

// TestAddressParameterEnumViolationRefused verifies a supplied parameter
// value outside the declared enum is refused loudly (the Parameter Object's
// own constraint, incorporated).
func TestAddressParameterEnumViolationRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	doc := paramDoc(srv)
	ch := doc.Channels["rooms"]
	ch.Parameters["roomId"] = parameter{Enum: []string{"general", "ops"}}
	doc.Channels["rooms"] = ch

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:    "#/operations/post",
		Context: map[string]any{"configuration": map[string]any{
			"address": map[string]any{"parameters": map[string]any{"roomId": "backstage"}},
		}},
	})
	if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR for an out-of-enum parameter value, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Server set, server variables, URL assembly (ASYNC-P-04)
// ---------------------------------------------------------------------------

// TestServerVariablesAndPathnameAssembly verifies the target URL assembly:
// server variables substitute into host and pathname from consumer-supplied
// values, else declared defaults; the path is the pathname concatenated
// with the expanded address, exactly one `/` at the join; an
// unsubstitutable variable is a pre-dispatch refusal.
func TestServerVariablesAndPathnameAssembly(t *testing.T) {
	var requests atomic.Int32
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		gotPath = r.URL.Path
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)

	doc := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers: map[string]server{
			"test": {
				Host:     hostOf(srv),
				Protocol: "http",
				PathName: "/{version}/", // trailing slash: the join still yields exactly one /
				Variables: map[string]serverVariable{
					"version": {Default: "v1", Enum: []string{"v1", "v2"}},
				},
			},
		},
		Channels: map[string]channel{"events": {Address: "/events"}},
		Operations: map[string]asyncOperation{
			"post": {Action: "receive", Channel: channelRef{Ref: "#/channels/events"}},
		},
	}

	binv := NewInvoker()
	defer binv.Close()
	publish := func(bindCtx map[string]any) error {
		call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
			Ref:     "#/operations/post",
			Context: bindCtx,
		})
		if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
			return err
		}
		if err := call.Close(); err != nil {
			return err
		}
		_, err := drainOutputs(t, call)
		return err
	}

	// Declared default.
	if err := publish(nil); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/events" {
		t.Errorf("path = %q, want /v1/events (default-substituted pathname + address, one slash at the join)", gotPath)
	}

	// Consumer-supplied variable value via the server configuration point.
	if err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"variables": map[string]any{"version": "v2"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2/events" {
		t.Errorf("path = %q, want /v2/events (consumer-supplied server variable)", gotPath)
	}

	// A supplied value outside the declared enum is refused.
	if err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"variables": map[string]any{"version": "v3"}},
	}}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR for an out-of-enum server variable, got %v", err)
	}

	// No default and no supplied value: pre-dispatch refusal.
	docNoDefault := *doc
	docNoDefault.Servers = map[string]server{
		"test": {Host: hostOf(srv), Protocol: "http", PathName: "/{version}",
			Variables: map[string]serverVariable{"version": {}}},
	}
	before := requests.Load()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(&docNoDefault)},
		Ref:    "#/operations/post",
	})
	if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR for an unsubstitutable server variable, got %v", err)
	}
	if got := requests.Load(); got != before {
		t.Errorf("the refusal is pre-dispatch: %d requests dispatched", got-before)
	}
}

// TestChannelServersSubsetInArrayOrder verifies the effective server set
// (ASYNC-P-04): a non-empty channel `servers` array selects that subset in
// the ARTIFACT'S OWN ARRAY ORDER (the default connects to its first
// bound-protocol member), while an absent/empty channel `servers` means ALL
// document servers in lexicographic key order, unbound protocols skipped.
func TestChannelServersSubsetInArrayOrder(t *testing.T) {
	var hitsA, hitsB atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsA.Add(1)
		w.WriteHeader(202)
	}))
	t.Cleanup(srvA.Close)
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitsB.Add(1)
		w.WriteHeader(202)
	}))
	t.Cleanup(srvB.Close)

	mkDoc := func(channelServers []serverRef) *document {
		return &document{
			AsyncAPI: "3.0.0",
			Info:     info{Title: "t", Version: "1"},
			Servers: map[string]server{
				// Lexicographically first, but an out-of-revision protocol:
				// the doc-order default must SKIP it, never refuse on it.
				"aKafka": {Host: "broker.example.com:9092", Protocol: "kafka"},
				"bHTTP":  {Host: hostOf(srvA), Protocol: "http"},
				"zHTTP":  {Host: hostOf(srvB), Protocol: "http"},
			},
			Channels: map[string]channel{
				"c": {Address: "/c", Servers: channelServers},
			},
			Operations: map[string]asyncOperation{
				"post": {Action: "receive", Channel: channelRef{Ref: "#/channels/c"}},
			},
		}
	}

	binv := NewInvoker()
	defer binv.Close()
	publish := func(doc *document, bindCtx map[string]any) error {
		call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
			Ref:     "#/operations/post",
			Context: bindCtx,
		})
		if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
			return err
		}
		if err := call.Close(); err != nil {
			return err
		}
		_, err := drainOutputs(t, call)
		return err
	}

	// Non-empty channel servers subset: array order wins — zHTTP is listed
	// FIRST, so it is the default even though bHTTP sorts before it.
	if err := publish(mkDoc([]serverRef{{Ref: "#/servers/zHTTP"}, {Ref: "#/servers/bHTTP"}}), nil); err != nil {
		t.Fatal(err)
	}
	if hitsB.Load() != 1 || hitsA.Load() != 0 {
		t.Fatalf("subset array order must win: hits A=%d B=%d, want A=0 B=1", hitsA.Load(), hitsB.Load())
	}

	// Absent channel servers = ALL doc servers in lexicographic key order:
	// aKafka (unbound) is skipped, bHTTP selected.
	if err := publish(mkDoc(nil), nil); err != nil {
		t.Fatal(err)
	}
	if hitsA.Load() != 1 {
		t.Fatalf("doc-order default must select the first bound server: hits A=%d B=%d", hitsA.Load(), hitsB.Load())
	}

	// Consumer configuration selects another member of the effective set by
	// name.
	if err := publish(mkDoc(nil), map[string]any{"configuration": map[string]any{
		"server": map[string]any{"name": "zHTTP"},
	}}); err != nil {
		t.Fatal(err)
	}
	if hitsB.Load() != 2 {
		t.Fatalf("configuration.server.name must select the named member: hits A=%d B=%d", hitsA.Load(), hitsB.Load())
	}

	// A name outside the effective set is a refusal.
	if err := publish(mkDoc([]serverRef{{Ref: "#/servers/zHTTP"}}), map[string]any{"configuration": map[string]any{
		"server": map[string]any{"name": "bHTTP"},
	}}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal selecting a server outside the channel's effective set, got %v", err)
	}
}

// TestOnlyUnboundProtocolServersIsRefused verifies "no resolvable server is
// a pre-dispatch refusal": a document declaring only out-of-revision
// protocols (kafka, mqtt, …) refuses rather than guessing.
func TestOnlyUnboundProtocolServersIsRefused(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers: map[string]server{
			"broker": {Host: "broker.example.com:9092", Protocol: "kafka"},
		},
		Channels:   map[string]channel{"c": {Address: "/c"}},
		Operations: map[string]asyncOperation{"post": {Action: "receive", Channel: channelRef{Ref: "#/channels/c"}}},
	}
	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:    "#/operations/post",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR with only unbound protocols declared, got %v", err)
	}
}

// TestFullURLOverride verifies the full-URL override lane: the URL's scheme
// decides the protocol, an out-of-revision scheme is refused pre-dispatch,
// and the declared security of the server the default selection would have
// targeted STILL applies (§9.5).
func TestFullURLOverride(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)

	doc := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers: map[string]server{
			"prod": {Host: "unreachable.example.com", Protocol: "https",
				Security: []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}}},
		},
		Channels:   map[string]channel{"c": {Address: "/c"}},
		Operations: map[string]asyncOperation{"post": {Action: "receive", Channel: channelRef{Ref: "#/channels/c"}}},
		Components: &components{SecuritySchemes: map[string]securityScheme{
			"bearer": {Type: "http", Scheme: "bearer"},
		}},
	}

	binv := NewInvoker()
	defer binv.Close()
	publish := func(bindCtx map[string]any) error {
		call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
			Ref:     "#/operations/post",
			Context: bindCtx,
		})
		if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
			return err
		}
		if err := call.Close(); err != nil {
			return err
		}
		_, err := drainOutputs(t, call)
		return err
	}

	// Out-of-revision scheme: refused pre-dispatch.
	if err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"url": "ftp://files.example.com"},
	}}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal of an out-of-revision scheme, got %v", err)
	}

	// The default-selected server's declared security still applies under a
	// full-URL override: the challenge fires before any I/O.
	before := requests.Load()
	err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"url": srv.URL},
	}})
	if codeOf(t, err) != openbindings.ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED under a full-URL override, got %v", err)
	}
	if got := requests.Load(); got != before {
		t.Errorf("the challenge precedes any I/O: %d requests dispatched", got-before)
	}

	// With the credential supplied, the override URL is dialed.
	if err := publish(map[string]any{
		"bearerToken":   "tok",
		"configuration": map[string]any{"server": map[string]any{"url": srv.URL}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != before+1 {
		t.Errorf("expected the override URL to be dialed once, got %d requests", got-before)
	}
}

// ---------------------------------------------------------------------------
// Protocol bindings (ASYNC-P-02)
// ---------------------------------------------------------------------------

// TestHTTPBindingMethodOverride verifies the http operation binding's
// `method` is authoritative where it speaks: a publish uses it in place of
// POST, an SSE subscription in place of GET.
func TestHTTPBindingMethodOverride(t *testing.T) {
	var publishMethod, sseMethod atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/in":
			publishMethod.Store(r.Method)
			w.WriteHeader(202)
		case "/out":
			sseMethod.Store(r.Method)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"seq\":1}\n\n")
		}
	}))
	t.Cleanup(srv.Close)

	doc := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers:  map[string]server{"test": {Host: hostOf(srv), Protocol: "http"}},
		Channels: map[string]channel{
			"in":  {Address: "/in"},
			"out": {Address: "/out"},
		},
		Operations: map[string]asyncOperation{
			"post": {Action: "receive", Channel: channelRef{Ref: "#/channels/in"},
				Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "PUT"}}},
			"sub": {Action: "send", Channel: channelRef{Ref: "#/channels/out"},
				Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}}},
		},
	}

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:    "#/operations/post",
	})
	if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := drainOutputs(t, call); err != nil {
		t.Fatal(err)
	}
	if got := publishMethod.Load(); got != "PUT" {
		t.Errorf("publish method = %v, want the binding-declared PUT", got)
	}

	sub := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:    "#/operations/sub",
	})
	vals, err := drainOutputs(t, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 event, got %d", len(vals))
	}
	if got := sseMethod.Load(); got != "POST" {
		t.Errorf("SSE establishment method = %v, want the binding-declared POST", got)
	}
}

// wsBindingDoc declares a ws channel binding with a required query
// property, a defaulted query property, a defaulted header, and a required
// header.
func wsBindingDoc(srv *httptest.Server) *document {
	return &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers:  map[string]server{"test": {Host: hostOf(srv), Protocol: "ws"}},
		Channels: map[string]channel{
			"stream": {
				Address: "/ws",
				Bindings: &channelBindings{WS: &wsChannelBinding{
					Method: "GET",
					Query: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"token": map[string]any{"type": "string"},
							"lane":  map[string]any{"type": "string", "default": "live"},
						},
						"required": []any{"token"},
					},
					Headers: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"X-Client": map[string]any{"type": "string", "default": "ob"},
							"X-Trace":  map[string]any{"type": "string"},
						},
						"required": []any{"X-Trace"},
					},
				}},
			},
		},
		Operations: map[string]asyncOperation{
			"publish": {Action: "receive", Channel: channelRef{Ref: "#/channels/stream"}},
		},
	}
}

// TestWSBindingQueryAndHeadersGovernUpgrade verifies §8: the websockets
// channel binding's declared query and header values ride the upgrade
// request — consumer-supplied like address parameters, else declared
// defaults (the context's generic header carriage satisfies a header
// declaration too) — and an unsatisfied required declaration is a
// pre-dispatch refusal (no upgrade is ever dialed).
func TestWSBindingQueryAndHeadersGovernUpgrade(t *testing.T) {
	var upgrades atomic.Int32
	type seen struct{ token, lane, xClient, xTrace string }
	seenCh := make(chan seen, 4)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		seenCh <- seen{
			token:   r.URL.Query().Get("token"),
			lane:    r.URL.Query().Get("lane"),
			xClient: r.Header.Get("X-Client"),
			xTrace:  r.Header.Get("X-Trace"),
		}
		for {
			if _, err := readWSJSON(ctx, conn); err != nil {
				return
			}
		}
	})

	binv := NewInvoker()
	defer binv.Close()
	publish := func(bindCtx map[string]any) error {
		call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(wsBindingDoc(srv))},
			Ref:     "#/operations/publish",
			Context: bindCtx,
		})
		if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
			return err
		}
		if err := call.Close(); err != nil {
			return err
		}
		_, err := drainOutputs(t, call)
		return err
	}

	// Required query via the parameter bag; required header via the generic
	// header carriage; defaults fill the rest.
	if err := publish(map[string]any{
		"headers": map[string]any{"X-Trace": "trace-1"},
		"configuration": map[string]any{
			"address": map[string]any{"parameters": map[string]any{"token": "qtok"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seenCh:
		if got.token != "qtok" {
			t.Errorf("query token = %q, want the supplied qtok", got.token)
		}
		if got.lane != "live" {
			t.Errorf("query lane = %q, want the declared default live", got.lane)
		}
		if got.xClient != "ob" {
			t.Errorf("X-Client = %q, want the declared default ob", got.xClient)
		}
		if got.xTrace != "trace-1" {
			t.Errorf("X-Trace = %q, want the context-supplied trace-1", got.xTrace)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw the upgrade")
	}

	// Unsatisfied required declarations: pre-dispatch refusals, no upgrade.
	before := upgrades.Load()
	if err := publish(map[string]any{
		"headers": map[string]any{"X-Trace": "trace-2"},
	}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal for the missing required query property, got %v", err)
	}
	if err := publish(map[string]any{
		"configuration": map[string]any{
			"address": map[string]any{"parameters": map[string]any{"token": "qtok"}},
		},
	}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal for the missing required header, got %v", err)
	}
	if got := upgrades.Load(); got != before {
		t.Errorf("required-declaration refusals are pre-dispatch: %d upgrades dialed", got-before)
	}
}

// TestWSBindingNonGETMethodRefused verifies a declared upgrade method this
// platform cannot apply (RFC 6455 upgrades are GET) is refused loudly
// pre-dispatch — surfaced, never silently rerouted.
func TestWSBindingNonGETMethodRefused(t *testing.T) {
	var upgrades atomic.Int32
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		<-ctx.Done()
	})

	doc := wsBindingDoc(srv)
	ch := doc.Channels["stream"]
	ch.Bindings = &channelBindings{WS: &wsChannelBinding{Method: "POST"}}
	doc.Channels["stream"] = ch

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:    "#/operations/publish",
	})
	if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal of a POST upgrade method, got %v", err)
	}
	if c := upgrades.Load(); c != 0 {
		t.Errorf("the refusal is pre-dispatch: %d upgrades dialed", c)
	}
}

// ---------------------------------------------------------------------------
// SSE establishment and WHATWG event framing (§8)
// ---------------------------------------------------------------------------

// TestSSEEstablishmentRequiresEventStreamContentType verifies §8's
// establishment pin (ASYNC-P-06): a 2xx response NOT bearing the
// text/event-stream content type is a protocol-error failure, never a
// silently-consumed stream.
func TestSSEEstablishmentRequiresEventStreamContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"not": "a stream"})
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(sseEventDoc(srv.URL, "/"))},
		Ref:    "#/operations/receiveCaps",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeProtocol {
		t.Fatalf("expected ERR_PROTOCOL for a non-event-stream 2xx, got %v", err)
	}
}

// TestSSEWHATWGFraming verifies the incorporated WHATWG event framing:
// multi-line data joins with U+000A; comment-only and empty-data events
// emit nothing; `event`/`id`/`retry` never enter the output value; exactly
// one leading space is stripped from a field value; an incomplete final
// event (no dispatching blank line before EOF) is discarded.
func TestSSEWHATWGFraming(t *testing.T) {
	body := strings.Join([]string{
		": stream comment", // comment-only event
		"",
		"event: greeting",
		"id: 41",
		"data: hello",
		"data:  indented", // ONE leading space stripped: " indented"
		"",
		"data:", // empty-data event: emits nothing
		"",
		"retry: 5000", // framing only; never acted on (no reconnect in @1)
		"data: second",
		"",
		"data: never dispatched (EOF before blank line)",
	}, "\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(sseEventDoc(srv.URL, "/"))},
		Ref:    "#/operations/receiveCaps",
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"hello\n indented", "second"}
	if len(vals) != len(want) {
		t.Fatalf("expected %d events %v, got %d: %v", len(want), want, len(vals), vals)
	}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("event %d = %q, want %q", i, vals[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Encode/decode lanes (ASYNC-P-03, ASYNC-P-05)
// ---------------------------------------------------------------------------

// laneDoc builds a publish+subscribe doc whose channel message declares the
// given content type (empty string = no declaration anywhere).
func laneDoc(srv *httptest.Server, proto, contentType string) *document {
	msg := message{Name: "m"}
	if contentType != "" {
		msg.ContentType = contentType
	}
	return &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers:  map[string]server{"test": {Host: hostOf(srv), Protocol: proto}},
		Channels: map[string]channel{
			"c": {Address: "/c", Messages: map[string]message{"m": msg}},
		},
		Operations: map[string]asyncOperation{
			"post": {Action: "receive", Channel: channelRef{Ref: "#/channels/c"},
				Messages: []messageRef{{Ref: "#/channels/c/messages/m"}},
				Reply:    &operationReply{Messages: []messageRef{{Ref: "#/channels/c/messages/m"}}}},
			"sub": {Action: "send", Channel: channelRef{Ref: "#/channels/c"},
				Messages: []messageRef{{Ref: "#/channels/c/messages/m"}}},
		},
	}
}

// TestInputTextLane verifies §9.1's text lane: a declared text-family type
// sends a string value raw (with the declared type on the wire) and refuses
// a non-string value — before any request is dispatched.
func TestInputTextLane(t *testing.T) {
	var requests atomic.Int32
	var gotBody atomic.Value
	var gotCT atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		gotCT.Store(r.Header.Get("Content-Type"))
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	publish := func(v any) error {
		call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
			Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(laneDoc(srv, "http", "text/plain"))},
			Ref:    "#/operations/post",
		})
		if err := call.Write(bg(), v); err != nil {
			return err
		}
		if err := call.Close(); err != nil {
			return err
		}
		_, err := drainOutputs(t, call)
		return err
	}

	if err := publish("raw payload, not JSON-quoted"); err != nil {
		t.Fatal(err)
	}
	if got := gotBody.Load(); got != "raw payload, not JSON-quoted" {
		t.Errorf("body = %q, want the string sent RAW (never JSON-encoded)", got)
	}
	if got := gotCT.Load(); got != "text/plain" {
		t.Errorf("Content-Type = %q, want the declared text/plain", got)
	}

	before := requests.Load()
	err := publish(map[string]any{"not": "a string"})
	if codeOf(t, err) != openbindings.ErrCodeValidationFailed {
		t.Fatalf("expected ERR_VALIDATION_FAILED for a non-string value on the text lane, got %v", err)
	}
	if got := requests.Load(); got != before {
		t.Errorf("the text-lane refusal precedes dispatch: %d requests sent", got-before)
	}
}

// TestInputExcludedFamilyRefusedPreDispatch verifies §9.1's exclusion: a
// declared non-JSON non-text family (avro, protobuf, binary, …) is refused
// BEFORE dispatch — on the HTTP cell before any request, on the WS cell
// before any upgrade.
func TestInputExcludedFamilyRefusedPreDispatch(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(laneDoc(srv, "http", "avro/binary"))},
		Ref:    "#/operations/post",
	})
	if err := call.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR for an excluded declared family, got %v", err)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("the exclusion refusal is pre-dispatch: %d requests sent", got)
	}

	// WS cell: refused before any socket is dialed.
	var upgrades atomic.Int32
	wsSrv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		upgrades.Add(1)
		<-ctx.Done()
	})
	wsDoc := laneDoc(wsSrv, "ws", "application/octet-stream")
	c := wsDoc.Channels["c"]
	c.Address = "/ws"
	wsDoc.Channels["c"] = c
	wsCall := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(wsDoc)},
		Ref:    "#/operations/post",
	})
	if err := wsCall.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := wsCall.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = drainOutputs(t, wsCall)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR for an excluded family on the ws cell, got %v", err)
	}
	if c := upgrades.Load(); c != 0 {
		t.Errorf("the exclusion refusal precedes the dial: %d upgrades", c)
	}
}

// TestDecodeTextLaneAndReplyDirection verifies §9.3 end to end: a publish
// output decodes by the REPLY-side declaration — text/plain keeps the body
// a raw string even when it happens to look like JSON — and a governing set
// with two distinct effective types falls to the text lane rather than
// guessing.
func TestDecodeTextLaneAndReplyDirection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/c":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"looks":"like json"}`)
		case "/mixed":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"seq\":1}\n\n")
		}
	}))
	t.Cleanup(srv.Close)

	// Reply-side declares text/plain: the response body stays a raw string
	// (the lane is the DECLARATION's, never sniffed from bytes or headers).
	doc := laneDoc(srv, "http", "text/plain")
	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:    "#/operations/post",
	})
	if err := call.Write(bg(), "ping"); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	if vals[0] != `{"looks":"like json"}` {
		t.Errorf("output = %v (%T), want the raw string (declared text/plain, never sniffed)", vals[0], vals[0])
	}

	// Two distinct effective types govern the subscription: ambiguous →
	// text lane, events stay strings.
	mixed := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers:  map[string]server{"test": {Host: hostOf(srv), Protocol: "http"}},
		Channels: map[string]channel{
			"mixed": {Address: "/mixed", Messages: map[string]message{
				"j": {Name: "j", ContentType: "application/json"},
				"t": {Name: "t", ContentType: "text/plain"},
			}},
		},
		Operations: map[string]asyncOperation{
			"sub": {Action: "send", Channel: channelRef{Ref: "#/channels/mixed"}},
		},
	}
	sub := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(mixed)},
		Ref:    "#/operations/sub",
	})
	events, err := drainOutputs(t, sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0] != `{"seq":1}` {
		t.Errorf("ambiguous declaration must decode on the text lane, got %v (%T)", events[0], events[0])
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
