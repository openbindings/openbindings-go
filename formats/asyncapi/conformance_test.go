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

	"github.com/coder/websocket"
	openbindings "github.com/openbindings/openbindings-go"
)

// Conformance tests for the openbindings.asyncapi@1 remainder: the server
// and address configuration points (ASYNC-P-04), protocol-bindings honoring
// (ASYNC-P-02), SSE establishment and WHATWG event framing (§8), and the
// §9.1/§9.3 encode/decode lanes (ASYNC-P-03, ASYNC-P-05).

// hostOf strips the scheme from an httptest server URL.
func hostOf(srv *httptest.Server) string {
	return strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")
}

func testJSONChannel(address string) channel {
	return channel{
		Address:  address,
		Messages: map[string]message{"json": {Name: "json", ContentType: "application/json"}},
	}
}

func testHTTPPublish(channelName, method string) asyncOperation {
	return asyncOperation{
		Action:   "receive",
		Channel:  channelRef{Ref: "#/channels/" + channelName},
		Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: method}},
	}
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
				Address:  "/rooms/{roomId}/{lane}",
				Messages: map[string]message{"json": {Name: "json", ContentType: "application/json"}},
				Parameters: map[string]parameter{
					"roomId": {},
					"lane":   {Default: "main"},
				},
			},
		},
		Operations: map[string]asyncOperation{
			"post": {
				Action: "receive", Channel: channelRef{Ref: "#/channels/rooms"},
				Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}},
			},
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

	// Unresolved after defaults: braces never dialed. R1a: a resolvable-
	// missing address parameter is a config.value CONTEXT_REQUIRED, not a
	// terminal ERR_SOURCE_CONFIG_ERROR.
	before := requests.Load()
	err := publish(nil)
	assertConfigValue(t, err, "address", "roomId")
	if got := requests.Load(); got != before {
		t.Errorf("the refusal is pre-dispatch: %d requests dispatched", got-before)
	}
}

// TestAddressParameterEnumIsAuthoritative verifies that a supplied parameter
// value outside the artifact-declared enum is refused before dispatch.
func TestAddressParameterEnumIsAuthoritative(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
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
	if _, err := drainOutputs(t, call); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("an out-of-enum address parameter value must be refused: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("out-of-enum refusal must precede dispatch; got %d requests", requests.Load())
	}
}

// ---------------------------------------------------------------------------
// Server set, server variables, URL assembly (ASYNC-P-04)
// ---------------------------------------------------------------------------

// TestServerVariablesAndPathnameAssembly verifies the target URL assembly:
// server variables substitute from consumer-supplied values (the key form's
// `variables` member, §9.2), else declared defaults; the path is the
// pathname concatenated with the expanded address, exactly one `/` at the
// join; an unsubstitutable variable and a supplied value outside the
// declared enum are pre-dispatch refusals.
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
		Channels: map[string]channel{"events": testJSONChannel("/events")},
		Operations: map[string]asyncOperation{
			"post": testHTTPPublish("events", "POST"),
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

	// A supplied value at the key form's `variables` member wins over the
	// declared default (§9.2's supplied-else-default substitution).
	if err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "test", "variables": map[string]any{"version": "v2"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2/events" {
		t.Errorf("path = %q, want /v2/events (supplied-substituted pathname + address)", gotPath)
	}

	// A supplied value outside the variable's declared enum is refused.
	beforeEnum := requests.Load()
	if err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "test", "variables": map[string]any{"version": "v9"}},
	}}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("an out-of-enum supplied value must be refused: %v", err)
	}
	if requests.Load() != beforeEnum {
		t.Fatal("out-of-enum supplied value dispatched")
	}

	// A declared default outside the variable's own declared enum is an
	// artifact inconsistency and is likewise refused.
	docBadDefault := *doc
	docBadDefault.Servers = map[string]server{
		"test": {Host: hostOf(srv), Protocol: "http", PathName: "/{version}",
			Variables: map[string]serverVariable{
				"version": {Default: "v9", Enum: []string{"v1", "v2"}},
			}},
	}
	callBad := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(&docBadDefault)},
		Ref:    "#/operations/post",
	})
	if err := callBad.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := callBad.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := drainOutputs(t, callBad); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("an out-of-enum declared default must be refused: %v", err)
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
	// R1a: an unsubstitutable server variable is a resolvable-missing config
	// value, surfaced as a config.value CONTEXT_REQUIRED (retryable), not a
	// terminal ERR_SOURCE_CONFIG_ERROR.
	assertConfigValue(t, err, "server", "version")
	if got := requests.Load(); got != before {
		t.Errorf("the refusal is pre-dispatch: %d requests dispatched", got-before)
	}

	// The same undefaulted variable IS satisfiable by supply — AsyncAPI
	// declares Server Variable defaults OPTIONAL, so consumer supply is the
	// only way to satisfy it (the carriage §9.2's assembly rule
	// presupposes).
	callSupplied := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(&docNoDefault)},
		Ref:    "#/operations/post",
		Context: map[string]any{"configuration": map[string]any{
			"server": map[string]any{"key": "test", "variables": map[string]any{"version": "v7"}},
		}},
	})
	if err := callSupplied.Write(bg(), map[string]any{"m": 1}); err != nil {
		t.Fatal(err)
	}
	if err := callSupplied.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := drainOutputs(t, callSupplied); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v7/events" {
		t.Errorf("path = %q, want /v7/events (undefaulted variable satisfied by supply)", gotPath)
	}
}

