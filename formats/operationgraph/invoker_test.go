package operationgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func bg() context.Context { return context.Background() }

// drainOutputs reads an invocation's outputs to io.EOF or terminal error,
// bounded by a timeout so a stuck graph fails fast instead of hanging.
func drainOutputs(t *testing.T, call openbindings.Invocation[any, any]) ([]any, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := call.Outputs()
	var vals []any
	for {
		v, err := out.Read(ctx)
		if err == io.EOF {
			return vals, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				t.Fatal("timed out collecting outputs")
			}
			return vals, err
		}
		vals = append(vals, v)
	}
}

// codeOf asserts err is an *InvocationError and returns its code.
func codeOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %T: %v", err, err)
	}
	return ie.Code
}

// invokeGraph invokes a graph binding directly: it writes the initial input,
// closes the input side, and drains the outputs.
func invokeGraph(t *testing.T, graphJSON string, ref string, input any, invoker *openbindings.OperationInvoker) ([]any, error) {
	t.Helper()
	invoker.AddBindingInvoker(NewInvoker(invoker))
	call := invoker.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{
			Format:  FormatToken,
			Content: json.RawMessage(graphJSON),
		},
		Ref: ref,
	})
	_ = call.Write(bg(), input) // pre-errored handles reject; the drain reports
	_ = call.Close()
	return drainOutputs(t, call)
}

// invokeGraphWithTransform is invokeGraph with a transform evaluator wired in.
func invokeGraphWithTransform(t *testing.T, graphJSON string, ref string, input any, te openbindings.TransformEvaluator) ([]any, error) {
	t.Helper()
	invoker := openbindings.NewOperationInvoker()
	invoker.TransformEvaluator = te
	return invokeGraph(t, graphJSON, ref, input, invoker)
}

// ---------------------------------------------------------------------------
// Core graph semantics
// ---------------------------------------------------------------------------

func TestSimplePassthrough(t *testing.T) {
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"pass": {
				"nodes": {
					"in": { "type": "input" },
					"out": { "type": "output" }
				},
				"edges": [{ "from": "in", "to": "out" }]
			}
		}
	}`, "pass", map[string]any{"hello": "world"}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	data := vals[0].(map[string]any)
	if data["hello"] != "world" {
		t.Fatalf("expected hello=world, got %v", data["hello"])
	}
}

func TestRefNotFound(t *testing.T) {
	_, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {}
	}`, "missing", nil, openbindings.NewOperationInvoker())

	if codeOf(t, err) != openbindings.ErrCodeRefNotFound {
		t.Fatalf("expected %s, got %v", openbindings.ErrCodeRefNotFound, err)
	}
}

func TestSourceLoadFailed(t *testing.T) {
	_, err := invokeGraph(t, `{"not valid json`, "g", nil, openbindings.NewOperationInvoker())
	if codeOf(t, err) != openbindings.ErrCodeSourceLoadFailed {
		t.Fatalf("expected %s, got %v", openbindings.ErrCodeSourceLoadFailed, err)
	}
}

func TestGraphValidationFailedTerminal(t *testing.T) {
	// No input node: validation fails before any input is consumed.
	_, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"bad": {
				"nodes": { "out": { "type": "output" } },
				"edges": []
			}
		}
	}`, "bad", map[string]any{"x": 1}, openbindings.NewOperationInvoker())

	if codeOf(t, err) != openbindings.ErrCodeValidationFailed {
		t.Fatalf("expected %s, got %v", openbindings.ErrCodeValidationFailed, err)
	}
}

func TestCloseWithoutWriteRunsNilInput(t *testing.T) {
	// Closing the input side without writing seeds the graph with nil.
	invoker := openbindings.NewOperationInvoker()
	invoker.AddBindingInvoker(NewInvoker(invoker))
	call := invoker.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{
			Format: FormatToken,
			Content: json.RawMessage(`{
				"openbindings.operation-graph": "0.2.0",
				"graphs": {
					"pass": {
						"nodes": {
							"in": { "type": "input" },
							"out": { "type": "output" }
						},
						"edges": [{ "from": "in", "to": "out" }]
					}
				}
			}`),
		},
		Ref: "pass",
	})
	_ = call.Close()
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 || vals[0] != nil {
		t.Fatalf("expected one nil output, got %v", vals)
	}
}

func TestExitSuccess(t *testing.T) {
	// Graph: in -> filter (passes) -> exit, in -> out
	// The filter ensures only one path reaches exit.
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"early": {
				"nodes": {
					"in": { "type": "input" },
					"check": { "type": "filter", "schema": { "required": ["stop"] } },
					"stop": { "type": "exit" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "check" },
					{ "from": "check", "to": "stop" },
					{ "from": "in", "to": "out" }
				]
			}
		}
	}`, "early", map[string]any{"stop": true}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Exit emits the event as output and cancels. We may also get the normal
	// output event depending on timing. At minimum, one output has our data.
	hasExitEvent := false
	for _, v := range vals {
		if data, ok := v.(map[string]any); ok && data["stop"] == true {
			hasExitEvent = true
		}
	}
	if !hasExitEvent {
		t.Fatalf("expected at least one output with stop=true, got %v", vals)
	}
}

