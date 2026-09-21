package invoke

import (
	"context"
	"errors"
	"testing"
)

func TestEarlyPreparationDoesNotResolveOrInvoke(t *testing.T) {
	binding := &preparerMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
	resolved := 0
	invoker := newOpInvoker(binding, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		resolved++
		return map[string]any{"bearerToken": "token"}, nil
	})
	for i := 0; i < 2; i++ {
		details, err := invoker.PrepareOperation(t.Context(), opTestInterface(), "getUser")
		if err != nil || details == nil {
			t.Fatalf("prepare=(%v, %v)", details, err)
		}
	}
	if attempts, prepares, _, _ := binding.snapshot(); attempts != 0 || prepares != 2 || resolved != 0 {
		t.Fatalf("early work invoked or resolved: attempts=%d preparations=%d resolutions=%d", attempts, prepares, resolved)
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
	if attempts, prepares, _, _ := binding.snapshot(); attempts != 1 || prepares != 3 || resolved != 1 {
		t.Fatalf("invocation bypassed current preparation: attempts=%d preparations=%d resolutions=%d", attempts, prepares, resolved)
	}
}

func TestPreparationFailureStopsOnlyCurrentCall(t *testing.T) {
	requiredFailure := NewInvocationError(ErrCodeSourceLoadFailed)
	failing := true
	binding := &cancellationPreparer{prepare: func(context.Context) (*ContextRequiredDetails, error) {
		if failing {
			return nil, requiredFailure
		}
		return nil, nil
	}}
	invoker := newOpInvoker(binding, nil)
	if _, err := invoker.PrepareOperation(t.Context(), opTestInterface(), "ping"); !errors.Is(err, requiredFailure) {
		t.Fatalf("explicit preparation hid its error: %v", err)
	}
	call := Invoke(t.Context(), invoker, opTestInterface(), NewOperationSignature[any, any]("ping"))
	defer call.Cancel()
	if _, err := drainOutputs(t, call); codeOf(t, err) != ErrCodeSourceLoadFailed {
		t.Fatalf("automatic preparation hid its error: %v", err)
	}
	if attempts, _, _, _ := binding.snapshot(); attempts != 0 {
		t.Fatal("execution started after required preparation failed")
	}
	failing = false // The later invocation reassesses current conditions.
	later := Invoke(t.Context(), invoker, opTestInterface(), NewOperationSignature[any, any]("ping"))
	defer later.Cancel()
	if _, err := drainOutputs(t, later); err != nil {
		t.Fatalf("earlier preparation failure disabled invocation: %v", err)
	}
}
