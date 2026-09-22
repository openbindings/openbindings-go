package mcp

import (
	"errors"
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

func liveChallenge(t *testing.T, invoker *Invoker, args *invoke.BindingInvocationArgs) *invoke.ContextRequiredDetails {
	t.Helper()
	_, err := drainOutputs(t, invoker.InvokeBinding(bg(), args))
	var ierr *invoke.InvocationError
	if !errors.As(err, &ierr) || ierr.Code != invoke.ErrCodeContextRequired {
		t.Fatalf("live terminal = %v, want %s", err, invoke.ErrCodeContextRequired)
	}
	return invoke.ContextRequiredFrom(ierr)
}

// PreflightBinding reports exactly the challenge the live invocation raises
// for the same arguments: an apiKey or basic credential with no named
// header destination is surfaced under §9.4 / MCP-P-07 as one auth.apiKey
// requirement against the endpoint, before the handshake.
func TestPreflightBinding_MatchesLiveChallenge(t *testing.T) {
	for name, bindCtx := range map[string]map[string]any{
		"apiKey": {"apiKey": "k-secret"},
		"basic":  {"basic": map[string]any{"username": "u", "password": "p-secret"}},
	} {
		t.Run(name, func(t *testing.T) {
			srv, hit := untouchableServer(t)
			invoker := NewInvoker()
			args := invocationArgs(srv.URL, "tools/echo", bindCtx)

			preflight, err := invoker.PreflightBinding(bg(), args)
			if err != nil {
				t.Fatalf("PreflightBinding: %v", err)
			}
			if !invoke.ValidContextRequiredDetails(preflight) {
				t.Fatalf("PreflightBinding must report a valid challenge, got %#v", preflight)
			}
			if preflight.Target != srv.URL {
				t.Errorf("Target = %q, want the endpoint %q", preflight.Target, srv.URL)
			}

			live := liveChallenge(t, invoker, args)
			if !reflect.DeepEqual(preflight, live) {
				t.Errorf("preflight and live challenge differ:\n preflight %#v\n live      %#v", preflight, live)
			}
			if hit.Load() {
				t.Error("server was contacted; neither preflight nor the pre-handshake challenge may connect")
			}
		})
	}
}

// Supplying context the binding can place narrows the result to nothing:
// PreflightBinding returns nil exactly where the invocation would proceed to
// the handshake.
func TestPreflightBinding_SatisfiedContextIsNil(t *testing.T) {
	srv, hit := untouchableServer(t)
	for name, bindCtx := range map[string]map[string]any{
		"empty":                  {},
		"bearer":                 {"bearerToken": "tok-1"},
		"apiKey named a header":  {"headers": map[string]any{"X-API-Key": "k-9"}},
		"resources take no auth": nil,
	} {
		t.Run(name, func(t *testing.T) {
			details, err := NewInvoker().PreflightBinding(bg(), invocationArgs(srv.URL, "tools/echo", bindCtx))
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

// Gates the invocation would fail with a different error are not context
// requirements: preflight reports nothing and leaves the refusal to the
// authoritative invocation.
func TestPreflightBinding_NonContextRefusalsReportNothing(t *testing.T) {
	srv, hit := untouchableServer(t)
	credential := map[string]any{"apiKey": "k-secret"}
	foreign := invocationArgs(srv.URL, "tools/echo", credential)
	foreign.Source.BindingSpec = "openbindings.other@1"
	for name, args := range map[string]*invoke.BindingInvocationArgs{
		"foreign binding spec": foreign,
		"invalid selector":     invocationArgs(srv.URL, "echo", credential),
		"non-HTTP endpoint":    invocationArgs("ftp://x", "tools/echo", credential),
		"no endpoint":          invocationArgs("", "tools/echo", credential),
	} {
		t.Run(name, func(t *testing.T) {
			details, err := NewInvoker().PreflightBinding(bg(), args)
			if err != nil || details != nil {
				t.Errorf("PreflightBinding = (%#v, %v), want (nil, nil)", details, err)
			}
		})
	}
	if hit.Load() {
		t.Error("server was contacted during preflight")
	}
}
