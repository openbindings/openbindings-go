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

// A preflight error means the binding could not answer and predicts nothing:
// the explicit call reports it, and ordinary invocation proceeds to its one
// attempt as if preflight had reported nothing. The outcome is the attempt's.
func TestPreflightErrorDoesNotStopInvocation(t *testing.T) {
	requiredFailure := NewInvocationError(ErrCodeSourceLoadFailed)
	binding := &cancellationPreflighter{prepare: func(context.Context) (*ContextRequiredDetails, error) {
		return nil, requiredFailure
	}}
	resolved := 0
	invoker := newOpInvoker(binding, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		resolved++
		return nil, nil
	})
	if _, err := invoker.PreflightOperation(t.Context(), opTestInterface(), "ping"); !errors.Is(err, requiredFailure) {
		t.Fatalf("explicit preflight hid its error: %v", err)
	}
	call := Invoke(t.Context(), invoker, opTestInterface(), NewOperationSignature[any, any]("ping"))
	defer call.Cancel()
	outputs, err := drainOutputs(t, call)
	if err != nil || len(outputs) != 1 {
		t.Fatalf("preflight error stopped invocation: outputs=%v err=%v", outputs, err)
	}
	if attempts, _, _, _ := binding.snapshot(); attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if resolved != 0 {
		t.Fatalf("resolver consulted %d time(s) with no details", resolved)
	}

	// The attempt's own outcome surfaces, including its failure.
	failing := &cancellationPreflighter{
		mockBindingInvoker: mockBindingInvoker{opts: mockOpts{nativeFailure: true}},
		prepare: func(context.Context) (*ContextRequiredDetails, error) {
			return nil, requiredFailure
		},
	}
	later := Invoke(t.Context(), newOpInvoker(failing, nil), opTestInterface(), NewOperationSignature[any, any]("ping"))
	defer later.Cancel()
	if _, err := drainOutputs(t, later); codeOf(t, err) != ErrCodeExecutionFailed {
		t.Fatalf("attempt outcome was not the invocation's outcome: %v", err)
	}
}
