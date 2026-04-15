package openbindings

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock executor for BEC tests
// ---------------------------------------------------------------------------

type mockExecutor struct {
	formats []FormatInfo

	executeFn func(ctx context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error)
}

func (m *mockExecutor) Formats() []FormatInfo { return m.formats }
func (m *mockExecutor) ExecuteBinding(ctx context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
	if m.executeFn != nil {
		return m.executeFn(ctx, in)
	}
	return SingleEventChannel(&ExecuteOutput{Output: "ok"}), nil
}

var _ BindingExecutor = (*mockExecutor)(nil)

// ---------------------------------------------------------------------------
// MemoryStore tests
// ---------------------------------------------------------------------------

func TestMemoryStore_GetSetDelete(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	v, err := store.Get(ctx, "missing")
	if err != nil || v != nil {
		t.Fatalf("expected nil, nil for missing key; got %v, %v", v, err)
	}

	if err := store.Set(ctx, "k1", map[string]any{"bearerToken": "abc"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get(ctx, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got["bearerToken"] != "abc" {
		t.Fatalf("expected abc, got %v", got["bearerToken"])
	}

	if err := store.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err = store.Get(ctx, "k1")
	if err != nil || got != nil {
		t.Fatalf("expected nil after delete; got %v, %v", got, err)
	}
}

func TestMemoryStore_SetNilDeletes(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	_ = store.Set(ctx, "k", map[string]any{"x": 1})
	_ = store.Set(ctx, "k", nil)
	v, _ := store.Get(ctx, "k")
	if v != nil {
		t.Fatalf("expected nil after Set(nil), got %v", v)
	}
}

func TestMemoryStore_DeepCopy(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	original := map[string]any{
		"basic": map[string]any{
			"username": "alice",
			"password": "secret",
		},
	}
	if err := store.Set(ctx, "k", original); err != nil {
		t.Fatal(err)
	}

	original["basic"].(map[string]any)["password"] = "MUTATED"

	got, _ := store.Get(ctx, "k")
	basic := got["basic"].(map[string]any)
	if basic["password"] != "secret" {
		t.Fatalf("store was mutated through original reference: %v", basic["password"])
	}

	got["basic"].(map[string]any)["username"] = "MUTATED"
	got2, _ := store.Get(ctx, "k")
	basic2 := got2["basic"].(map[string]any)
	if basic2["username"] != "alice" {
		t.Fatalf("store was mutated through Get return: %v", basic2["username"])
	}
}

func TestMemoryStore_Concurrent(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "key"
			_ = store.Set(ctx, key, map[string]any{"i": i})
			_, _ = store.Get(ctx, key)
			_ = store.Delete(ctx, key)
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// NormalizeContextKey tests
// ---------------------------------------------------------------------------

func TestNormalizeContextKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://api.example.com/v1/users", "api.example.com"},
		{"http://api.example.com/v1", "api.example.com"},
		{"https://api.example.com", "api.example.com"},
		{"ws://api.example.com:8080/stream", "api.example.com:8080"},
		{"wss://api.example.com", "api.example.com"},
		{"grpc://localhost:50051/svc", "localhost:50051"},
		{"localhost:50051", "localhost:50051"},
		{"", ""},
		{"  https://api.example.com/path  ", "api.example.com"},
	}
	for _, tt := range tests {
		got := NormalizeContextKey(tt.input)
		if got != tt.want {
			t.Errorf("NormalizeContextKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// OperationExecutor BEC tests
// ---------------------------------------------------------------------------

func TestExecuteBinding_PropagatesStoreAndCallbacks(t *testing.T) {
	var capturedStore ContextStore
	var capturedCallbacks *PlatformCallbacks

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			capturedStore = in.Store
			capturedCallbacks = in.Callbacks
			return SingleEventChannel(&ExecuteOutput{Output: "ok"}), nil
		},
	}

	store := NewMemoryStore()
	callbacks := &PlatformCallbacks{}

	exec := NewOperationExecutor(executor)
	exec.ContextStore = store
	exec.PlatformCallbacks = callbacks

	_, err := exec.ExecuteBinding(context.Background(), &BindingExecutionInput{
		Source: BindingExecutionSource{Format: "test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedStore != store {
		t.Error("Store was not propagated to executor")
	}
	if capturedCallbacks != callbacks {
		t.Error("Callbacks were not propagated to executor")
	}
}

func TestExecuteBinding_DoesNotOverrideExistingStoreCallbacks(t *testing.T) {
	existingStore := NewMemoryStore()
	existingCb := &PlatformCallbacks{}

	var capturedStore ContextStore
	var capturedCallbacks *PlatformCallbacks

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			capturedStore = in.Store
			capturedCallbacks = in.Callbacks
			return SingleEventChannel(&ExecuteOutput{Output: "ok"}), nil
		},
	}

	exec := NewOperationExecutor(executor)
	exec.ContextStore = NewMemoryStore()
	exec.PlatformCallbacks = &PlatformCallbacks{}

	_, err := exec.ExecuteBinding(context.Background(), &BindingExecutionInput{
		Source:    BindingExecutionSource{Format: "test"},
		Store:     existingStore,
		Callbacks: existingCb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedStore != existingStore {
		t.Error("input's existing Store was overridden")
	}
	if capturedCallbacks != existingCb {
		t.Error("input's existing Callbacks were overridden")
	}
}

func TestExecuteBinding_ContextPassesThrough(t *testing.T) {
	var capturedCtx map[string]any
	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			capturedCtx = in.Context
			return SingleEventChannel(&ExecuteOutput{Output: "ok"}), nil
		},
	}

	exec := NewOperationExecutor(executor)

	_, err := exec.ExecuteBinding(context.Background(), &BindingExecutionInput{
		Source:  BindingExecutionSource{Format: "test"},
		Context: map[string]any{"custom": "value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedCtx["custom"] != "value" {
		t.Error("original context should pass through")
	}
}

// ---------------------------------------------------------------------------
// WithRuntime tests
// ---------------------------------------------------------------------------

func TestWithRuntime_ClonesWithOverrides(t *testing.T) {
	executor := &mockExecutor{formats: []FormatInfo{{Token: "test"}}}
	orig := NewOperationExecutor(executor)
	origStore := NewMemoryStore()
	orig.ContextStore = origStore

	newStore := NewMemoryStore()
	newCb := &PlatformCallbacks{}
	clone := orig.WithRuntime(newStore, newCb)

	if clone.ContextStore != newStore {
		t.Error("clone should use new store")
	}
	if clone.PlatformCallbacks != newCb {
		t.Error("clone should use new callbacks")
	}
	if orig.PlatformCallbacks != nil {
		t.Error("original should be unmodified")
	}

	fmts := clone.Formats()
	if len(fmts) != 1 || fmts[0].Token != "test" {
		t.Error("clone should share executor registrations")
	}
}

func TestWithRuntime_NilInheritsOriginal(t *testing.T) {
	executor := &mockExecutor{formats: []FormatInfo{{Token: "test"}}}
	orig := NewOperationExecutor(executor)
	origStore := NewMemoryStore()
	origCb := &PlatformCallbacks{}
	orig.ContextStore = origStore
	orig.PlatformCallbacks = origCb

	clone := orig.WithRuntime(nil, nil)
	if clone.ContextStore != origStore {
		t.Error("nil store should inherit original")
	}
	if clone.PlatformCallbacks != origCb {
		t.Error("nil callbacks should inherit original")
	}
}

// ---------------------------------------------------------------------------
// Formats defensive copy test
// ---------------------------------------------------------------------------

func TestFormats_ReturnsDefensiveCopy(t *testing.T) {
	executor := &mockExecutor{formats: []FormatInfo{{Token: "test@1.0"}}}
	exec := NewOperationExecutor(executor)

	fmts := exec.Formats()
	fmts[0] = FormatInfo{Token: "MUTATED"}

	original := exec.Formats()
	if original[0].Token != "test@1.0" {
		t.Errorf("Formats() did not return a copy; internal slice was mutated to %q", original[0].Token)
	}
}

// ---------------------------------------------------------------------------
// ExecuteError tests
// ---------------------------------------------------------------------------

func TestExecuteError_FallsBackToCode(t *testing.T) {
	e := &ExecuteError{Code: "auth_failed"}
	if e.Error() != "auth_failed" {
		t.Errorf("expected Code fallback, got %q", e.Error())
	}

	e2 := &ExecuteError{Code: "auth_failed", Message: "invalid token"}
	if e2.Error() != "invalid token" {
		t.Errorf("expected Message, got %q", e2.Error())
	}

	var eNil *ExecuteError
	if eNil.Error() != "" {
		t.Errorf("nil error should return empty string, got %q", eNil.Error())
	}
}

// ---------------------------------------------------------------------------
// RedactContext tests
// ---------------------------------------------------------------------------

func TestRedactContext(t *testing.T) {
	ctx := map[string]any{
		"bearerToken":  "secret-token",
		"apiKey":       "secret-key",
		"refreshToken": "secret-refresh",
		"accessToken":  "secret-access",
		"clientSecret": "secret-client",
		"basic": map[string]any{
			"username": "alice",
			"password": "secret-pass",
		},
		"custom": "visible",
	}

	redacted := RedactContext(ctx)

	if redacted["bearerToken"] != "[REDACTED]" {
		t.Errorf("bearerToken = %v, want [REDACTED]", redacted["bearerToken"])
	}
	if redacted["apiKey"] != "[REDACTED]" {
		t.Errorf("apiKey = %v, want [REDACTED]", redacted["apiKey"])
	}
	if redacted["refreshToken"] != "[REDACTED]" {
		t.Errorf("refreshToken = %v, want [REDACTED]", redacted["refreshToken"])
	}
	if redacted["accessToken"] != "[REDACTED]" {
		t.Errorf("accessToken = %v, want [REDACTED]", redacted["accessToken"])
	}
	if redacted["clientSecret"] != "[REDACTED]" {
		t.Errorf("clientSecret = %v, want [REDACTED]", redacted["clientSecret"])
	}
	basic := redacted["basic"].(map[string]any)
	if basic["username"] != "alice" {
		t.Errorf("basic.username = %v, want alice", basic["username"])
	}
	if basic["password"] != "[REDACTED]" {
		t.Errorf("basic.password = %v, want [REDACTED]", basic["password"])
	}
	if redacted["custom"] != "visible" {
		t.Errorf("custom = %v, want visible", redacted["custom"])
	}

	if ctx["bearerToken"] != "secret-token" {
		t.Error("original context was mutated")
	}
}

func TestRedactContext_Nil(t *testing.T) {
	if RedactContext(nil) != nil {
		t.Error("expected nil for nil input")
	}
}

// ---------------------------------------------------------------------------
// Well-known context helpers
// ---------------------------------------------------------------------------

func TestContextHelpers(t *testing.T) {
	ctx := map[string]any{
		"bearerToken": "tok123",
		"apiKey":      "key456",
		"basic": map[string]any{
			"username": "alice",
			"password": "pass",
		},
		"custom": "val",
	}

	if got := ContextBearerToken(ctx); got != "tok123" {
		t.Errorf("ContextBearerToken: %q", got)
	}
	if got := ContextAPIKey(ctx); got != "key456" {
		t.Errorf("ContextAPIKey: %q", got)
	}
	u, p, ok := ContextBasicAuth(ctx)
	if !ok || u != "alice" || p != "pass" {
		t.Errorf("ContextBasicAuth: %q %q %v", u, p, ok)
	}
	if got := ContextString(ctx, "custom"); got != "val" {
		t.Errorf("ContextString: %q", got)
	}

	if ContextBearerToken(nil) != "" || ContextAPIKey(nil) != "" || ContextString(nil, "x") != "" {
		t.Error("nil context should return empty strings")
	}
	_, _, ok = ContextBasicAuth(nil)
	if ok {
		t.Error("nil context should return ok=false")
	}
}

// ---------------------------------------------------------------------------
// withRuntime non-mutation tests
// ---------------------------------------------------------------------------

func TestWithRuntime_DoesNotMutateCallerInput(t *testing.T) {
	var capturedStore ContextStore

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			capturedStore = in.Store
			return SingleEventChannel(&ExecuteOutput{Output: "ok"}), nil
		},
	}

	store := NewMemoryStore()
	exec := NewOperationExecutor(executor)
	exec.ContextStore = store

	input := &BindingExecutionInput{
		Source: BindingExecutionSource{Format: "test"},
	}

	_, err := exec.ExecuteBinding(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}

	if capturedStore != store {
		t.Error("executor should have received the store")
	}
	if input.Store != nil {
		t.Error("caller's original input.Store was mutated; expected nil")
	}
	if input.Callbacks != nil {
		t.Error("caller's original input.Callbacks was mutated; expected nil")
	}
}

func TestWithRuntime_ReusableInput(t *testing.T) {
	callCount := 0
	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			callCount++
			if in.Store == nil {
				return nil, errors.New("store should be set")
			}
			return SingleEventChannel(&ExecuteOutput{Output: callCount}), nil
		},
	}

	exec := NewOperationExecutor(executor)
	exec.ContextStore = NewMemoryStore()

	input := &BindingExecutionInput{
		Source: BindingExecutionSource{Format: "test"},
	}

	for i := 0; i < 3; i++ {
		_, err := exec.ExecuteBinding(context.Background(), input)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if callCount != 3 {
		t.Errorf("expected 3 calls, got %d", callCount)
	}
}

// ---------------------------------------------------------------------------
// InterfaceClient.Close tests
// ---------------------------------------------------------------------------

func TestInterfaceClient_Close(t *testing.T) {
	executor := &mockExecutor{formats: []FormatInfo{{Token: "test"}}}
	exec := NewOperationExecutor(executor)
	ic := NewInterfaceClient(
		&Interface{OpenBindings: "0.1.0", Operations: map[string]Operation{}},
		exec,
	)

	if ic.State() != StateIdle {
		t.Fatalf("expected idle, got %s", ic.State())
	}

	ic.ResolveInterface(&Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{},
	})
	if ic.State() != StateBound {
		t.Fatalf("expected bound, got %s", ic.State())
	}

	ic.Close()
	if ic.State() != StateIdle {
		t.Errorf("expected idle after Close, got %s", ic.State())
	}
	if ic.Resolved() != nil {
		t.Error("resolved should be nil after Close")
	}
	if ic.ResolvedURL() != "" {
		t.Error("resolvedURL should be empty after Close")
	}

	ic.Close()
	if ic.State() != StateIdle {
		t.Error("double Close should not panic or change state")
	}
}

// ---------------------------------------------------------------------------
// ExecuteOperation integration
// ---------------------------------------------------------------------------

func TestExecuteOperation_ContextFlowsThrough(t *testing.T) {
	var capturedCtx map[string]any
	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			capturedCtx = in.Context
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Data: "ok"}
			close(ch)
			return ch, nil
		},
	}

	exec := NewOperationExecutor(executor)
	exec.ContextStore = NewMemoryStore()

	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getUser": {},
		},
		Sources: map[string]Source{
			"api": {Format: "test", Location: "https://api.example.com"},
		},
		Bindings: map[string]BindingEntry{
			"getUser.api": {Operation: "getUser", Source: "api", Ref: "#/paths/users/get"},
		},
	}

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "getUser",
		Context:   map[string]any{"bearerToken": "op-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if capturedCtx["bearerToken"] != "op-token" {
		t.Errorf("expected op-token, got %v", capturedCtx["bearerToken"])
	}
}

