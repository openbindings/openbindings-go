package openbindings

import (
	"errors"
	"strings"
	"testing"
)

func intp(v int) *int { return &v }

func testSite() InvokeSite {
	return InvokeSite{
		Operation: "com.example.op",
		InvokedAs: "op",
		Format:    "usage@2.13.1",
		Ref:       "check",
	}
}

// The decline chain: per-invocation → invoker-level → builtin, uniformly.
func TestInvokeHooks_DeclineChain(t *testing.T) {
	site := testSite()
	raw := RawResult{Status: intp(1), Body: []byte("out")}

	builtin := func(InvokeSite, RawResult) (any, error) { return "builtin", nil }

	// Per-invocation answers: wins outright.
	h := newInvokeHooks(
		hookSlots{decode: func(InvokeSite, RawResult) (any, error) { return "per-inv", nil }},
		hookSlots{decode: func(InvokeSite, RawResult) (any, error) { return "invoker", nil }},
	)
	if v, err := h.DecodeOutput(site, raw, builtin); err != nil || v != "per-inv" {
		t.Fatalf("per-invocation should win: %v %v", v, err)
	}

	// Per-invocation declines: falls to invoker-level, NOT straight to builtin
	// (the round-3 winner-takes-slot hazard).
	h = newInvokeHooks(
		hookSlots{decode: func(InvokeSite, RawResult) (any, error) { return nil, ErrUseDefault }},
		hookSlots{decode: func(InvokeSite, RawResult) (any, error) { return "invoker", nil }},
	)
	if v, _ := h.DecodeOutput(site, raw, builtin); v != "invoker" {
		t.Fatalf("decline must chain to invoker level, got %v", v)
	}

	// Both decline: builtin.
	h = newInvokeHooks(
		hookSlots{decode: func(InvokeSite, RawResult) (any, error) { return nil, ErrUseDefault }},
		hookSlots{decode: func(InvokeSite, RawResult) (any, error) { return nil, ErrUseDefault }},
	)
	if v, _ := h.DecodeOutput(site, raw, builtin); v != "builtin" {
		t.Fatalf("all-decline must reach the builtin, got %v", v)
	}

	// Nil carrier: builtins only (the seam is nil-safe).
	var nilHooks *InvokeHooks
	if v, err := nilHooks.DecodeOutput(site, raw, builtin); err != nil || v != "builtin" {
		t.Fatalf("nil carrier must run builtin: %v %v", v, err)
	}
}

// A panicking hook is contained as ErrCodeRuntime with tier provenance —
// never a process kill (hooks run in format-invoker goroutines outside
// core's Invoke recover).
func TestInvokeHooks_PanicContainment(t *testing.T) {
	h := newInvokeHooks(hookSlots{
		classify: func(InvokeSite, RawResult) (bool, error) { panic("nil map read") },
	}, hookSlots{})
	_, err := h.Classify(testSite(), RawResult{Status: intp(0)}, nil)
	var ie *InvocationError
	if !errors.As(err, &ie) || ie.Code != ErrCodeRuntime {
		t.Fatalf("panic must contain as ErrCodeRuntime, got %v", err)
	}
	if !strings.Contains(ie.Message, "per-invocation hook") {
		t.Fatalf("containment must name the tier: %v", ie.Message)
	}

	// Router panics are contained too (the seam's error channel exists for
	// exactly this).
	h = newInvokeHooks(hookSlots{
		route: func(InvokeSite, string, any) string { panic("boom") },
	}, hookSlots{})
	if _, err := h.RouteField(testSite(), "field", nil); err == nil {
		t.Fatal("router panic must surface as an error")
	}
}

// Failure channel (ii): a returned plain error is a DELIBERATE terminal
// wrapped with the axis's native code, never ErrCodeRuntime.
func TestInvokeHooks_ReturnedPlainError(t *testing.T) {
	h := newInvokeHooks(hookSlots{
		decode: func(InvokeSite, RawResult) (any, error) { return nil, errors.New("not orderml") },
	}, hookSlots{})
	_, err := h.DecodeOutput(testSite(), RawResult{}, nil)
	var ie *InvocationError
	if !errors.As(err, &ie) || ie.Code != ErrCodeResponseError {
		t.Fatalf("plain decode error must wrap as ErrCodeResponseError, got %v", err)
	}

	h = newInvokeHooks(hookSlots{
		classify: func(InvokeSite, RawResult) (bool, error) { return false, errors.New("bad state") },
	}, hookSlots{})
	_, err = h.Classify(testSite(), RawResult{}, nil)
	if !errors.As(err, &ie) || ie.Code != ErrCodeExecutionFailed {
		t.Fatalf("plain classify error must wrap as ErrCodeExecutionFailed, got %v", err)
	}
}

