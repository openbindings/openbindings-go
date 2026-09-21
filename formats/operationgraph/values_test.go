package operationgraph

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// mutatingEvaluator returns the bound $input for any expression naming it,
// and for "change $input" also mutates both trees it was handed.
type mutatingEvaluator struct{ calls atomic.Int32 }

func (e *mutatingEvaluator) Evaluate(ctx context.Context, expression string, input any) (any, error) {
	return e.EvaluateWithBindings(ctx, expression, input, nil)
}
func (e *mutatingEvaluator) EvaluateWithBindings(_ context.Context, expression string, input any, bindings map[string]any) (any, error) {
	e.calls.Add(1)
	if expression == "change $input" {
		input.(map[string]any)["n"] = 2
		bindings["input"].(map[string]any)["n"] = 3
		return input, nil
	}
	return bindings["input"], nil
}
func strptr(s string) *string { return &s }

// echoBindingInvoker serves one operation that emits every write after its
// input closes, so a conduit's outputs are produced after the write events
// that rooted them are gone from the graph.
type echoBindingInvoker struct{}

const echoSpec = "echo@1"

func (echoBindingInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: echoSpec}}
}
func (e echoBindingInvoker) CheckBindingSpecs(specs []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(specs, e.BindingSpecs())
}
func (echoBindingInvoker) InvokeBinding(ctx context.Context, _ *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	inv := invoke.NewInvocationImpl[any, any](ctx)
	go func() {
		var writes []any
		for {
			v, err := inv.ReadInput(ctx)
			if err != nil {
				break
			}
			writes = append(writes, v)
		}
		for _, v := range writes {
			if inv.EmitOutput(v) != nil {
				return
			}
		}
		inv.CloseOutput()
	}()
	return inv
}

func echoInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{"echo": {}},
		Sources:      map[string]openbindings.Source{"s": {BindingSpec: echoSpec, Content: mustContent(map[string]any{})}},
		Bindings:     map[string]openbindings.BindingEntry{"echo.s": {Operation: "echo", Source: "s"}},
	}
}

// runGraph drives a graph invocation with the given writes and collects its
// outputs, returning the engine for retention assertions after completion.
func runGraph(t *testing.T, ctx context.Context, graph *Graph, invoker *invoke.OperationInvoker, args *invoke.BindingInvocationArgs, evaluator invoke.TransformEvaluator, writes []any) (*engine, []any) {
	t.Helper()
	call := invoke.NewInvocationImpl[any, any](ctx)
	eng := newEngine(graph, invoker, args, evaluator, newSchemaCache())
	done := make(chan struct{})
	go func() { defer close(done); eng.execute(ctx, call) }()
	go func() {
		for _, w := range writes {
			if err := call.Write(ctx, w); err != nil {
				break
			}
		}
		_ = call.Close()
	}()
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
	return eng, values
}

func TestGraphCallbacksCannotMutateSiblingOrRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "change": {Type: "transform", Transform: strptr("change $input")}, "root": {Type: "transform", Transform: strptr("$input")}, "out": {Type: "output"}}, Edges: []Edge{{From: "in", To: "change"}, {From: "in", To: "root"}, {From: "change", To: "out"}, {From: "root", To: "out"}}}
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
	if n := eng.liveRoots(); n != 0 {
		t.Fatalf("retained %d roots after completion", n)
	}
}