// ---------------------------------------------------------------------------
// Error sentinel tests
// ---------------------------------------------------------------------------

func TestBECErrorSentinels(t *testing.T) {
	if !errors.Is(ErrContextInsufficient, ErrContextInsufficient) {
		t.Error("ErrContextInsufficient identity check failed")
	}
	if !errors.Is(ErrResolutionUnavailable, ErrResolutionUnavailable) {
		t.Error("ErrResolutionUnavailable identity check failed")
	}

	wrapped := errors.Join(errors.New("outer"), ErrContextInsufficient)
	if !errors.Is(wrapped, ErrContextInsufficient) {
		t.Error("wrapped ErrContextInsufficient should be detectable")
	}
}

// ---------------------------------------------------------------------------
// DefaultBindingSelector tests
// ---------------------------------------------------------------------------

func TestDefaultBindingSelector_PrefersNonDeprecated(t *testing.T) {
	pri := 1.0
	iface := &Interface{
		Bindings: map[string]BindingEntry{
			"deprecated": {
				Operation:  "op",
				Source:     "s",
				Deprecated: true,
				Priority:   &pri,
			},
			"active": {
				Operation: "op",
				Source:     "s",
			},
		},
	}

	key, _, err := DefaultBindingSelector(iface, "op")
	if err != nil {
		t.Fatal(err)
	}
	if key != "active" {
		t.Errorf("expected active, got %q", key)
	}
}