func TestExitError(t *testing.T) {
	_, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"fail": {
				"nodes": {
					"in": { "type": "input" },
					"check": { "type": "filter", "schema": { "required": ["fail"] } },
					"die": { "type": "exit", "error": true },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "check" },
					{ "from": "check", "to": "die" },
					{ "from": "in", "to": "out" }
				]
			}
		}
	}`, "fail", map[string]any{"fail": true}, openbindings.NewOperationInvoker())

	if codeOf(t, err) != openbindings.ErrCodeOperationGraphExit {
		t.Fatalf("expected %s, got %v", openbindings.ErrCodeOperationGraphExit, err)
	}
}

func TestFilterSchemaPass(t *testing.T) {
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"f": {
				"nodes": {
					"in": { "type": "input" },
					"check": { "type": "filter", "schema": { "required": ["name"] } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "check" },
					{ "from": "check", "to": "out" }
				]
			}
		}
	}`, "f", map[string]any{"name": "Alice"}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output (filter pass), got %d", len(vals))
	}
}

func TestFilterSchemaDrop(t *testing.T) {
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"f": {
				"nodes": {
					"in": { "type": "input" },
					"check": { "type": "filter", "schema": { "required": ["name"] } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "check" },
					{ "from": "check", "to": "out" }
				]
			}
		}
	}`, "f", map[string]any{"age": 30}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("expected 0 outputs (filter drop), got %d", len(vals))
	}
}

func TestOnErrorSilentDrop(t *testing.T) {
	// Transform node with no evaluator should fail. Without onError, error is dropped.
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": "x" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "t" },
					{ "from": "t", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"x": 1}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("expected 0 outputs (error silently dropped), got %d", len(vals))
	}
}

func TestOnErrorRouting(t *testing.T) {
	// Transform node fails, onError routes to output.
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": "x", "onError": "out" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "t" },
					{ "from": "t", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"x": 1}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 routed error event, got %d", len(vals))
	}
	data, ok := vals[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map error event, got %T", vals[0])
	}
	if _, hasError := data["error"]; !hasError {
		t.Fatal("expected error field in routed error event")
	}
	if _, hasInput := data["input"]; !hasInput {
		t.Fatal("expected input field in routed error event")
	}
}

// ---------------------------------------------------------------------------
// Graph validation (pure)
// ---------------------------------------------------------------------------

func TestValidation(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"in":  {Type: "input"},
			"out": {Type: "output"},
		},
		Edges: []Edge{{From: "in", To: "out"}},
	}
	if err := Validate(g, nil); err != nil {
		t.Fatalf("valid graph should pass: %v", err)
	}
}

func TestValidationNoInput(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{"out": {Type: "output"}},
		Edges: []Edge{},
	}
	if err := Validate(g, nil); err == nil {
		t.Fatal("expected error for missing input")
	}
}

func TestValidationCycleWithoutGuard(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"in":  {Type: "input"},
			"op":  {Type: "operation", Operation: "test.op"},
			"out": {Type: "output"},
		},
		Edges: []Edge{
			{From: "in", To: "op"},
			{From: "op", To: "op"},
			{From: "op", To: "out"},
		},
	}
	if err := Validate(g, nil); err == nil {
		t.Fatal("expected error for unguarded cycle")
	}
}

func TestValidationCycleWithGuard(t *testing.T) {
	maxIter := 10
	g := &Graph{
		Nodes: map[string]*Node{
			"in":  {Type: "input"},
			"op":  {Type: "operation", Operation: "test.op", MaxIterations: &maxIter},
			"out": {Type: "output"},
		},
		Edges: []Edge{
			{From: "in", To: "op"},
			{From: "op", To: "op"},
			{From: "op", To: "out"},
		},
	}
	if err := Validate(g, nil); err != nil {
		t.Fatalf("guarded cycle should pass: %v", err)
	}
}

func TestValidationOrphanNode(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"in":     {Type: "input"},
			"out":    {Type: "output"},
			"orphan": {Type: "filter", Schema: rawJSON(`{"required":["x"]}`)},
		},
		Edges: []Edge{{From: "in", To: "out"}},
	}
	if err := Validate(g, nil); err == nil {
		t.Fatal("expected error for orphan node")
	}
}

func TestValidationOnErrorReachability(t *testing.T) {
	// A node reachable only via onError should pass validation.
	g := &Graph{
		Nodes: map[string]*Node{
			"in":      {Type: "input"},
			"op":      {Type: "operation", Operation: "test.op", OnError: "handler"},
			"handler": {Type: "output"},
			"out":     {Type: "output"},
		},
		Edges: []Edge{
			{From: "in", To: "op"},
			{From: "op", To: "out"},
		},
	}
	// This should fail because there are two output nodes, but handler IS reachable.
	err := Validate(g, nil)
	if err == nil {
		t.Fatal("expected error (two output nodes)")
	}
	// Verify the error is about output count, not reachability.
	if !strings.Contains(err.Error(), "output") {
		t.Fatalf("expected output count error, got: %v", err)
	}
}

func TestValidationExitNoOutgoing(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"in":   {Type: "input"},
			"stop": {Type: "exit"},
			"out":  {Type: "output"},
			"bad":  {Type: "output"},
		},
		Edges: []Edge{
			{From: "in", To: "stop"},
			{From: "stop", To: "out"},
		},
	}
	err := Validate(g, nil)
	if err == nil {
		t.Fatal("expected error for exit with outgoing edges")
	}
}

// ---------------------------------------------------------------------------
// Transforms
// ---------------------------------------------------------------------------

// mockTransformEvaluator implements both TransformEvaluator and TransformEvaluatorWithBindings.
type mockTransformEvaluator struct{}

func (m *mockTransformEvaluator) Evaluate(expression string, data any) (any, error) {
	// Simple expression evaluator for testing: returns a field from the data.
	dataMap, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("data is not a map")
	}
	if val, exists := dataMap[expression]; exists {
		return val, nil
	}
	return nil, nil
}

func (m *mockTransformEvaluator) EvaluateWithBindings(expression string, data any, bindings map[string]any) (any, error) {
	// If expression starts with "$input.", return from bindings.
	if len(expression) > 7 && expression[:7] == "$input." {
		field := expression[7:]
		if input, ok := bindings["input"]; ok {
			if inputMap, ok := input.(map[string]any); ok {
				return inputMap[field], nil
			}
		}
		return nil, nil
	}
	return m.Evaluate(expression, data)
}

func TestTransformWithEvaluator(t *testing.T) {
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": "name" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "t" },
					{ "from": "t", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"name": "Alice", "age": 30}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	if vals[0] != "Alice" {
		t.Fatalf("expected 'Alice', got %v", vals[0])
	}
}

func TestTransformWithBindings(t *testing.T) {
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": "$input.original" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "t" },
					{ "from": "t", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"original": "from-input", "other": "data"}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	if vals[0] != "from-input" {
		t.Fatalf("expected 'from-input', got %v", vals[0])
	}
}

func TestFilterSchemaTypeValidation(t *testing.T) {
	// Tests that full JSON Schema validation works (not just required fields).
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"f": {
				"nodes": {
					"in": { "type": "input" },
					"check": { "type": "filter", "schema": { "type": "object", "properties": { "age": { "type": "number", "minimum": 18 } }, "required": ["age"] } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "check" },
					{ "from": "check", "to": "out" }
				]
			}
		}
	}`, "f", map[string]any{"age": float64(25)}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output (age >= 18 passes), got %d", len(vals))
	}
}

