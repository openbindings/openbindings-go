package invoke

import (
	"context"
	"encoding/json"
	"io"
	"sync/atomic"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

type preparedEchoBinding struct{ calls atomic.Int64 }

func (b *preparedEchoBinding) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: "example.prepared@1"}}
}

func (b *preparedEchoBinding) PrepareBinding(context.Context, *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	return nil, nil
}

func (b *preparedEchoBinding) InvokeBinding(ctx context.Context, _ *BindingInvocationArgs) Invocation[any, any] {
	b.calls.Add(1)
	invocation := NewInvocationImpl[any, any](ctx)
	go func() {
		value, err := invocation.ReadInput(ctx)
		if err != nil {
			if err != io.EOF {
				invocation.FireError(AsInvocationError(err))
			}
			return
		}
		_ = invocation.CloseInput()
		if err := invocation.EmitOutput(value); err == nil {
			invocation.CloseOutput()
		}
	}()
	return invocation
}

func TestCompiledRealizationPinsPreparedWork(t *testing.T) {
	raw := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"echo": {Input: map[string]any{"type": "string"}, Output: map[string]any{"type": "string"}},
		},
		Sources: map[string]openbindings.Source{
			"local": {BindingSpec: "example.prepared@1", Content: json.RawMessage(`{}`)},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"echo": {Operation: "echo", Source: "local"},
		},
	}
	prepared, err := openbindings.PrepareInterface(raw)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := prepared.Binding("echo")
	engine := &preparedEchoBinding{}
	behavior, err := NewOperationInvoker(engine).CompileRealization(shortCtx(t), prepared, binding)
	if err != nil {
		t.Fatal(err)
	}

	// Neither later caller mutation nor a generic binding option can retarget
	// the SDK-selected realization.
	raw.Bindings["echo"] = openbindings.BindingEntry{Operation: "missing", Source: "missing"}
	for _, value := range []string{"one", "two"} {
		call := behavior.Invoke(shortCtx(t), WithBindingKey("forged"))
		if err := call.Write(shortCtx(t), value); err != nil {
			t.Fatal(err)
		}
		output, err := Single(shortCtx(t), call.Outputs())
		if err != nil || output != value {
			t.Fatalf("output=%v err=%v", output, err)
		}
	}
	if engine.calls.Load() != 2 {
		t.Fatalf("binding calls = %d", engine.calls.Load())
	}

	invalid := behavior.Invoke(shortCtx(t))
	if err := invalid.Write(shortCtx(t), 42); err == nil {
		t.Fatal("compiled input validator accepted a number")
	}
}

func TestTypedInvocationPreservesNativeJSONReference(t *testing.T) {
	inner := NewInvocationImpl[any, any](shortCtx(t))
	typed := NewTypedInvocation[map[string]any, any](inner)
	input := map[string]any{"nested": []any{"value"}}
	if err := typed.Write(shortCtx(t), input); err != nil {
		t.Fatal(err)
	}
	received, err := inner.ReadInput(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	receivedMap, ok := received.(map[string]any)
	if !ok {
		t.Fatalf("received %T", received)
	}
	receivedMap["identity"] = true
	if input["identity"] != true {
		t.Fatal("native JSON input was cloned")
	}
	inner.Cancel()
}