func TestDefaultBindingSelector_LowerPriorityWins(t *testing.T) {
	low := 1.0
	high := 10.0
	iface := &Interface{
		Bindings: map[string]BindingEntry{
			"high": {Operation: "op", Source: "s", Priority: &high},
			"low":  {Operation: "op", Source: "s", Priority: &low},
		},
	}

	key, _, err := DefaultBindingSelector(iface, "op")
	if err != nil {
		t.Fatal(err)
	}
	if key != "low" {
		t.Errorf("expected low, got %q", key)
	}
}

func TestDefaultBindingSelector_NoMatch(t *testing.T) {
	iface := &Interface{
		Bindings: map[string]BindingEntry{
			"other": {Operation: "other", Source: "s"},
		},
	}
	_, _, err := DefaultBindingSelector(iface, "missing")
	if !errors.Is(err, ErrBindingNotFound) {
		t.Errorf("expected ErrBindingNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// PlatformCallbacks tests
// ---------------------------------------------------------------------------

func TestPlatformCallbacks_NilFieldsAreGraceful(t *testing.T) {
	cb := &PlatformCallbacks{}

	if cb.Prompt != nil {
		t.Error("nil Prompt should remain nil")
	}
	if cb.Confirmation != nil {
		t.Error("nil Confirmation should remain nil")
	}
	if cb.BrowserRedirect != nil {
		t.Error("nil BrowserRedirect should remain nil")
	}
	if cb.FileSelect != nil {
		t.Error("nil FileSelect should remain nil")
	}
}

func TestPlatformCallbacks_PromptIntegration(t *testing.T) {
	called := false
	cb := &PlatformCallbacks{
		Prompt: func(_ context.Context, msg string, opts *PromptOptions) (string, error) {
			called = true
			if opts == nil || !opts.Secret {
				t.Error("expected Secret=true")
			}
			return "user-input", nil
		},
	}

	val, err := cb.Prompt(context.Background(), "Enter token", &PromptOptions{Label: "bearerToken", Secret: true})
	if err != nil {
		t.Fatal(err)
	}
	if !called || val != "user-input" {
		t.Errorf("Prompt callback not working: called=%v val=%q", called, val)
	}
}

// ---------------------------------------------------------------------------
// Transform tests
// ---------------------------------------------------------------------------

// staticEvaluator is a TransformEvaluator that returns a fixed value.
type staticEvaluator struct {
	result any
	err    error
}

func (e *staticEvaluator) Evaluate(expression string, data any) (any, error) {
	return e.result, e.err
}

// captureEvaluator records each Evaluate call and returns the data unchanged.
type captureEvaluator struct {
	calls []struct {
		Expression string
		Data       any
	}
}

func (e *captureEvaluator) Evaluate(expression string, data any) (any, error) {
	e.calls = append(e.calls, struct {
		Expression string
		Data       any
	}{expression, data})
	return data, nil
}

func makeTransformInterface(inputTransform, outputTransform *TransformOrRef) *Interface {
	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{"op": {}},
		Sources:      map[string]Source{"src": {Format: "test", Location: "test://loc"}},
		Bindings: map[string]BindingEntry{
			"op.src": {
				Operation:       "op",
				Source:           "src",
				Ref:              "#/ref",
				InputTransform:  inputTransform,
				OutputTransform: outputTransform,
			},
		},
	}
	return iface
}

func TestExecuteOperation_InputTransformApplied(t *testing.T) {
	eval := &captureEvaluator{}

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			// The input should have been passed through the transform evaluator
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Data: in.Input}
			close(ch)
			return ch, nil
		},
	}

	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = eval

	iface := makeTransformInterface(
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$.name"}},
		nil,
	)

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
		Input:     map[string]any{"name": "alice"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	if len(eval.calls) != 1 {
		t.Fatalf("expected 1 evaluate call, got %d", len(eval.calls))
	}
	if eval.calls[0].Expression != "$.name" {
		t.Errorf("expression = %q, want %q", eval.calls[0].Expression, "$.name")
	}
}

