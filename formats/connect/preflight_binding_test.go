package connect

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

// untouchableServer is an HTTP server that records any contact. This
// adapter's preflight uses no network, so any request reaching it is a
// failure.
func untouchableServer(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, &hit
}

// PreflightBinding reports exactly the challenge the live invocation raises
// for the same arguments: an apiKey with no consumer-named header is
// inexpressible under §9.6 and is surfaced as one auth.apiKey requirement
// against the resolved target.
func TestPreflightBinding_MatchesLiveChallenge(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content any
	}{
		{name: "schema mode", content: testProto},
		{name: "descriptorless mode", content: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testContext(t)
			srv, hit := untouchableServer(t)
			args := unaryArgs(srv.URL, tc.content, "testpkg.TestService/GetItem")
			args.Context = map[string]any{"apiKey": "k-secret"}

			preflight, err := NewInvoker().PreflightBinding(ctx, args)
			if err != nil {
				t.Fatalf("PreflightBinding: %v", err)
			}
			if !invoke.ValidContextRequiredDetails(preflight) {
				t.Fatalf("PreflightBinding must report a valid challenge, got %#v", preflight)
			}
			if preflight.Target != srv.URL {
				t.Errorf("Target = %q, want the resolved target %q", preflight.Target, srv.URL)
			}

			inv := NewInvoker().InvokeBinding(ctx, args)
			live := invoke.ContextRequiredFrom(mustTerminalError(t, ctx, inv, invoke.ErrCodeContextRequired))
			if !reflect.DeepEqual(preflight, live) {
				t.Errorf("preflight and live challenge differ:\n preflight %#v\n live      %#v", preflight, live)
			}
			if hit.Load() {
				t.Error("server was contacted; neither preflight nor the pre-dispatch challenge may dispatch")
			}
		})
	}
}

// Supplying context that the binding can place narrows the result to
// nothing: PreflightBinding returns nil exactly where the invocation would
// proceed past context application.
func TestPreflightBinding_SatisfiedContextIsNil(t *testing.T) {
	ctx := testContext(t)
	srv, hit := untouchableServer(t)
	for name, bindCtx := range map[string]map[string]any{
		"empty":                 {},
		"bearer":                {"bearerToken": "tok-1"},
		"apiKey named a header": {"headers": map[string]any{"X-API-Key": "k-9"}},
	} {
		t.Run(name, func(t *testing.T) {
			args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
			args.Context = bindCtx
			details, err := NewInvoker().PreflightBinding(ctx, args)
			if err != nil {
				t.Fatalf("PreflightBinding: %v", err)
			}
			if details != nil {
				t.Errorf("PreflightBinding = %#v, want nil for placeable context", details)
			}
		})
	}
	if hit.Load() {
		t.Error("server was contacted during preflight")
	}
}

// The challenge names the target the invocation would dispatch to: a
// configured target (§9.1) replaces the source location in the challenge
// exactly as it does on the wire.
func TestPreflightBinding_ConfiguredTargetIsChallengeTarget(t *testing.T) {
	ctx := testContext(t)
	srv, hit := untouchableServer(t)
	args := unaryArgs("http://never.invalid", testProto, "testpkg.TestService/GetItem")
	args.Context = map[string]any{
		"apiKey":        "k-secret",
		"configuration": map[string]any{"target": srv.URL},
	}
	preflight, err := NewInvoker().PreflightBinding(ctx, args)
	if err != nil {
		t.Fatalf("PreflightBinding: %v", err)
	}
	if preflight == nil || preflight.Target != srv.URL {
		t.Fatalf("Target = %#v, want configured target %q", preflight, srv.URL)
	}
	inv := NewInvoker().InvokeBinding(ctx, args)
	live := invoke.ContextRequiredFrom(mustTerminalError(t, ctx, inv, invoke.ErrCodeContextRequired))
	if !reflect.DeepEqual(preflight, live) {
		t.Errorf("preflight and live challenge differ:\n preflight %#v\n live      %#v", preflight, live)
	}
	if hit.Load() {
		t.Error("server was contacted")
	}
}

// Gates the invocation would fail with a different error are not context
// requirements: preflight reports nothing and leaves the refusal to the
// authoritative invocation.
func TestPreflightBinding_NonContextRefusalsReportNothing(t *testing.T) {
	ctx := testContext(t)
	srv, hit := untouchableServer(t)
	for name, args := range map[string]*invoke.BindingInvocationArgs{
		"invalid selector":      unaryArgs(srv.URL, testProto, "no-slash"),
		"unknown method":        unaryArgs(srv.URL, testProto, "testpkg.TestService/Missing"),
		"malformed target":      unaryArgs("ftp://x", testProto, "testpkg.TestService/GetItem"),
		"unparseable content":   unaryArgs(srv.URL, "syntax = \"proto3\"; service {", "testpkg.TestService/GetItem"),
		"no location or target": unaryArgs("", testProto, "testpkg.TestService/GetItem"),
	} {
		t.Run(name, func(t *testing.T) {
			args.Context = map[string]any{"apiKey": "k-secret"}
			details, err := NewInvoker().PreflightBinding(ctx, args)
			if err != nil || details != nil {
				t.Errorf("PreflightBinding = (%#v, %v), want (nil, nil)", details, err)
			}
		})
	}
	if hit.Load() {
		t.Error("server was contacted during preflight")
	}
}