func TestFilterSchemaTypeValidationFail(t *testing.T) {
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"f": {
				"nodes": {
					"in": { "type": "input" },
					"check": { "type": "filter", "schema": { "type": "object", "properties": { "age": { "type": "number", "minimum": 18 } }, "required": ["age"] } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "check" },
					{ "from": "check", "to": "out" }
				]
			}
		}
	}`, "f", map[string]any{"age": float64(12)}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("expected 0 outputs (age < 18 fails), got %d", len(vals))
	}
}

func rawJSON(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

// --- Buffer tests ---

func TestBufferDrain(t *testing.T) {
	// input -> buffer -> output (no conditions, drains on completion)
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"buf": { "type": "buffer" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "buf" },
					{ "from": "buf", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"hello": "world"}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output (buffered array), got %d", len(vals))
	}
	arr, ok := vals[0].([]any)
	if !ok {
		t.Fatalf("expected array, got %T", vals[0])
	}
	if len(arr) != 1 {
		t.Fatalf("expected array of 1, got %d", len(arr))
	}
}

func TestBufferLimit(t *testing.T) {
	// input -> map (unpack 5 items) -> buffer(limit=2) -> output
	// Should produce 3 batches: [1,2], [3,4], [5]
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": "items" },
					"buf": { "type": "buffer", "limit": 2 },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "buf" },
					{ "from": "buf", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"items": []any{1, 2, 3, 4, 5}}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(vals))
	}
	// First two batches should have 2 items, last should have 1.
	for i, v := range vals {
		arr, ok := v.([]any)
		if !ok {
			t.Fatalf("output %d: expected array, got %T", i, v)
		}
		if i < 2 && len(arr) != 2 {
			t.Fatalf("output %d: expected 2 items, got %d", i, len(arr))
		}
		if i == 2 && len(arr) != 1 {
			t.Fatalf("output %d: expected 1 item, got %d", i, len(arr))
		}
	}
}

func TestBufferUntil(t *testing.T) {
	// input -> map (unpack items) -> buffer(until: {required: ["stop"]}) -> output
	// Items: [{v:1}, {v:2}, {stop:true}, {v:3}]
	// Buffer should flush [v1, v2] when stop arrives (stop is dropped), then [v3] on completion.
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": "items" },
					"buf": { "type": "buffer", "until": { "required": ["stop"] } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "buf" },
					{ "from": "buf", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{
		"items": []any{
			map[string]any{"v": 1},
			map[string]any{"v": 2},
			map[string]any{"stop": true},
			map[string]any{"v": 3},
		},
	}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(vals))
	}
	batch1, _ := vals[0].([]any)
	batch2, _ := vals[1].([]any)
	if len(batch1) != 2 {
		t.Fatalf("first batch: expected 2 items, got %d", len(batch1))
	}
	if len(batch2) != 1 {
		t.Fatalf("second batch: expected 1 item, got %d", len(batch2))
	}
}

func TestBufferThrough(t *testing.T) {
	// Same as until but with through (matching event IS included).
	// Items: [{v:1}, {v:2}, {stop:true}, {v:3}]
	// Buffer should flush [v1, v2, {stop:true}] when stop arrives, then [v3] on completion.
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": "items" },
					"buf": { "type": "buffer", "through": { "required": ["stop"] } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "buf" },
					{ "from": "buf", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{
		"items": []any{
			map[string]any{"v": 1},
			map[string]any{"v": 2},
			map[string]any{"stop": true},
			map[string]any{"v": 3},
		},
	}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(vals))
	}
	batch1, _ := vals[0].([]any)
	batch2, _ := vals[1].([]any)
	if len(batch1) != 3 {
		t.Fatalf("first batch: expected 3 items (inclusive), got %d", len(batch1))
	}
	if len(batch2) != 1 {
		t.Fatalf("second batch: expected 1 item, got %d", len(batch2))
	}
}

func TestFilterToBufferCompletion(t *testing.T) {
	// input -> filter -> buffer -> output
	// Verifies completion propagates through filter to buffer.
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"f": { "type": "filter", "schema": { "required": ["name"] } },
					"buf": { "type": "buffer" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "f" },
					{ "from": "f", "to": "buf" },
					{ "from": "buf", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"name": "Alice"}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	arr, ok := vals[0].([]any)
	if !ok {
		t.Fatalf("expected array, got %T", vals[0])
	}
	if len(arr) != 1 {
		t.Fatalf("expected array of 1, got %d", len(arr))
	}
}

// --- Combine tests ---

func TestCombineBasic(t *testing.T) {
	// input fans out to two filters, both feed into combine, combine -> output.
	// Both filters pass. Combine emits on every event from either source
	// (combineLatest semantics), so we get 2 outputs:
	//   1. First source fires: { pathA: value, pathB: null } (or vice versa)
	//   2. Second source fires: { pathA: value, pathB: value }
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"pathA": { "type": "filter", "schema": { "required": ["a"] } },
					"pathB": { "type": "filter", "schema": { "required": ["b"] } },
					"join": { "type": "combine" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "pathA" },
					{ "from": "in", "to": "pathB" },
					{ "from": "pathA", "to": "join" },
					{ "from": "pathB", "to": "join" },
					{ "from": "join", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"a": 1, "b": 2}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 outputs (combineLatest), got %d", len(vals))
	}
	// The last output should have both keys non-null.
	last := vals[len(vals)-1].(map[string]any)
	if last["pathA"] == nil {
		t.Fatal("expected pathA in last combined output")
	}
	if last["pathB"] == nil {
		t.Fatal("expected pathB in last combined output")
	}
}

func TestCombineMissingSource(t *testing.T) {
	// input fans out to two filters. Filter B drops the event (no "b" field).
	// Combine should emit with pathB = null.
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"pathA": { "type": "filter", "schema": { "required": ["a"] } },
					"pathB": { "type": "filter", "schema": { "required": ["b"] } },
					"join": { "type": "combine" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "pathA" },
					{ "from": "in", "to": "pathB" },
					{ "from": "pathA", "to": "join" },
					{ "from": "pathB", "to": "join" },
					{ "from": "join", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"a": 1}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	result, ok := vals[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", vals[0])
	}
	if result["pathA"] == nil {
		t.Fatal("expected pathA to have a value")
	}
	if result["pathB"] != nil {
		t.Fatalf("expected pathB to be nil (dropped by filter), got %v", result["pathB"])
	}
}

// --- Map tests ---

func TestMapUnpack(t *testing.T) {
	// input -> map (unpack items) -> output
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": "items" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"items": []any{"a", "b", "c"}}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 3 {
		t.Fatalf("expected 3 outputs, got %d", len(vals))
	}
	if vals[0] != "a" || vals[1] != "b" || vals[2] != "c" {
		t.Fatalf("unexpected output data: %v, %v, %v", vals[0], vals[1], vals[2])
	}
}

func TestMapNotArray(t *testing.T) {
	// input -> map (expression returns non-array) -> output
	// With onError -> output, should get a routed error event (NOT terminal).
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": "name", "onError": "out" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"name": "notanarray"}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 routed error event, got %d", len(vals))
	}
	data, ok := vals[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", vals[0])
	}
	if data["error"] != openbindings.ErrCodeMapNotArray {
		t.Fatalf("expected %s error, got %v", openbindings.ErrCodeMapNotArray, data["error"])
	}
}

// --- Transform -> buffer completion chain ---

func TestTransformToBufferCompletion(t *testing.T) {
	// input -> transform -> buffer -> output
	// Verifies completion propagates through transform to buffer.
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": "name" },
					"buf": { "type": "buffer" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "t" },
					{ "from": "t", "to": "buf" },
					{ "from": "buf", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"name": "Alice"}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	arr, ok := vals[0].([]any)
	if !ok {
		t.Fatalf("expected array, got %T", vals[0])
	}
	if len(arr) != 1 || arr[0] != "Alice" {
		t.Fatalf("expected [Alice], got %v", arr)
	}
}

// --- Map -> buffer -> combine integration ---

func TestMapBufferCombineIntegration(t *testing.T) {
	// input fans out to two map->buffer paths, combine joins them.
	// Path A unpacks "a" items, Path B unpacks "b" items.
	// Each buffer flushes once. Combine emits on each (combineLatest): 2 outputs.
	vals, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"mapA": { "type": "map", "transform": "a" },
					"mapB": { "type": "map", "transform": "b" },
					"bufA": { "type": "buffer" },
					"bufB": { "type": "buffer" },
					"join": { "type": "combine" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "mapA" },
					{ "from": "in", "to": "mapB" },
					{ "from": "mapA", "to": "bufA" },
					{ "from": "mapB", "to": "bufB" },
					{ "from": "bufA", "to": "join" },
					{ "from": "bufB", "to": "join" },
					{ "from": "join", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{
		"a": []any{1, 2},
		"b": []any{3, 4, 5},
	}, &mockTransformEvaluator{})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected 2 outputs (combineLatest), got %d", len(vals))
	}
	// Last output has both buffers.
	result, ok := vals[len(vals)-1].(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", vals[len(vals)-1])
	}
	bufAResult, _ := result["bufA"].([]any)
	bufBResult, _ := result["bufB"].([]any)
	if len(bufAResult) != 2 {
		t.Fatalf("expected bufA to have 2 items, got %d", len(bufAResult))
	}
	if len(bufBResult) != 3 {
		t.Fatalf("expected bufB to have 3 items, got %d", len(bufBResult))
	}
}

// --- Empty buffer ---

func TestBufferEmptyDrain(t *testing.T) {
	// input -> filter (drops) -> buffer -> output
	// Filter drops the event. Buffer gets no data and should flush empty (no output).
	vals, err := invokeGraph(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"f": { "type": "filter", "schema": { "required": ["nope"] } },
					"buf": { "type": "buffer" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "f" },
					{ "from": "f", "to": "buf" },
					{ "from": "buf", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"hello": "world"}, openbindings.NewOperationInvoker())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("expected 0 outputs (empty buffer), got %d", len(vals))
	}
}

// --- Event amplification limit ---

func TestEventLimitExceeded(t *testing.T) {
	// A map node unpacking more items than maxEvents trips the event budget:
	// the invocation terminates with ERR_EVENT_LIMIT_EXCEEDED after the
	// already-queued outputs drain.
	items := make([]any, maxEvents+100)
	for i := range items {
		items[i] = i
	}
	_, err := invokeGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": "items" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"items": items}, &mockTransformEvaluator{})

	if codeOf(t, err) != openbindings.ErrCodeEventLimitExceeded {
		t.Fatalf("expected %s, got %v", openbindings.ErrCodeEventLimitExceeded, err)
	}
}

// ---------------------------------------------------------------------------
// Operation nodes (sub-operation invocation through the OperationInvoker)
// ---------------------------------------------------------------------------

// mockOpBinding is a BindingInvoker for sub-operations referenced by graph
// operation nodes, built on NewInvocationImpl like the SDK's reference mock.
type mockOpBinding struct{}

func (m *mockOpBinding) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: "mock@1.0"}}
}

func (m *mockOpBinding) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go m.run(ctx, args, inv)
	return inv
}

func (m *mockOpBinding) run(ctx context.Context, args *openbindings.BindingInvocationArgs, h *openbindings.InvocationImpl[any, any]) {
	switch args.Ref {
	case "double":
		// Unary: read one input, emit {"n": n*2}.
		v, err := h.ReadInput(ctx)
		if err != nil {
			if err == io.EOF {
				h.FireError(&openbindings.InvocationError{
					Code: openbindings.ErrCodeMissingInput, Message: "double requires input",
				})
			}
			return
		}
		_ = h.CloseInput()
		n, _ := v.(map[string]any)["n"].(float64)
		if err := h.EmitOutput(map[string]any{"n": n * 2}); err != nil {
			return
		}
		h.CloseOutput()
	case "ticks":
		// Server-streaming, no input needed: close input on entry.
		_ = h.CloseInput()
		for i := 1; i <= 3; i++ {
			if err := h.EmitOutput(map[string]any{"tick": float64(i)}); err != nil {
				return
			}
		}
		h.CloseOutput()
	case "fail":
		_ = h.CloseInput()
		h.FireError(&openbindings.InvocationError{
			Code: openbindings.ErrCodeExecutionFailed, Message: "sub-operation exploded",
		})
	case "block":
		// Emits one output, then blocks until torn down (cancel/timeout).
		_ = h.CloseInput()
		if err := h.EmitOutput(map[string]any{"started": true}); err != nil {
			return
		}
		<-h.Done()
	default:
		h.FireError(&openbindings.InvocationError{
			Code: openbindings.ErrCodeRuntime, Message: "unknown ref: " + args.Ref,
		})
	}
}

// graphTestInterface is an OBI whose operations are served by mockOpBinding.
func graphTestInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"double": {},
			"ticks":  {},
			"fail":   {},
			"block":  {},
		},
		Sources: map[string]openbindings.Source{
			"mock": {Format: "mock@1.0", Location: "mem://mock"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"double.main": {Operation: "double", Source: "mock", Ref: "double"},
			"ticks.main":  {Operation: "ticks", Source: "mock", Ref: "ticks"},
			"fail.main":   {Operation: "fail", Source: "mock", Ref: "fail"},
			"block.main":  {Operation: "block", Source: "mock", Ref: "block"},
		},
	}
}

// invokeGraphWithInterface invokes a graph binding directly with an Interface
// whose sub-operations resolve through mockOpBinding.
func invokeGraphWithInterface(t *testing.T, graphJSON string, ref string, input any) ([]any, error) {
	t.Helper()
	op := openbindings.NewOperationInvoker(&mockOpBinding{})
	op.AddBindingInvoker(NewInvoker(op))
	call := op.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{
			Format:  FormatToken,
			Content: json.RawMessage(graphJSON),
		},
		Ref:       ref,
		Interface: graphTestInterface(),
	})
	_ = call.Write(bg(), input)
	_ = call.Close()
	return drainOutputs(t, call)
}

func TestOperationNodeUnary(t *testing.T) {
	vals, err := invokeGraphWithInterface(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"call": { "type": "operation", "operation": "double" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "call" },
					{ "from": "call", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"n": float64(21)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	if vals[0].(map[string]any)["n"] != float64(42) {
		t.Fatalf("expected n=42, got %v", vals[0])
	}
}

func TestOperationNodeStreamingFanout(t *testing.T) {
	// A streaming sub-operation fans each output downstream.
	vals, err := invokeGraphWithInterface(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"call": { "type": "operation", "operation": "ticks" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "call" },
					{ "from": "call", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"go": true})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 3 {
		t.Fatalf("expected 3 outputs, got %d", len(vals))
	}
	for i, v := range vals {
		if v.(map[string]any)["tick"] != float64(i+1) {
			t.Fatalf("output %d: expected tick=%d, got %v", i, i+1, v)
		}
	}
}

func TestOperationNodeErrorRoutesOnError(t *testing.T) {
	// A sub-operation's terminal error routes to onError as an error event.
	vals, err := invokeGraphWithInterface(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"call": { "type": "operation", "operation": "fail", "onError": "out" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "call" },
					{ "from": "call", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"x": 1})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 routed error event, got %d", len(vals))
	}
	data := vals[0].(map[string]any)
	msg, _ := data["error"].(string)
	if !strings.Contains(msg, "sub-operation exploded") {
		t.Fatalf("expected sub-operation error message, got %v", data)
	}
}

func TestOperationNodeErrorSilentDrop(t *testing.T) {
	// Without onError, a sub-operation failure is dropped and the graph
	// completes cleanly with no outputs.
	vals, err := invokeGraphWithInterface(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"call": { "type": "operation", "operation": "fail" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "call" },
					{ "from": "call", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"x": 1})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 0 {
		t.Fatalf("expected 0 outputs, got %v", vals)
	}
}

func TestOperationNodeTimeout(t *testing.T) {
	// The per-node timeout cancels a blocked sub-operation; the failure
	// routes through onError. The blocked binding's first output flows
	// downstream before the timeout fires.
	vals, err := invokeGraphWithInterface(t, `{
		"openbindings.operation-graph": "0.2.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"call": { "type": "operation", "operation": "block", "timeout": 50, "onError": "out" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "call" },
					{ "from": "call", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"x": 1})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 2 {
		t.Fatalf("expected started output + routed timeout error, got %v", vals)
	}
	if vals[0].(map[string]any)["started"] != true {
		t.Fatalf("expected first output started=true, got %v", vals[0])
	}
	if _, hasError := vals[1].(map[string]any)["error"]; !hasError {
		t.Fatalf("expected routed error event, got %v", vals[1])
	}
}

