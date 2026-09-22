package invoke

import (
	"context"
	"errors"
	"testing"
)

func TestEarlyPreflightDoesNotResolveOrInvoke(t *testing.T) {
	binding := &preflighterMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
	resolved := 0
	invoker := newOpInvoker(binding, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		resolved++
		return map[string]any{"bearerToken": "token"}, nil
	})
	for i := 0; i < 2; i++ {
		details, err := invoker.PreflightOperation(t.Context(), opTestInterface(), "getUser")
		if err != nil || details == nil {
			t.Fatalf("prepare=(%v, %v)", details, err)
		}
	}
	if attempts, preflights, _, _ := binding.snapshot(); attempts != 0 || preflights != 2 || resolved != 0 {
		t.Fatalf("early work invoked or resolved: attempts=%d preflights=%d resolutions=%d", attempts, preflights, resolved)
	}
	call := Invoke(t.Context(), invoker, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	defer call.Cancel()
	if err := call.Write(t.Context(), map[string]any{"id": "user"}); err != nil {
		t.Fatal(err)
	}
	_ = call.Close()
	if _, err := drainOutputs(t, call); err != nil {
		t.Fatal(err)
	}
	if attempts, preflights, _, _ := binding.snapshot(); attempts != 1 || preflights != 3 || resolved != 1 {
		t.Fatalf("invocation bypassed current preflight: attempts=%d preflights=%d resolutions=%d", attempts, preflights, resolved)
	}
}

func TestPreflightFailureStopsOnlyCurrentCall(t *testing.T) {
	requiredFailure := NewInvocationError(ErrCodeSourceLoadFailed)
	failing := true
	binding := &cancellationPreflighter{prepare: func(context.Context) (*ContextRequiredDetails, error) {
		if failing {
			return nil, requiredFailure
		}
		return nil, nil
	}}
	invoker := newOpInvoker(binding, nil)
	if _, err := invoker.PreflightOperation(t.Context(), opTestInterface(), "ping"); !errors.Is(err, requiredFailure) {
		t.Fatalf("explicit preflight hid its error: %v", err)
	}
	call := Invoke(t.Context(), invoker, opTestInterface(), NewOperationSignature[any, any]("ping"))
	defer call.Cancel()
	if _, err := drainOutputs(t, call); codeOf(t, err) != ErrCodeSourceLoadFailed {
		t.Fatalf("automatic preflight hid its error: %v", err)
	}
	if attempts, _, _, _ := binding.snapshot(); attempts != 0 {
		t.Fatal("execution started after preflight failed")
	}
	failing = false // The later invocation reassesses current conditions.
	later := Invoke(t.Context(), invoker, opTestInterface(), NewOperationSignature[any, any]("ping"))
	defer later.Cancel()
	if _, err := drainOutputs(t, later); err != nil {
		t.Fatalf("earlier preflight failure disabled invocation: %v", err)
	}
}