// TestChannelServersSubsetInArrayOrder verifies the effective server set
// (ASYNC-P-04): a non-empty channel `servers` array selects that subset in
// artifact order, while absent/empty means all document servers in key
// order. Neither ordering invents identity when several bindable members
// remain.
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
				"c": func() channel {
					ch := testJSONChannel("/c")
					ch.Servers = channelServers
					return ch
				}(),
			},
			Operations: map[string]asyncOperation{
				"post": testHTTPPublish("c", "POST"),
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

	subset := mkDoc([]serverRef{{Ref: "#/servers/zHTTP"}, {Ref: "#/servers/bHTTP"}})
	if err := publish(subset, nil); codeOf(t, err) != openbindings.ErrCodeContextRequired {
		t.Fatalf("several subset members must require selection, got %v", err)
	}
	if err := publish(subset, map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "zHTTP"},
	}}); err != nil {
		t.Fatal(err)
	}
	if hitsB.Load() != 1 || hitsA.Load() != 0 {
		t.Fatalf("explicit subset selection: hits A=%d B=%d, want A=0 B=1", hitsA.Load(), hitsB.Load())
	}

	all := mkDoc(nil)
	if err := publish(all, nil); codeOf(t, err) != openbindings.ErrCodeContextRequired {
		t.Fatalf("several document members must require selection, got %v", err)
	}
	if err := publish(all, map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "bHTTP"},
	}}); err != nil {
		t.Fatal(err)
	}
	if hitsA.Load() != 1 {
		t.Fatalf("configuration.server.key must select bHTTP: hits A=%d B=%d", hitsA.Load(), hitsB.Load())
	}

	// A key outside the effective set is a refusal.
	if err := publish(mkDoc([]serverRef{{Ref: "#/servers/zHTTP"}}), map[string]any{"configuration": map[string]any{
		"server": map[string]any{"key": "bHTTP"},
	}}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal selecting a server outside the channel's effective set, got %v", err)
	}
}

// TestServerConfigurationPinnedShapesOnly verifies this SDK's composable
// server-point carriage end to end.
func TestServerConfigurationPinnedShapesOnly(t *testing.T) {
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
			"test": {Host: hostOf(srv), Protocol: "http"},
		},
		Channels:   map[string]channel{"c": testJSONChannel("/c")},
		Operations: map[string]asyncOperation{"post": testHTTPPublish("c", "POST")},
	}

	binv := NewInvoker()
	defer binv.Close()
	publish := func(serverCfg any) error {
		call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
			Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
			Ref:     "#/operations/post",
			Context: map[string]any{"configuration": map[string]any{"server": serverCfg}},
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

	refused := []struct {
		name  string
		cfg   any
		teach string
	}{
		{"bare string", "test", "must be an object"},
		{"retired name member", map[string]any{"name": "test"}, `member "name" is not pinned`},
		{"empty object", map[string]any{}, `carries none of "key", "variables", or "url"`},
	}
	for _, tc := range refused {
		before := requests.Load()
		err := publish(tc.cfg)
		if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
			t.Fatalf("%s: expected ERR_SOURCE_CONFIG_ERROR, got %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.teach) {
			t.Errorf("%s: refusal must teach the pin (%q), got %v", tc.name, tc.teach, err)
		}
		if !strings.Contains(err.Error(), `{"key": "<server-name>"?`) || !strings.Contains(err.Error(), `"url": "<connection-url>"?`) {
			t.Errorf("%s: refusal must name the composable shape, got %v", tc.name, err)
		}
		if got := requests.Load(); got != before {
			t.Errorf("%s: the refusal is pre-dispatch, %d requests dispatched", tc.name, got-before)
		}
	}

	// Selection, sole-member URL replacement, and their composition dispatch.
	if err := publish(map[string]any{"key": "test"}); err != nil {
		t.Fatal(err)
	}
	if err := publish(map[string]any{"url": srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := publish(map[string]any{"key": "test", "url": srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 3 {
		t.Errorf("expected exactly three valid composable-form dispatches, got %d", got)
	}
}

// TestOnlyUnboundProtocolServersIsRefused verifies that an artifact target
// remains valid while a missing local protocol driver fails pre-dispatch.
func TestOnlyUnboundProtocolServersIsRefused(t *testing.T) {
	doc := &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers: map[string]server{
			"broker": {Host: "broker.example.com:9092", Protocol: "kafka"},
		},
		Channels:   map[string]channel{"c": testJSONChannel("/c")},
		Operations: map[string]asyncOperation{"post": testHTTPPublish("c", "POST")},
	}
	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:    "#/operations/post",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != "DRIVER_UNAVAILABLE" {
		t.Fatalf("expected local driver capability refusal, got %v", err)
	}
}

// TestFullURLOverride verifies the full-URL replacement lane: it refines the
// selected artifact server without changing its protocol, an incompatible
// scheme is refused pre-dispatch, and the selected server's declared security
// STILL applies (§9.5).
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
			"prod": {Host: "unreachable.example.com", Protocol: "http",
				Security: []securityRequirement{{Ref: "#/components/securitySchemes/bearer"}}},
		},
		Channels:   map[string]channel{"c": testJSONChannel("/c")},
		Operations: map[string]asyncOperation{"post": testHTTPPublish("c", "POST")},
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

	// A scheme incompatible with the selected server: refused pre-dispatch.
	if err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"url": "ftp://files.example.com"},
	}}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal of an out-of-revision scheme, got %v", err)
	}

	// The sole selected server's declared security still applies under a
	// full-URL replacement: the challenge fires before any I/O.
	before := requests.Load()
	err := publish(map[string]any{"configuration": map[string]any{
		"server": map[string]any{"url": srv.URL},
	}})
	if codeOf(t, err) != openbindings.ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED under a full-URL replacement, got %v", err)
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