// Failure channel (i): a returned *InvocationError passes through as the
// deliberate terminal, code preserved, tier provenance added.
func TestInvokeHooks_InvocationErrorPassthrough(t *testing.T) {
	h := newInvokeHooks(hookSlots{
		decode: func(InvokeSite, RawResult) (any, error) {
			return nil, &InvocationError{Code: ErrCodeStreamError, Message: "frame error"}
		},
	}, hookSlots{})
	_, err := h.DecodeOutput(testSite(), RawResult{}, nil)
	var ie *InvocationError
	if !errors.As(err, &ie) || ie.Code != ErrCodeStreamError {
		t.Fatalf("InvocationError must pass through with its code, got %v", err)
	}
	if m, ok := ie.Details.(map[string]any); !ok || m["decidedBy"] != "per-invocation hook" {
		t.Fatalf("passthrough must carry tier provenance, got %#v", ie.Details)
	}
}

// Builtin dispatch: loud on an unstamped site and on an absent axis —
// never a silent no-op.
func TestBuiltinDispatch(t *testing.T) {
	// Unstamped site (never passed through the seam).
	if _, err := BuiltinDecode(testSite(), RawResult{}); err == nil {
		t.Fatal("BuiltinDecode on an unstamped site must be loud")
	}

	// Stamped site with an absent axis (e.g. grpc): loud ErrCodeRuntime.
	site := testSite()
	site.seamStamped = true // stamped, but no builtins (matrix row: none)
	_, err := BuiltinClassify(site, RawResult{})
	var ie *InvocationError
	if !errors.As(err, &ie) || ie.Code != ErrCodeRuntime {
		t.Fatalf("absent-axis builtin must be loud ErrCodeRuntime, got %v", err)
	}

	// Stamped site with a real builtin: dispatches.
	site.builtinDecode = func(_ InvokeSite, raw RawResult) (any, error) { return string(raw.Body), nil }
	if v, err := BuiltinDecode(site, RawResult{Body: []byte("ok")}); err != nil || v != "ok" {
		t.Fatalf("stamped builtin must dispatch: %v %v", v, err)
	}
}

// The route chain declines with "" and falls through to "" (format default).
func TestInvokeHooks_RouteChain(t *testing.T) {
	h := newInvokeHooks(
		hookSlots{route: func(_ InvokeSite, field string, _ any) string {
			if field == "locator" {
				return "stdin-dash"
			}
			return "" // decline
		}},
		hookSlots{route: func(_ InvokeSite, field string, _ any) string {
			if field == "config" {
				return "file"
			}
			return ""
		}},
	)
	if r, _ := h.RouteField(testSite(), "locator", nil); r != "stdin-dash" {
		t.Fatalf("per-invocation route must win: %q", r)
	}
	if r, _ := h.RouteField(testSite(), "config", nil); r != "file" {
		t.Fatalf("decline must chain to invoker level: %q", r)
	}
	if r, _ := h.RouteField(testSite(), "other", nil); r != "" {
		t.Fatalf("all-decline must return the format default: %q", r)
	}
}

// WithRuntime is a struct copy: hook fields ride automatically (the
// field-enumeration copy silently dropped new fields — the round-2 panel
// hazard).
func TestWithRuntime_CarriesHookFields(t *testing.T) {
	inv := NewOperationInvoker()
	inv.ResultClassifier = func(InvokeSite, RawResult) (bool, error) { return true, nil }
	cp := inv.WithRuntime(nil)
	if cp.ResultClassifier == nil {
		t.Fatal("WithRuntime must carry hook fields (struct copy, not field enumeration)")
	}
}

// The per-Invoke snapshot is immune to later field mutation; consultation
// still decline-chains across both captured tiers.
func TestSnapshotImmunity(t *testing.T) {
	inv := NewOperationInvoker()
	inv.OutputDecoder = func(InvokeSite, RawResult) (any, error) { return "captured", nil }
	h := inv.snapshotHooks(hookSlots{})
	inv.OutputDecoder = func(InvokeSite, RawResult) (any, error) { return "mutated", nil }
	if v, _ := h.DecodeOutput(testSite(), RawResult{}, nil); v != "captured" {
		t.Fatalf("snapshot must be immune to later mutation, got %v", v)
	}
}