func TestExecuteOperation_OutputTransformApplied(t *testing.T) {
	eval := &staticEvaluator{result: map[string]any{"transformed": true}}

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Data: map[string]any{"raw": true}}
			close(ch)
			return ch, nil
		},
	}

	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = eval

	iface := makeTransformInterface(
		nil,
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$.raw"}},
	)

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	m, ok := events[0].Data.(map[string]any)
	if !ok || m["transformed"] != true {
		t.Errorf("expected transformed output, got %v", events[0].Data)
	}
}

func TestExecuteOperation_OutputTransformMultipleEvents(t *testing.T) {
	callCount := 0
	eval := &captureEvaluator{}

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			ch := make(chan StreamEvent, 3)
			ch <- StreamEvent{Data: "event1"}
			ch <- StreamEvent{Data: "event2"}
			ch <- StreamEvent{Data: "event3"}
			close(ch)
			callCount++
			return ch, nil
		},
	}

	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = eval

	iface := makeTransformInterface(
		nil,
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$"}},
	)

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if len(eval.calls) != 3 {
		t.Fatalf("expected 3 evaluate calls, got %d", len(eval.calls))
	}
}

func TestExecuteOperation_InputTransformNoEvaluator(t *testing.T) {
	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
	}
	exec := NewOperationExecutor(executor)
	// No TransformEvaluator set

	iface := makeTransformInterface(
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$"}},
		nil,
	)

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(events))
	}
	if events[0].Error == nil || events[0].Error.Code != "transform_error" {
		t.Errorf("expected transform_error, got %+v", events[0])
	}
}