func TestGraphCancellationTearsDownSubOperations(t *testing.T) {
	// Cancelling the graph invocation propagates into in-flight
	// sub-operations and tears down all workers.
	op := openbindings.NewOperationInvoker(&mockOpBinding{})
	op.AddBindingInvoker(NewInvoker(op))
	call := op.InvokeBinding(bg(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{
			Format: FormatToken,
			Content: json.RawMessage(`{
				"openbindings.operation-graph": "0.2.0",
				"graphs": {
					"g": {
						"nodes": {
							"in": { "type": "input" },
							"call": { "type": "operation", "operation": "block" },
							"out": { "type": "output" }
						},
						"edges": [
							{ "from": "in", "to": "call" },
							{ "from": "call", "to": "out" }
						]
					}
				}
			}`),
		},
		Ref:       "g",
		Interface: graphTestInterface(),
	})
	if err := call.Write(bg(), map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	_ = call.Close()

	ctx, cancel := context.WithTimeout(bg(), 10*time.Second)
	defer cancel()
	out := call.Outputs()
	// The first output proves the sub-operation is in flight.
	v, err := out.Read(ctx)
	if err != nil {
		t.Fatalf("expected started output, got %v", err)
	}
	if v.(map[string]any)["started"] != true {
		t.Fatalf("expected started=true, got %v", v)
	}

	call.Cancel()
	for {
		_, err := out.Read(ctx)
		if err == nil {
			continue // a queued output drained before the terminal
		}
		if codeOf(t, err) != openbindings.ErrCodeCancelled {
			t.Fatalf("expected %s, got %v", openbindings.ErrCodeCancelled, err)
		}
		break
	}
}

// ---------------------------------------------------------------------------
// Through the operation layer (graph as the bound format)
// ---------------------------------------------------------------------------

const passGraphJSON = `{
	"openbindings.operation-graph": "0.2.0",
	"graphs": {
		"pass": {
			"nodes": {
				"in": { "type": "input" },
				"out": { "type": "output" }
			},
			"edges": [{ "from": "in", "to": "out" }]
		},
		"wrap": {
			"nodes": {
				"in": { "type": "input" },
				"call": { "type": "operation", "operation": "double" },
				"out": { "type": "output" }
			},
			"edges": [
				{ "from": "in", "to": "call" },
				{ "from": "call", "to": "out" }
			]
		}
	}
}`

// graphLayerInterface is an OBI binding operations to the graph source; the
// "double" sub-operation is served by mockOpBinding.
func graphLayerInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			// No input schema: the graph binding must close input on entry
			// and seed nil instead of reading (no-input convention).
			"runGraph": {},
			"passGraph": {
				Input: openbindings.JSONSchema{"type": "object"},
			},
			"wrapDouble": {
				Input: openbindings.JSONSchema{"type": "object"},
			},
			"double": {},
		},
		Sources: map[string]openbindings.Source{
			"graph": {Format: FormatToken, Content: passGraphJSON},
			"mock":  {Format: "mock@1.0", Location: "mem://mock"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"runGraph.main":   {Operation: "runGraph", Source: "graph", Ref: "pass"},
			"passGraph.main":  {Operation: "passGraph", Source: "graph", Ref: "pass"},
			"wrapDouble.main": {Operation: "wrapDouble", Source: "graph", Ref: "wrap"},
			"double.main":     {Operation: "double", Source: "mock", Ref: "double"},
		},
	}
}

