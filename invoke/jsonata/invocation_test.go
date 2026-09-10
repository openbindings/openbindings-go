package jsonata_test

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	jsonataevaluator "github.com/openbindings/openbindings-go/invoke/jsonata"
)

type echoBinding struct{ received atomic.Int32 }

func (*echoBinding) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: "test.jsonata@1"}}
}
func (b *echoBinding) CheckBindingSpecs(names []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(names, b.BindingSpecs())
}
func (b *echoBinding) InvokeBinding(ctx context.Context, _ *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	handle := invoke.NewInvocationImpl[any, any](ctx)
	go func() {
		value, err := handle.ReadInput(ctx)
		if err != nil {
			handle.CloseOutput()
			return
		}
		b.received.Add(1)
		_ = handle.CloseInput()
		if err := handle.EmitOutput(value); err != nil {
			return
		}
		handle.CloseOutput()
	}()
	return handle
}

func fixture(direction string, named, schema bool, expression string) *openbindings.Interface {
	tor := &openbindings.TransformOrRef{Inline: expression}
	if named {
		tor = &openbindings.TransformOrRef{Ref: "#/transforms/map"}
	}
	entry := openbindings.BindingEntry{Operation: "echo", Source: "test"}
	if direction == "input" {
		entry.InputTransform = tor
	} else {
		entry.OutputTransform = tor
	}
	op := openbindings.Operation{}
	if schema {
		op.Input = map[string]any{}
		op.Output = map[string]any{}
	}
	return &openbindings.Interface{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"echo": op}, Sources: map[string]openbindings.Source{"test": {BindingSpec: "test.jsonata@1", Content: []byte(`{}`)}}, Transforms: map[string]openbindings.Transform{"map": openbindings.Transform(expression)}, Bindings: map[string]openbindings.BindingEntry{"echo": entry}}
}

func invokeOnce(t *testing.T, iface *openbindings.Interface, input any, evaluator invoke.TransformEvaluator) ([]any, error, int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	binding := &echoBinding{}
	op := invoke.NewOperationInvoker(binding)
	op.TransformEvaluator = evaluator
	call := invoke.Invoke(ctx, op, iface, invoke.NewOperationSignature[any, any]("echo"))
	defer call.Cancel()
	written := make(chan struct{})
	go func() { defer close(written); _ = call.Write(ctx, input); _ = call.Close() }()
	stream := call.Outputs()
	var values []any
	for {
		value, err := stream.Read(ctx)
		if err != nil {
			<-written
			if errors.Is(err, io.EOF) {
				return values, nil, binding.received.Load()
			}
			return values, err, binding.received.Load()
		}
		values = append(values, value)
	}
}

func TestOfficialAdapterOperationJourneys(t *testing.T) {
	e := evaluator(t, jsonataevaluator.Options{})
	input := jsonInput(t, `{"id":9223372036854775807,"amount":0.12345678901234567890123456789,"\ud800":"\udc00","empty":[],"null":null}`)
	for _, direction := range []string{"input", "output"} {
		for _, named := range []bool{false, true} {
			for _, schema := range []bool{false, true} {
				for _, c := range []struct {
					expression string
					want       any
					failed     bool
				}{
					{`$`, input, false},
					{`$ ~> |$|{"comparison":id = 9223372036854775807,"sum":0.1+0.2}|`, jsonInput(t, `{"id":9223372036854775807,"amount":0.12345678901234567890123456789,"\ud800":"\udc00","empty":[],"null":null,"comparison":true,"sum":0.3}`), false},
					{`null`, nil, false},
					{`missing`, nil, true},
					{`{"nested":[function(){1}]}`, nil, true},
					{`$error("test failure")`, nil, true},
				} {
					values, err, received := invokeOnce(t, fixture(direction, named, schema, c.expression), input, e)
					if c.failed {
						var failure *invoke.InvocationError
						if !errors.As(err, &failure) || failure.Code != invoke.ErrCodeTransformError || len(values) != 0 {
							t.Fatalf("%s %s: %#v %v", direction, c.expression, values, err)
						}
						if direction == "input" && received != 0 {
							t.Fatal("failed input reached binding")
						}
						continue
					}
					if err != nil || len(values) != 1 || !equal(values[0], c.want) {
						t.Fatalf("%s %s named=%v schema=%v: %#v %v", direction, c.expression, named, schema, values, err)
					}
				}
			}
		}
	}
}

type unusedEvaluator struct{ calls atomic.Int32 }

func (e *unusedEvaluator) Evaluate(context.Context, string, any) (any, error) {
	e.calls.Add(1)
	return nil, errors.New("must not be called")
}
func TestTransformFreeDoesNotUseEvaluator(t *testing.T) {
	iface := fixture("input", false, false, "$")
	entry := iface.Bindings["echo"]
	entry.InputTransform = nil
	iface.Bindings["echo"] = entry
	e := &unusedEvaluator{}
	values, err, received := invokeOnce(t, iface, jsonInput(t, `{"id":9007199254740993}`), e)
	if err != nil || len(values) != 1 || received != 1 || e.calls.Load() != 0 {
		t.Fatalf("transform-free: %#v %v calls=%d", values, err, e.calls.Load())
	}
}