// Root values are retained only when an expression can observe $input, and
// then only while some stored event descends from them.
func TestGraphRootRetentionFollowsLiveLineage(t *testing.T) {
	writes := make([]any, 1000)
	for n := range writes {
		writes[n] = map[string]any{"n": n}
	}

	t.Run("pass-through retains nothing", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "out": {Type: "output"}}, Edges: []Edge{{From: "in", To: "out"}}}
		eng, values := runGraph(t, ctx, graph, invoke.NewOperationInvoker(), &invoke.BindingInvocationArgs{}, nil, writes)
		if eng.usesInput {
			t.Fatal("no expression references $input")
		}
		if len(values) != len(writes) {
			t.Fatalf("got %d outputs", len(values))
		}
		if eng.nextRoot != 0 || eng.liveRoots() != 0 {
			t.Fatalf("pass-through graph rooted %d lineages, %d live", eng.nextRoot, eng.liveRoots())
		}
	})

	t.Run("filter on $input drops roots with its events", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "keep": {Type: "filter", Transform: strptr("$input")}, "out": {Type: "output"}}, Edges: []Edge{{From: "in", To: "keep"}, {From: "keep", To: "out"}}}
		eng, values := runGraph(t, ctx, graph, invoke.NewOperationInvoker(), &invoke.BindingInvocationArgs{}, &mutatingEvaluator{}, writes)
		if !eng.usesInput {
			t.Fatal("filter expression references $input")
		}
		if len(values) != len(writes) {
			t.Fatalf("got %d outputs", len(values))
		}
		if eng.nextRoot != len(writes) {
			t.Fatalf("rooted %d lineages", eng.nextRoot)
		}
		if n := eng.liveRoots(); n != 0 {
			t.Fatalf("%d roots outlive their lineages", n)
		}
	})

	t.Run("conduit keeps its merged root for later outputs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "echo": {Type: "operation", Operation: "echo"}, "root": {Type: "transform", Transform: strptr("$input")}, "out": {Type: "output"}}, Edges: []Edge{{From: "in", To: "echo"}, {From: "echo", To: "root"}, {From: "root", To: "out"}}}
		invoker := invoke.NewOperationInvoker(echoBindingInvoker{})
		eng, values := runGraph(t, ctx, graph, invoker, &invoke.BindingInvocationArgs{Interface: echoInterface()}, &mutatingEvaluator{}, []any{map[string]any{"n": 1}})
		want := []any{map[string]any{"n": json.Number("1")}}
		if !reflect.DeepEqual(values, want) {
			t.Fatalf("$input undefined for a conduit output: %#v", values)
		}
		if n := eng.liveRoots(); n != 0 {
			t.Fatalf("%d roots outlive the conduit", n)
		}
	})
}

// The refcount itself: every retained copy holds one reference, markers and
// unrooted events hold none, and the last release drops the value.
func TestGraphRootReferences(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "t": {Type: "transform", Transform: strptr("$input")}, "out": {Type: "output"}}, Edges: []Edge{{From: "in", To: "t"}, {From: "t", To: "out"}}}
	eng := newEngine(graph, invoke.NewOperationInvoker(), &invoke.BindingInvocationArgs{}, nil, newSchemaCache())
	eng.ctx, eng.limits = ctx, resolvedLimits(nil)

	root, err := eng.captureEvent(ctx, map[string]any{"n": 1})
	if err != nil {
		t.Fatal(err)
	}
	eng.addRoot(root)
	if root.root == noRoot || eng.liveRoots() != 1 {
		t.Fatalf("root %d, live %d", root.root, eng.liveRoots())
	}
	a := eng.retainEvent(root)
	b := eng.retainEvent(a)
	root.release()
	root.release() // idempotent
	a.release()
	if _, defined := eng.rootPacket(b.root); !defined || eng.liveRoots() != 1 {
		t.Fatal("root dropped while a descendant is live")
	}
	// A merge over one root keeps it defined; over two roots it is undefined
	// and the batch holds nothing.
	rt := rootTracker{}
	rt.add(b.root)
	rt.add(b.root)
	if rt.merged() != b.root {
		t.Fatal("single-root merge lost its root")
	}
	rt.add(noRoot)
	if rt.merged() != noRoot {
		t.Fatal("conflicting merge kept a root")
	}
	if m := eng.retainEvent(&event{complete: true, root: b.root}); m.held || eng.liveRoots() != 1 {
		t.Fatal("a marker held a root")
	}
	if u := eng.retainEvent(&event{data: "x", source: "t"}); u.held || u.root != noRoot || u.packet == nil {
		t.Fatal("an unrooted assembled event held a root or was not admitted")
	}
	b.release()
	if _, defined := eng.rootPacket(b.root); defined || eng.liveRoots() != 0 {
		t.Fatal("last release did not drop the root")
	}

	// A conduit holds its merged root across writes and drops it on conflict.
	second, _ := eng.captureEvent(ctx, map[string]any{"n": 2})
	third, _ := eng.captureEvent(ctx, map[string]any{"n": 3})
	eng.addRoot(second)
	eng.addRoot(third)
	c := newConduitState()
	c.mergeEvent(eng, second)
	second.release()
	if _, defined := eng.rootPacket(second.root); !defined || !c.rootHeld {
		t.Fatal("conduit did not hold its merged root")
	}
	c.mergeEvent(eng, third)
	third.release()
	if c.rootHeld || eng.liveRoots() != 0 {
		t.Fatalf("conflicting conduit merge kept a root (%d live)", eng.liveRoots())
	}
	c.releaseRoot(eng) // idempotent
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