func newGraphOperationInvoker() *openbindings.OperationInvoker {
	op := openbindings.NewOperationInvoker(&mockOpBinding{})
	op.AddBindingInvoker(NewInvoker(op))
	return op
}

func TestOperationLayerNoInputConvention(t *testing.T) {
	// A no-input operation bound to a graph must complete without the caller
	// writing anything: the binding closes input on entry and seeds nil.
	op := newGraphOperationInvoker()
	call := op.Invoke(bg(), &openbindings.OperationInvocationArgs{
		Interface: graphLayerInterface(),
		Operation: "runGraph",
	})
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 || vals[0] != nil {
		t.Fatalf("expected one nil output (nil-seeded graph), got %v", vals)
	}
}

func TestOperationLayerWithInput(t *testing.T) {
	// An operation that declares input: the graph reads the caller's first
	// message as the graph input.
	op := newGraphOperationInvoker()
	call := op.Invoke(bg(), &openbindings.OperationInvocationArgs{
		Interface: graphLayerInterface(),
		Operation: "passGraph",
	})
	if err := call.Write(bg(), map[string]any{"hello": "world"}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	if vals[0].(map[string]any)["hello"] != "world" {
		t.Fatalf("expected passthrough, got %v", vals[0])
	}
}

func TestOperationLayerEndToEnd(t *testing.T) {
	// Full stack: caller -> operation layer -> graph binding -> operation
	// node -> operation layer -> mock binding -> back up.
	op := newGraphOperationInvoker()
	call := op.Invoke(bg(), &openbindings.OperationInvocationArgs{
		Interface: graphLayerInterface(),
		Operation: "wrapDouble",
	})
	if err := call.Write(bg(), map[string]any{"n": float64(21)}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vals) != 1 {
		t.Fatalf("expected 1 output, got %d", len(vals))
	}
	if vals[0].(map[string]any)["n"] != float64(42) {
		t.Fatalf("expected n=42, got %v", vals[0])
	}
}