func TestExecuteOperation_OutputTransformNoEvaluator(t *testing.T) {
	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Data: "data"}
			close(ch)
			return ch, nil
		},
	}
	exec := NewOperationExecutor(executor)
	// No TransformEvaluator set

	iface := makeTransformInterface(
		nil,
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$"}},
	)

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(events))
	}
	if events[0].Error == nil || events[0].Error.Code != "transform_error" {
		t.Errorf("expected transform_error, got %+v", events[0])
	}
}

func TestExecuteOperation_TransformRef(t *testing.T) {
	eval := &captureEvaluator{}

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Data: in.Input}
			close(ch)
			return ch, nil
		},
	}

	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = eval

	iface := makeTransformInterface(
		&TransformOrRef{Ref: "#/transforms/myTransform"},
		nil,
	)
	iface.Transforms = map[string]Transform{
		"myTransform": {Type: "jsonata", Expression: "$.id"},
	}

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
		Input:     map[string]any{"id": 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	if len(eval.calls) != 1 {
		t.Fatalf("expected 1 evaluate call, got %d", len(eval.calls))
	}
	if eval.calls[0].Expression != "$.id" {
		t.Errorf("expression = %q, want %q", eval.calls[0].Expression, "$.id")
	}
}

func TestExecuteOperation_TransformRefNotFound(t *testing.T) {
	eval := &captureEvaluator{}

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
	}
	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = eval

	iface := makeTransformInterface(
		&TransformOrRef{Ref: "#/transforms/missing"},
		nil,
	)
	// No transforms defined — reference will fail

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(events))
	}
	if events[0].Error == nil || events[0].Error.Code != "transform_error" {
		t.Errorf("expected transform_error, got %+v", events[0])
	}
}

