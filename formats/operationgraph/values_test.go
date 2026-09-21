package operationgraph

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go/internal/valueio"
	"github.com/openbindings/openbindings-go/invoke"
)

type mutatingEvaluator struct{ calls atomic.Int32 }

func (e *mutatingEvaluator) Evaluate(ctx context.Context, expression string, input any) (any, error) {
	return e.EvaluateWithBindings(ctx, expression, input, nil)
}
func (e *mutatingEvaluator) EvaluateWithBindings(_ context.Context, expression string, input any, bindings map[string]any) (any, error) {
	e.calls.Add(1)
	if expression == "change" {
		input.(map[string]any)["n"] = 2
		bindings["input"].(map[string]any)["n"] = 3
		return input, nil
	}
	return bindings["input"], nil
}
func strptr(s string) *string { return &s }

func TestGraphCallbacksCannotMutateSiblingOrRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "change": {Type: "transform", Transform: strptr("change")}, "root": {Type: "transform", Transform: strptr("root")}, "out": {Type: "output"}}, Edges: []Edge{{From: "in", To: "change"}, {From: "in", To: "root"}, {From: "change", To: "out"}, {From: "root", To: "out"}}}
	evaluator := &mutatingEvaluator{}
	call := invoke.NewInvocationImpl[any, any](ctx)
	eng := newEngine(graph, invoke.NewOperationInvoker(), &invoke.BindingInvocationArgs{}, evaluator, newSchemaCache())
	done := make(chan struct{})
	go func() { defer close(done); eng.execute(ctx, call) }()
	input := map[string]any{"n": 1}
	if err := call.Write(ctx, input); err != nil {
		t.Fatal(err)
	}
	input["n"] = 99
	_ = call.Close()
	out := call.Outputs()
	var values []any
	for {
		v, err := out.Read(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, v)
	}
	<-done
	want := []any{map[string]any{"n": json.Number("2")}, map[string]any{"n": json.Number("1")}}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("got %#v", values)
	}
	if n, _ := valueio.From(call).Scope.Usage(); n != 0 {
		t.Fatalf("retained %d", n)
	}
}

func TestGraphRetentionExhaustionTerminatesAndReleases(t *testing.T) {
	for _, kind := range []string{"pass", "buffer", "combine"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "out": {Type: "output"}}, Edges: []Edge{{From: "in", To: "out"}}}
			if kind != "pass" {
				graph.Nodes["hold"] = &Node{Type: kind}
				graph.Edges = []Edge{{From: "in", To: "hold"}, {From: "hold", To: "out"}}
			}
			call := invoke.NewInvocationImpl[any, any](ctx, invoke.WithInvocationValueLimits(invoke.ValueLimits{MaxValueUnits: 2048, MaxLiveUnits: 4096}))
			eng := newEngine(graph, invoke.NewOperationInvoker(), &invoke.BindingInvocationArgs{}, nil, newSchemaCache())
			done := make(chan struct{})
			go func() { defer close(done); eng.execute(ctx, call) }()
			writes := make(chan int, 1)
			go func() {
				n := 0
				for ; n < 1000; n++ {
					if call.Write(ctx, map[string]any{"n": n}) != nil {
						break
					}
				}
				_ = call.Close()
				writes <- n
			}()
			out := call.Outputs()
			for {
				_, err := out.Read(ctx)
				if err != nil {
					if invoke.AsInvocationError(err).Code != invoke.ErrCodeRuntime {
						t.Fatalf("terminal %v", err)
					}
					break
				}
			}
			if n := <-writes; n == 1000 {
				t.Fatal("root retention was unbounded")
			}
			<-done
			if n, _ := valueio.From(call).Scope.Usage(); n != 0 {
				t.Fatalf("retained %d after worker retirement", n)
			}
		})
	}
}

func TestGraphNumbersAndSchemaRefusal(t *testing.T) {
	for _, token := range []string{"0", "-0", "0e999999", "0.000e-90000"} {
		if isTruthy(json.Number(token)) {
			t.Fatal(token)
		}
	}
	for _, token := range []string{"1e-999999", "-0.00001", "9007199254740993"} {
		if !isTruthy(json.Number(token)) {
			t.Fatal(token)
		}
	}
	sc := newSchemaCache()
	schema := json.RawMessage(`{"type":"number","minimum":0}`)
	if ok, err := sc.match(&schema, json.Number("-1")); err != nil || ok {
		t.Fatalf("mismatch: %v %v", ok, err)
	}
	if ok, err := sc.match(&schema, json.Number("1e10001")); err == nil || ok {
		t.Fatalf("refusal collapsed to mismatch: %v %v", ok, err)
	}
	buffer := newBufferState(&Node{Until: &schema}, sc)
	if batch := buffer.add(&event{data: json.Number("1e10001")}); batch != nil || buffer.failure == nil || len(buffer.acc) != 0 {
		t.Fatal("buffer ignored schema refusal")
	}
}