// TestHTTPBindingMethodOverride verifies that a supported HTTP publish uses
// the artifact-declared method and that a standalone HTTP send operation
// remains excluded even when it declares one.
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
			"in":  testJSONChannel("/in"),
			"out": testJSONChannel("/out"),
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
	if _, err := drainOutputs(t, sub); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("standalone HTTP send must be refused, got %v", err)
	}
	if got := sseMethod.Load(); got != nil {
		t.Errorf("excluded HTTP send dispatched with method %v", got)
	}
}

// wsBindingDoc declares WebSocket upgrade query and header schemas. JSON
// Schema `default` values remain annotations and are not invented as wire
// values; concrete values come from configuration.protocolFields.
func wsBindingDoc(srv *httptest.Server) *document {
	return &document{
		AsyncAPI: "3.0.0",
		Info:     info{Title: "t", Version: "1"},
		Servers:  map[string]server{"test": {Host: hostOf(srv), Protocol: "ws"}},
		Channels: map[string]channel{
			"stream": {
				Address:  "/ws",
				Messages: map[string]message{"json": {Name: "json", ContentType: "application/json"}},
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
			Context: wsTextContext(bindCtx),
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

	// Query and header values ride the named protocolFields point. Values
	// whose schemas carry `default` annotations are supplied explicitly too.
	if err := publish(map[string]any{
		"configuration": map[string]any{
			"protocolFields": map[string]any{
				"webSocketQuery":   map[string]any{"token": "qtok", "lane": "live"},
				"webSocketHeaders": map[string]any{"X-Trace": "trace-1", "X-Client": "ob"},
			},
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
		"configuration": map[string]any{"protocolFields": map[string]any{
			"webSocketHeaders": map[string]any{"X-Trace": "trace-2"},
		}},
	}); codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected refusal for the missing required query property, got %v", err)
	}
	if err := publish(map[string]any{
		"configuration": map[string]any{"protocolFields": map[string]any{
			"webSocketQuery": map[string]any{"token": "qtok"},
		}},
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
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:     "#/operations/publish",
		Context: wsTextContext(nil),
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

// TestStandaloneHTTPSendIsExcluded verifies the revision-1 cell boundary:
// an AsyncAPI `send` over HTTP is not silently interpreted as SSE, even when
// the endpoint would have returned an event stream.
func TestStandaloneHTTPSendIsExcluded(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: should-not-dispatch\n\n")
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(sseEventDoc(srv.URL, "/"))},
		Ref:    "#/operations/receiveCaps",
	})
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != openbindings.ErrCodeSourceConfigError {
		t.Fatalf("expected revision-1 exclusion for standalone HTTP send, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("excluded cell performed %d requests", requests.Load())
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
				Reply:    &operationReply{Messages: []messageRef{{Ref: "#/channels/c/messages/m"}}},
				Bindings: &operationBindings{HTTP: &httpOperationBinding{Method: "POST"}}},
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

// TestInputArbitraryValueForNonJSONFamilyRefusedPreDispatch verifies §9.1's
// value boundary: binary/codec-specific media have no revision-1 bytes
// carriage and are refused before dispatch, regardless of the caller value.
func TestInputArbitraryValueForNonJSONFamilyRefusedPreDispatch(t *testing.T) {
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
		t.Fatalf("expected ERR_SOURCE_CONFIG_ERROR for a media family outside revision 1, got %v", err)
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
// output decodes by the reply-side declaration, and the response media type
// must agree with that artifact declaration.
func TestDecodeTextLaneAndReplyDirection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/c" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, `{"looks":"like json"}`)
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
