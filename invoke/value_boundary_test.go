package invoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/internal/value"
)

func TestRawAndTypedAnyOutputsAreDetachedLogicalValues(t *testing.T) {
	for _, typed := range []bool{false, true} {
		i := NewInvocationImpl[any, any](context.Background())
		payload := map[string]any{"image": []byte{1, 2, 3}, "child": map[string]any{"name": "before"}}
		if err := i.EmitOutput(payload); err != nil {
			t.Fatal(err)
		}
		payload["image"].([]byte)[0] = 9
		payload["child"].(map[string]any)["name"] = "after"
		i.CloseOutput()
		var out OutputStream[any]
		if typed {
			out = NewTypedInvocation[any, any](i).Outputs()
		} else {
			out = i.Outputs()
		}
		got, err := out.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		m := got.(map[string]any)
		if m["image"] != "AQID" || m["child"].(map[string]any)["name"] != "before" {
			t.Fatalf("public representation: %#v", m)
		}
		if _, err = out.Read(context.Background()); err != io.EOF {
			t.Fatal(err)
		}
	}
}
func TestAcceptedOutputDrainsAfterCaptureLimitFailure(t *testing.T) {
	i := NewInvocationImpl[any, any](context.Background(), WithInvocationValueLimits(ValueLimits{MaxValueUnits: 512}))
	if err := i.EmitOutput("accepted"); err != nil {
		t.Fatal(err)
	}
	err := i.EmitOutput(strings.Repeat("x", 600))
	var limit *value.LimitError
	if !errors.As(err, &limit) || codeOf(t, err) != ErrCodeRuntime {
		t.Fatalf("resource cause: %v", err)
	}
	out := i.Outputs()
	got, err := out.Read(context.Background())
	if err != nil || got != "accepted" {
		t.Fatalf("lost accepted prefix: %v %v", got, err)
	}
	if _, err = out.Read(context.Background()); codeOf(t, err) != ErrCodeRuntime {
		t.Fatal(err)
	}
}
func TestDepthLimitRefusesDeepValues(t *testing.T) {
	i := NewInvocationImpl[any, any](context.Background(), WithInvocationValueLimits(ValueLimits{MaxDepth: 3}))
	shallow := map[string]any{"a": map[string]any{"b": "leaf"}}
	if err := i.Write(context.Background(), shallow); err != nil {
		t.Fatal(err)
	}
	deep := map[string]any{"a": map[string]any{"b": map[string]any{"c": map[string]any{"d": "leaf"}}}}
	err := i.Write(context.Background(), deep)
	var limit *value.LimitError
	if !errors.As(err, &limit) || limit.Kind != "depth" || codeOf(t, err) != ErrCodeRuntime {
		t.Fatalf("depth refusal: %v", err)
	}
}
func TestTypedConstructionFailureConsumesOneOutputOnly(t *testing.T) {
	type padded struct {
		Padding [4096]byte `json:"-"`
	}
	i := NewInvocationImpl[any, any](context.Background(), WithInvocationValueLimits(ValueLimits{MaxValueUnits: 512}))
	if err := i.EmitOutput(map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := i.EmitOutput(nil); err != nil {
		t.Fatal(err)
	}
	i.CloseOutput()
	out := NewTypedInvocation[any, *padded](i).Outputs()
	got, err := out.Read(context.Background())
	var conversion *ValueConversionError
	if got != nil || !errors.As(err, &conversion) || codeOf(t, err) != ErrCodeRuntime {
		t.Fatalf("expected local conversion error: %v %v", got, err)
	}
	if got, err = out.Read(context.Background()); got != nil || err != nil {
		t.Fatalf("next null lost: %v %v", got, err)
	}
	if _, err = out.Read(context.Background()); err != io.EOF {
		t.Fatal(err)
	}
}
func TestInvalidLimitsPreventInvocationDispatch(t *testing.T) {
	mock := &mockBindingInvoker{}
	op := newOpInvoker(mock, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"), WithValueLimits(ValueLimits{MaxValueUnits: -1}))
	_, err := call.Outputs().Read(shortCtx(t))
	if codeOf(t, err) != ErrCodeRuntime {
		t.Fatal(err)
	}
	attempts, _, _, _ := mock.snapshot()
	if attempts != 0 {
		t.Fatal("invalid configuration dispatched binding")
	}
}

func TestTerminalDataIsIsolatedAndBounded(t *testing.T) {
	i := NewInvocationImpl[any, any](bg())
	data := map[string]any{"nested": []any{"before"}}
	original := &InvocationError{Code: ErrCodeExecutionFailed, Data: data}
	i.FireError(original)
	data["nested"].([]any)[0] = "producer mutation"
	out := i.Outputs()
	_, err := out.Read(bg())
	first := AsInvocationError(err)
	first.Data.(map[string]any)["nested"].([]any)[0] = "observer mutation"
	first.Code = "changed"
	_, err = out.Read(bg())
	second := AsInvocationError(err)
	if second.Code != ErrCodeExecutionFailed || second.Data.(map[string]any)["nested"].([]any)[0] != "before" {
		t.Fatalf("shared terminal: %#v", second)
	}
	for _, input := range []*InvocationError{NewInvocationError(ErrCodeExecutionFailed), NewInvocationErrorWithData(ErrCodeExecutionFailed, nil)} {
		c := NewInvocationImpl[any, any](bg())
		c.FireError(input)
		_, err := c.Outputs().Read(bg())
		if AsInvocationError(err).HasData() != input.HasData() {
			t.Fatal("lost data presence")
		}
	}
	// Terminal data is captured under the per-value limit like any other
	// value; a refusal degrades the terminal to code-only ERR_RUNTIME.
	limited := NewInvocationImpl[any, any](bg(), WithInvocationValueLimits(ValueLimits{MaxValueUnits: value.NodeUnits}))
	limited.FireError(NewInvocationErrorWithData(ErrCodeExecutionFailed, "exceeds the per-value allowance"))
	_, err = limited.Outputs().Read(bg())
	if codeOf(t, err) != ErrCodeRuntime || AsInvocationError(err).HasData() {
		t.Fatal(err)
	}
}

// TestLiveChallengeTerminalSurvivesStop: Stop is exactly Cancel. It abandons
// unread outputs by cancelling, never overwrites a real terminal, and does not
// discard the terminal's data, so a caller that stopped reading can still
// recover the challenge it needs to resolve.
func TestLiveChallengeTerminalSurvivesStop(t *testing.T) {
	i := NewInvocationImpl[any, any](bg())
	if err := i.EmitOutput("unread"); err != nil {
		t.Fatal(err)
	}
	i.FireError(NewContextRequiredError(bearerDetails))
	out := i.Outputs()
	out.Stop()
	_, err := out.Read(bg())
	if err == nil {
		// The unread output drains first; the terminal follows.
		_, err = out.Read(bg())
	}
	details := ContextRequiredFrom(AsInvocationError(err))
	if details == nil || details.Target != bearerDetails.Target {
		t.Fatalf("terminal data lost after Stop: %v", err)
	}
	_, err = out.Read(bg())
	if ContextRequiredFrom(AsInvocationError(err)) == nil {
		t.Fatalf("terminal not sticky after Stop: %v", err)
	}
}

type foreignOverride struct {
	*InvocationImpl[any, any]
	writes, outputs int
}

func (f *foreignOverride) Write(ctx context.Context, v any) error {
	f.writes++
	return f.InvocationImpl.Write(ctx, v)
}
func (f *foreignOverride) Outputs() OutputStream[any] { f.outputs++; return f.InvocationImpl.Outputs() }
func TestForeignOverridesAreNotBypassedByTypedFacade(t *testing.T) {
	foreign := &foreignOverride{InvocationImpl: NewInvocationImpl[any, any](bg())}
	type picture struct {
		Data []byte `json:"data"`
	}
	call := NewTypedInvocation[picture, picture](foreign)
	bytes := []byte{1, 2, 3}
	if err := call.Write(bg(), picture{bytes}); err != nil {
		t.Fatal(err)
	}
	bytes[0] = 9
	v, err := foreign.ReadInput(bg())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["data"] != "AQID" {
		t.Fatalf("foreign received private carrier or shared input: %#v", v)
	}
	if err := foreign.EmitOutput(v); err != nil {
		t.Fatal(err)
	}
	foreign.CloseOutput()
	result, err := Single(bg(), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if foreign.writes != 1 || foreign.outputs != 1 || result.Data[0] != 1 {
		t.Fatalf("overrides %d/%d result %v", foreign.writes, foreign.outputs, result)
	}
}

func TestProducerReuseAfterAcceptedWriteAndEmit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	i := NewInvocationImpl[map[string]any, map[string]any](ctx)
	out := i.Outputs()
	const count = 200
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		payload := map[string]any{"items": []any{""}}
		for n := 0; n < count; n++ {
			payload["items"].([]any)[0] = fmt.Sprint(n)
			if err := i.Write(ctx, payload); err != nil {
				t.Error(err)
				return
			}
			payload["items"].([]any)[0] = "reused"
		}
		_ = i.Close()
	}()
	go func() {
		defer workers.Done()
		for {
			payload, err := i.ReadInput(ctx)
			if err == io.EOF {
				i.CloseOutput()
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			if err := i.EmitOutput(payload); err != nil {
				t.Error(err)
				return
			}
			payload["items"].([]any)[0] = "handler reused"
		}
	}()
	for n := 0; n < count; n++ {
		payload, err := out.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if payload["items"].([]any)[0] != fmt.Sprint(n) {
			t.Fatalf("mutable handoff: %v", payload)
		}
	}
	if _, err := out.Read(ctx); err != io.EOF {
		t.Fatal(err)
	}
	workers.Wait()
}

type panicValueEvaluator struct{}

func (panicValueEvaluator) Evaluate(context.Context, string, any) (any, error) {
	panic("callback failure")
}

// TestTransformPanicRetiresOwnedAttempt: a panicking foreign evaluator on
// either pump ends the one attempt loudly (ERR_RUNTIME) and the run returns,
// with the input pump joined, rather than stranding a goroutine.
func TestTransformPanicRetiresOwnedAttempt(t *testing.T) {
	for _, phase := range []string{"input", "output"} {
		t.Run(phase, func(t *testing.T) {
			iface := opTestInterface()
			for key, binding := range iface.Bindings {
				if binding.Operation != "getUser" {
					continue
				}
				var transform openbindings.TransformOrRef = openbindings.InlineTransform("$")
				if phase == "input" {
					binding.InputTransform = transform
				} else {
					binding.OutputTransform = transform
				}
				iface.Bindings[key] = binding
			}
			op := newOpInvoker(&mockBindingInvoker{}, nil)
			op.TransformEvaluator = panicValueEvaluator{}
			call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("getUser"))
			_ = call.Write(shortCtx(t), map[string]any{"id": "one"})
			_ = call.Close()
			_, err := call.Outputs().Read(shortCtx(t))
			if codeOf(t, err) != ErrCodeRuntime {
				t.Fatal(err)
			}
		})
	}
}
