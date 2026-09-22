package grpc

import (
	"context"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

const preflightBindingProto = `syntax = "proto3";
package tiny;
service Tiny { rpc Ping(PingMsg) returns (PingMsg); }
message PingMsg { string msg = 1; }
`

// untouchableListener is a TCP listener that records any connection. This
// adapter's preflight uses no network, so any dial reaching it is a
// failure. It returns the explicit plaintext (§4) target form, so the
// transport determination needs no configuration.
func untouchableListener(t *testing.T) (string, *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var hit atomic.Bool
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			hit.Store(true)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return "grpc://" + ln.Addr().String(), &hit
}

func preflightBindingArgs(target string, content bool, selector string, bindCtx map[string]any) *invoke.BindingInvocationArgs {
	source := invoke.InvocationSource{BindingSpec: BindingSpec, Location: target}
	if content {
		source.Content = openbindings.TextContent(preflightBindingProto)
	}
	return &invoke.BindingInvocationArgs{Source: source, Selector: selector, Context: bindCtx}
}

func preflightBindingCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// PreflightBinding reports exactly the challenge the live invocation raises
// for the same arguments: a generic credential that names no metadata
// carriage is surfaced under §9.5 / GRPC-P-07 as one auth.apiKey
// requirement against the resolved target, for every generic family and in
// both source modes.
func TestPreflightBinding_MatchesLiveChallenge(t *testing.T) {
	credentials := map[string]map[string]any{
		"apiKey": {"apiKey": "k-secret"},
		"bearer": {"bearerToken": "tok-secret"},
		"basic":  {"basic": map[string]any{"username": "u", "password": "p-secret"}},
	}
	for _, mode := range []struct {
		name    string
		content bool
	}{
		{name: "embedded content", content: true},
		{name: "location only", content: false},
	} {
		for family, bindCtx := range credentials {
			t.Run(mode.name+"/"+family, func(t *testing.T) {
				ctx := preflightBindingCtx(t)
				target, hit := untouchableListener(t)
				args := preflightBindingArgs(target, mode.content, "tiny.Tiny/Ping", bindCtx)
				invoker := NewInvoker()
				t.Cleanup(func() { _ = invoker.Close() })

				preflight, err := invoker.PreflightBinding(ctx, args)
				if err != nil {
					t.Fatalf("PreflightBinding: %v", err)
				}
				if !invoke.ValidContextRequiredDetails(preflight) {
					t.Fatalf("PreflightBinding must report a valid challenge, got %#v", preflight)
				}
				if preflight.Target != target {
					t.Errorf("Target = %q, want the resolved target %q", preflight.Target, target)
				}

				inv := invoker.InvokeBinding(ctx, args)
				ierr := readToTerminal(ctx, inv)
				if ierr == nil || ierr.Code != invoke.ErrCodeContextRequired {
					t.Fatalf("live terminal = %v, want %s", ierr, invoke.ErrCodeContextRequired)
				}
				live := invoke.ContextRequiredFrom(ierr)
				if !reflect.DeepEqual(preflight, live) {
					t.Errorf("preflight and live challenge differ:\n preflight %#v\n live      %#v", preflight, live)
				}
				if hit.Load() {
					t.Error("server was dialed; neither preflight nor the pre-dispatch challenge may dial")
				}
			})
		}
	}
}

// Supplying context the binding can place narrows the result to nothing:
// PreflightBinding returns nil exactly where the invocation would proceed to
// dial.
func TestPreflightBinding_SatisfiedContextIsNil(t *testing.T) {
	ctx := preflightBindingCtx(t)
	target, hit := untouchableListener(t)
	for name, bindCtx := range map[string]map[string]any{
		"empty":                    {},
		"credential named a field": {"headers": map[string]any{"authorization": "Bearer tok-1"}},
		"configured transport":     {"configuration": map[string]any{"transport": "plaintext"}},
	} {
		t.Run(name, func(t *testing.T) {
			invoker := NewInvoker()
			t.Cleanup(func() { _ = invoker.Close() })
			details, err := invoker.PreflightBinding(ctx, preflightBindingArgs(target, true, "tiny.Tiny/Ping", bindCtx))
			if err != nil {
				t.Fatalf("PreflightBinding: %v", err)
			}
			if details != nil {
				t.Errorf("PreflightBinding = %#v, want nil for placeable context", details)
			}
		})
	}
	if hit.Load() {
		t.Error("server was dialed during preflight")
	}
}

// The challenge names the target the invocation would dial: a configured
// target (§9.3) replaces the source location in the challenge exactly as it
// does for the dial.
func TestPreflightBinding_ConfiguredTargetIsChallengeTarget(t *testing.T) {
	ctx := preflightBindingCtx(t)
	target, hit := untouchableListener(t)
	args := preflightBindingArgs("grpc://never.invalid:1", true, "tiny.Tiny/Ping", map[string]any{
		"apiKey":        "k-secret",
		"configuration": map[string]any{"target": target},
	})
	invoker := NewInvoker()
	t.Cleanup(func() { _ = invoker.Close() })
	preflight, err := invoker.PreflightBinding(ctx, args)
	if err != nil {
		t.Fatalf("PreflightBinding: %v", err)
	}
	if preflight == nil || preflight.Target != target {
		t.Fatalf("Target = %#v, want configured target %q", preflight, target)
	}
	live := invoke.ContextRequiredFrom(readToTerminal(ctx, invoker.InvokeBinding(ctx, args)))
	if !reflect.DeepEqual(preflight, live) {
		t.Errorf("preflight and live challenge differ:\n preflight %#v\n live      %#v", preflight, live)
	}
	if hit.Load() {
		t.Error("server was dialed")
	}
}

// Gates the invocation would fail with a different error are not context
// requirements: preflight reports nothing and leaves the refusal to the
// authoritative invocation.
func TestPreflightBinding_NonContextRefusalsReportNothing(t *testing.T) {
	ctx := preflightBindingCtx(t)
	target, hit := untouchableListener(t)
	bare := target[len("grpc://"):]
	credential := map[string]any{"apiKey": "k-secret"}
	for name, args := range map[string]*invoke.BindingInvocationArgs{
		"invalid selector":       preflightBindingArgs(target, true, "no-slash", credential),
		"undefined scheme":       preflightBindingArgs("ftp://"+bare, true, "tiny.Tiny/Ping", credential),
		"undetermined transport": preflightBindingArgs(bare, true, "tiny.Tiny/Ping", credential),
		"no location or target":  preflightBindingArgs("", true, "tiny.Tiny/Ping", credential),
	} {
		t.Run(name, func(t *testing.T) {
			invoker := NewInvoker()
			t.Cleanup(func() { _ = invoker.Close() })
			details, err := invoker.PreflightBinding(ctx, args)
			if err != nil || details != nil {
				t.Errorf("PreflightBinding = (%#v, %v), want (nil, nil)", details, err)
			}
		})
	}
	if hit.Load() {
		t.Error("server was dialed during preflight")
	}
}
