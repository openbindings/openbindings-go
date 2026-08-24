package invoke

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

func intp(v int) *int { return &v }

func testSite() InvokeSite {
	return InvokeSite{
		Operation:   "com.example.op",
		InvokedAs:   "op",
		BindingSpec: "usage@2.13.1",
		Selector:    "check",
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

// A panicking hook is contained as ErrCodeRuntime, never a process kill
// (hooks run in format-invoker goroutines outside core's Invoke recover).
func TestInvokeHooks_PanicContainment(t *testing.T) {
	h := newInvokeHooks(hookSlots{
		classify: func(InvokeSite, RawResult) (bool, error) { panic("nil map read") },
	}, hookSlots{})
	_, err := h.Classify(testSite(), RawResult{Status: intp(0)}, nil)
	var ie *InvocationError
	if !errors.As(err, &ie) || ie.Code != ErrCodeRuntime {
		t.Fatalf("panic must contain as ErrCodeRuntime, got %v", err)
	}
	if ie.Error() != ErrCodeRuntime {
		t.Fatalf("abstract error text must be the code, got %q", ie.Error())
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
// deliberate terminal, with its code and data preserved exactly.
func TestInvokeHooks_InvocationErrorPassthrough(t *testing.T) {
	h := newInvokeHooks(hookSlots{
		decode: func(InvokeSite, RawResult) (any, error) {
			return nil, &InvocationError{Code: ErrCodeStreamError}
		},
	}, hookSlots{})
	_, err := h.DecodeOutput(testSite(), RawResult{}, nil)
	var ie *InvocationError
	if !errors.As(err, &ie) || ie.Code != ErrCodeStreamError {
		t.Fatalf("InvocationError must pass through with its code, got %v", err)
	}
	if ie.HasData() {
		t.Fatalf("passthrough invented abstract data: %#v", ie.Data)
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

// SnapshotHooks composes per-invocation axes over the invoker level: a
// per-invocation decoder wins, and a nil per-invocation axis declines to
// the invoker-level one (the direct-binding-caller affordance).
func TestSnapshotHooks_ComposesTiers(t *testing.T) {
	inv := NewOperationInvoker()
	inv.OutputDecoder = func(InvokeSite, RawResult) (any, error) { return "invoker", nil }
	inv.ResultClassifier = func(InvokeSite, RawResult) (bool, error) { return true, nil }

	// Per-invocation decode overrides; classify axis (nil) declines to invoker.
	h := inv.SnapshotHooks(
		func(InvokeSite, RawResult) (any, error) { return "per-inv", nil },
		nil, nil,
	)
	if v, _ := h.DecodeOutput(testSite(), RawResult{}, nil); v != "per-inv" {
		t.Fatalf("per-invocation decode must win, got %v", v)
	}
	if h.DecodeDecidedBy() != "hook" {
		t.Fatalf("decode decidedBy = %q, want hook", h.DecodeDecidedBy())
	}
	ok, _ := h.Classify(testSite(), RawResult{Status: intp(0)}, nil)
	if !ok {
		t.Fatal("nil per-invocation classify must decline to the invoker-level classifier")
	}
	if h.ClassifyDecidedBy() != "hook" {
		t.Fatalf("classify decidedBy = %q, want hook", h.ClassifyDecidedBy())
	}
}

// The §4.5.3 warning fires only when an ASSUMPTION lane decoded (the
// format's x-ob-decode stamp) AND the contract cannot catch a wrong lane;
// a hook decode, a wire-framed lane, an absent stamp (no decode ran), or a
// discriminating schema silences it.
func TestAssumptionWarning(t *testing.T) {
	floor := map[string]any{"type": "string", "x-ob": map[string]any{"floor": "text"}}
	if w := AssumptionWarning("assumption/text", floor); w == "" || !strings.Contains(w, "floor-stamped") {
		t.Fatalf("floor-stamped + assumption decode must warn, got %q", w)
	}
	if w := AssumptionWarning("assumption/text", nil); !strings.Contains(w, "no output schema") {
		t.Fatalf("no schema + assumption decode must warn, got %q", w)
	}
	typed := map[string]any{"type": "object", "required": []any{"id"}}
	if w := AssumptionWarning("assumption/text", typed); w != "" {
		t.Fatalf("a discriminating schema must not warn, got %q", w)
	}
	if w := AssumptionWarning("hook", floor); w != "" {
		t.Fatalf("a hook decode must silence the warning, got %q", w)
	}
	if w := AssumptionWarning("header/content-type", nil); w != "" {
		t.Fatalf("a wire-framed lane is not an assumption, got %q", w)
	}
	if w := AssumptionWarning("", nil); w != "" {
		t.Fatalf("no stamp means no decode ran — must not warn, got %q", w)
	}
}

// TestInvokeHooks_DecidedByCrossGoroutineRace pins the structural safety of
// the decode/classify provenance pair. operation_invoker consults
// DecodeDecidedBy/ClassifyDecidedBy from the RUN goroutine while the BINDING
// goroutine is still running the hook chain that writes them; today the two
// paths only avoid a data race by an incidental InvocationImpl.mu edge on the
// emit path — a format that decodes off the emit path (pipelined SSE unit
// N+1) would race on the plain fields. This test drives the write and the
// read from two goroutines with NO shared mutex; under `-race` the plain-string
// fields fail here and the atomically-published pair passes.
func TestInvokeHooks_DecidedByCrossGoroutineRace(t *testing.T) {
	h := newInvokeHooks(
		hookSlots{
			decode:   func(InvokeSite, RawResult) (any, error) { return "v", nil },
			classify: func(InvokeSite, RawResult) (bool, error) { return true, nil },
		},
		hookSlots{},
	)
	site := testSite()

	const iters = 3000
	var wg sync.WaitGroup
	wg.Add(1)
	stop := make(chan struct{})
	// Binding goroutine: consult decode + classify, writing the provenance.
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < iters; i++ {
			_, _ = h.DecodeOutput(site, RawResult{}, nil)
			_, _ = h.Classify(site, RawResult{Status: intp(0)}, nil)
		}
	}()
	// Run goroutine: read the provenance concurrently, unsynchronized, for the
	// full lifetime of the writer.
	for {
		select {
		case <-stop:
			wg.Wait()
			if h.DecodeDecidedBy() != "hook" || h.ClassifyDecidedBy() != "hook" {
				t.Fatalf("decidedBy = (%q,%q), want (hook,hook)", h.DecodeDecidedBy(), h.ClassifyDecidedBy())
			}
			return
		default:
			_ = h.DecodeDecidedBy()
			_ = h.ClassifyDecidedBy()
		}
	}
}
