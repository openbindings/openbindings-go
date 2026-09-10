package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	jsonataruntime "github.com/openbindings/jsonata/go"
	openbindings "github.com/openbindings/openbindings-go"
)

type cancellationEvaluator func(context.Context, string, any) (any, error)

func (f cancellationEvaluator) Evaluate(ctx context.Context, expr string, data any) (any, error) {
	return f(ctx, expr, data)
}

func cancellationInterface(direction string) *openbindings.Interface {
	iface := opTestInterface()
	entry := iface.Bindings["echo.transformed"]
	entry.InputTransform, entry.OutputTransform = nil, nil
	if direction == "input" {
		entry.InputTransform = &openbindings.TransformOrRef{Inline: "$"}
	} else {
		entry.OutputTransform = &openbindings.TransformOrRef{Inline: "$"}
	}
	iface.Bindings["echo.transformed"] = entry
	return iface
}

func TestCancellationInvocationReachesEvaluator(t *testing.T) {
	for _, direction := range []string{"input", "output"} {
		t.Run(direction, func(t *testing.T) {
			entered, stopped := make(chan struct{}), make(chan struct{})
			ctx, cleanup := context.WithTimeout(context.Background(), 3*time.Second)
			defer cleanup()
			op := newOpInvoker(&mockBindingInvoker{}, nil)
			op.TransformEvaluator = cancellationEvaluator(func(ctx context.Context, _ string, _ any) (any, error) {
				close(entered)
				<-ctx.Done()
				close(stopped)
				return nil, ctx.Err()
			})
			call := Invoke(ctx, op, cancellationInterface(direction), NewOperationSignature[any, any]("echo"))
			defer call.Cancel()
			go func() { _ = call.Write(ctx, map[string]any{"id": "1"}) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("transform did not start")
			}
			call.Cancel()
			_, err := call.Outputs().Read(ctx)
			if codeOf(t, err) != ErrCodeCancelled {
				t.Fatalf("wrong terminal: %v", err)
			}
			select {
			case <-stopped:
			case <-ctx.Done():
				t.Fatal("evaluator did not stop")
			}
		})
	}
}

func TestCancellationTransformBoundaryGuards(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "pre_cancelled", true: "late_noncooperative_result"}[late], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			e := cancellationEvaluator(func(context.Context, string, any) (any, error) {
				called = true
				cancel()
				return "must not escape", nil
			})
			if !late {
				cancel()
			}
			v, err := applyTransformRef(ctx, e, nil, &openbindings.TransformOrRef{Inline: "$"}, nil)
			if !errors.Is(err, context.Canceled) || v != nil || called != late {
				t.Fatalf("value=%v error=%v called=%v", v, err, called)
			}
		})
	}
}

func TestCancellationNativeEvaluationThroughSDK(t *testing.T) {
	// Completion below means the native evaluator returned, not just its waiter.
	compiled, err := jsonataruntime.New(jsonataruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"input", "output"} {
		t.Run(direction, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			entered, stopped := make(chan struct{}), make(chan error, 1)
			op := newOpInvoker(&mockBindingInvoker{}, nil)
			op.TransformEvaluator = cancellationEvaluator(func(ctx context.Context, _ string, v any) (any, error) {
				close(entered)
				data, _ := json.Marshal(v)
				result, err := compiled.Evaluate(ctx, `$sum($map([1..1000000], function($v){$v*$v}))`, data, nil)
				stopped <- err
				return result, err
			})
			call := Invoke(ctx, op, cancellationInterface(direction), NewOperationSignature[any, any]("echo"))
			defer call.Cancel()
			go func() { _ = call.Write(ctx, map[string]any{"id": "1"}) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("not started")
			}
			timer := time.AfterFunc(5*time.Millisecond, call.Cancel)
			defer timer.Stop()
			select {
			case err := <-stopped:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("native did not observe cancellation: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("native evaluation did not terminate")
			}
			values, err := drainOutputs(t, call)
			if len(values) != 0 || codeOf(t, err) != ErrCodeCancelled {
				t.Fatalf("%v %v", values, err)
			}
		})
	}
}

func TestCancellationNativeConcurrentReuseAndDeadline(t *testing.T) {
	compiled, err := jsonataruntime.New(jsonataruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := compiled.Evaluate(ctx, `slow ? $sum($map([1..1000000], function($v){$v*$v})) : id`, []byte(`{"slow":true}`), nil)
		done <- err
	}()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := compiled.Evaluate(context.Background(), `slow ? $sum($map([1..1000000], function($v){$v*$v})) : id`, []byte(`{"slow":false,"id":"unrelated"}`), nil)
			if err != nil || string(v) != `"unrelated"` {
				t.Errorf("unrelated evaluation: %v %v", v, err)
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadline did not stop native evaluation")
	}
}

func TestCancellationRetiredTransformPreservesRawInput(t *testing.T) {
	// The production pump across two attempts; the first retires mid-transform.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	caller := NewInvocationImpl[any, any](ctx)
	first := NewInvocationImpl[any, any](ctx)
	second := NewInvocationImpl[any, any](ctx)
	defer caller.Cancel()
	defer first.Cancel()
	defer second.Cancel()
	attempt, retire := context.WithCancel(ctx)
	defer retire()
	entered, stopped := make(chan struct{}), make(chan struct{})
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	calls := 0
	op.TransformEvaluator = cancellationEvaluator(func(ctx context.Context, _ string, value any) (any, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return map[string]any{"mapped": value}, nil
	})
	iface := cancellationInterface("input")
	binding := iface.Bindings["echo.transformed"]
	pending := &pendingTransformInput{}
	var recorded []any
	record := func(v any) { recorded = append(recorded, v) }
	go func() {
		defer close(stopped)
		op.pumpInputs(attempt, ctx, caller, first, &binding, "echo.transformed", iface, nil, record, pending)
	}()
	go func() { _ = caller.Write(ctx, "raw"); _ = caller.Close() }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("not entered")
	}
	retire()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("not stopped")
	}
	if !pending.present || len(recorded) != 0 {
		t.Fatal("raw input not retained")
	}
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		op.pumpInputs(ctx, ctx, caller, second, &binding, "echo.transformed", iface, nil, record, pending)
	}()
	v, err := second.ReadInput(ctx)
	if err != nil || v.(map[string]any)["mapped"] != "raw" {
		t.Fatalf("%v %v", v, err)
	}
	select {
	case <-secondDone:
	case <-ctx.Done():
		t.Fatal("retry did not finish")
	}
	if calls != 2 || len(recorded) != 1 || pending.present {
		t.Fatalf("calls=%d records=%v", calls, recorded)
	}
}