func TestExecuteOperation_OutputTransformErrorPassesThrough(t *testing.T) {
	eval := &staticEvaluator{err: errors.New("eval boom")}

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Data: "data"}
			close(ch)
			return ch, nil
		},
	}
	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = eval

	iface := makeTransformInterface(
		nil,
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$"}},
	)

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error == nil || events[0].Error.Code != "transform_error" {
		t.Errorf("expected transform_error from failed eval, got %+v", events[0])
	}
}

func TestExecuteOperation_OutputTransformSkipsErrorEvents(t *testing.T) {
	eval := &captureEvaluator{}

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			ch := make(chan StreamEvent, 2)
			ch <- StreamEvent{Error: &ExecuteError{Code: "upstream_error", Message: "something broke"}}
			ch <- StreamEvent{Data: "good"}
			close(ch)
			return ch, nil
		},
	}
	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = eval

	iface := makeTransformInterface(
		nil,
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$"}},
	)

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	// First event is the error — passed through without transform
	if events[0].Error == nil || events[0].Error.Code != "upstream_error" {
		t.Errorf("expected upstream error passthrough, got %+v", events[0])
	}
	// Second event is transformed (captureEvaluator returns data unchanged)
	if events[1].Data != "good" {
		t.Errorf("expected transformed data, got %v", events[1].Data)
	}
	// Only the data event should have been evaluated
	if len(eval.calls) != 1 {
		t.Errorf("expected 1 evaluate call (skip error event), got %d", len(eval.calls))
	}
}

func TestExecuteOperation_TransformStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			ch := make(chan StreamEvent)
			go func() {
				defer close(ch)
				for i := 0; ; i++ {
					select {
					case ch <- StreamEvent{Data: i}:
					case <-ctx.Done():
						return
					}
				}
			}()
			return ch, nil
		},
	}
	exec := NewOperationExecutor(executor)
	exec.TransformEvaluator = &captureEvaluator{}

	iface := makeTransformInterface(
		nil,
		&TransformOrRef{Transform: &Transform{Type: "jsonata", Expression: "$"}},
	)

	ch, err := exec.ExecuteOperation(ctx, &OperationExecutionInput{
		Interface: iface,
		Operation: "op",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Read one event then cancel
	ev, ok := <-ch
	if !ok {
		t.Fatal("expected at least one event")
	}
	if ev.Data == nil {
		t.Fatal("expected data event")
	}

	cancel()

	// Channel should close after cancellation
	remaining := 0
	for range ch {
		remaining++
	}
	// We don't assert exact count — just that the channel closes and doesn't hang
	_ = remaining
}
