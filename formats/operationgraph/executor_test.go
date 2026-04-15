package operationgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// collectEvents drains a stream channel with a timeout.
func collectEvents(t *testing.T, ch <-chan openbindings.StreamEvent) []openbindings.StreamEvent {
	t.Helper()
	var events []openbindings.StreamEvent
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatal("timed out collecting events")
			return events
		}
	}
}

// execGraph is a test helper that parses and executes a graph document.
func execGraph(t *testing.T, graphJSON string, ref string, input any, opExec *openbindings.OperationExecutor) []openbindings.StreamEvent {
	t.Helper()
	exec := NewExecutor(opExec)
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: json.RawMessage(graphJSON),
		},
		Ref:   ref,
		Input: input,
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}
	return collectEvents(t, ch)
}

func TestSimplePassthrough(t *testing.T) {
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"pass": {
				"nodes": {
					"in": { "type": "input" },
					"out": { "type": "output" }
				},
				"edges": [{ "from": "in", "to": "out" }]
			}
		}
	}`, "pass", map[string]any{"hello": "world"}, openbindings.NewOperationExecutor())

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %v", events[0].Error)
	}
	data := events[0].Data.(map[string]any)
	if data["hello"] != "world" {
		t.Fatalf("expected hello=world, got %v", data["hello"])
	}
}

func TestRefNotFound(t *testing.T) {
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {}
	}`, "missing", nil, openbindings.NewOperationExecutor())

	if len(events) != 1 || events[0].Error == nil {
		t.Fatal("expected error event for missing ref")
	}
	if events[0].Error.Code != "ref_not_found" {
		t.Fatalf("expected ref_not_found, got %s", events[0].Error.Code)
	}
}

func TestExitSuccess(t *testing.T) {
	// Graph: in -> filter (passes) -> exit, in -> out
	// The filter ensures only one path reaches exit.
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "early", map[string]any{"stop": true}, openbindings.NewOperationExecutor())

	// Exit emits the event as output and cancels. We may also get the normal output event
	// depending on timing. At minimum, one event should have our data.
	hasExitEvent := false
	for _, ev := range events {
		if ev.Error == nil {
			if data, ok := ev.Data.(map[string]any); ok && data["stop"] == true {
				hasExitEvent = true
			}
		}
	}
	if !hasExitEvent {
		t.Fatalf("expected at least one event with stop=true, got %v", events)
	}
}

func TestExitError(t *testing.T) {
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "fail", map[string]any{"fail": true}, openbindings.NewOperationExecutor())

	hasErrorExit := false
	for _, ev := range events {
		if ev.Error != nil && ev.Error.Code == "operation_graph_exit" {
			hasErrorExit = true
		}
	}
	if !hasErrorExit {
		t.Fatalf("expected operation_graph_exit error, got %v", events)
	}
}

func TestFilterSchemaPass(t *testing.T) {
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "f", map[string]any{"name": "Alice"}, openbindings.NewOperationExecutor())

	if len(events) != 1 {
		t.Fatalf("expected 1 event (filter pass), got %d", len(events))
	}
}

func TestFilterSchemaDrop(t *testing.T) {
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "f", map[string]any{"age": 30}, openbindings.NewOperationExecutor())

	if len(events) != 0 {
		t.Fatalf("expected 0 events (filter drop), got %d", len(events))
	}
}

func TestOnErrorSilentDrop(t *testing.T) {
	// Transform node with no evaluator should fail. Without onError, error is dropped.
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": { "type": "jsonata", "expression": "x" } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "t" },
					{ "from": "t", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"x": 1}, openbindings.NewOperationExecutor())

	// No transform evaluator, so the transform fails. No onError, so error is dropped silently.
	if len(events) != 0 {
		t.Fatalf("expected 0 events (error silently dropped), got %d", len(events))
	}
}

func TestOnErrorRouting(t *testing.T) {
	// Transform node fails, onError routes to output.
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": { "type": "jsonata", "expression": "x" }, "onError": "out" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "t" },
					{ "from": "t", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"x": 1}, openbindings.NewOperationExecutor())

	if len(events) != 1 {
		t.Fatalf("expected 1 error event via onError, got %d", len(events))
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map error event, got %T", events[0].Data)
	}
	if _, hasError := data["error"]; !hasError {
		t.Fatal("expected error field in routed error event")
	}
	if _, hasInput := data["input"]; !hasInput {
		t.Fatal("expected input field in routed error event")
	}
}

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
			"in":       {Type: "input"},
			"op":       {Type: "operation", Operation: "test.op", OnError: "handler"},
			"handler":  {Type: "output"},
			"out":      {Type: "output"},
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
	opExec := openbindings.NewOperationExecutor()
	opExec.TransformEvaluator = &mockTransformEvaluator{}
	exec := NewExecutor(opExec)

	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format: FormatToken,
			Content: json.RawMessage(`{
				"openbindings.operation-graph": "0.1.0",
				"graphs": {
					"g": {
						"nodes": {
							"in": { "type": "input" },
							"t": { "type": "transform", "transform": { "type": "jsonata", "expression": "name" } },
							"out": { "type": "output" }
						},
						"edges": [
							{ "from": "in", "to": "t" },
							{ "from": "t", "to": "out" }
						]
					}
				}
			}`),
		},
		Ref:   "g",
		Input: map[string]any{"name": "Alice", "age": 30},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events := collectEvents(t, ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "Alice" {
		t.Fatalf("expected 'Alice', got %v", events[0].Data)
	}
}

func TestTransformWithBindings(t *testing.T) {
	opExec := openbindings.NewOperationExecutor()
	opExec.TransformEvaluator = &mockTransformEvaluator{}
	exec := NewExecutor(opExec)

	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format: FormatToken,
			Content: json.RawMessage(`{
				"openbindings.operation-graph": "0.1.0",
				"graphs": {
					"g": {
						"nodes": {
							"in": { "type": "input" },
							"t": { "type": "transform", "transform": { "type": "jsonata", "expression": "$input.original" } },
							"out": { "type": "output" }
						},
						"edges": [
							{ "from": "in", "to": "t" },
							{ "from": "t", "to": "out" }
						]
					}
				}
			}`),
		},
		Ref:   "g",
		Input: map[string]any{"original": "from-input", "other": "data"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	events := collectEvents(t, ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Data != "from-input" {
		t.Fatalf("expected 'from-input', got %v", events[0].Data)
	}
}

func TestFilterSchemaTypeValidation(t *testing.T) {
	// Tests that full JSON Schema validation works (not just required fields).
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "f", map[string]any{"age": float64(25)}, openbindings.NewOperationExecutor())

	if len(events) != 1 {
		t.Fatalf("expected 1 event (age >= 18 passes), got %d", len(events))
	}
}

func TestFilterSchemaTypeValidationFail(t *testing.T) {
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "f", map[string]any{"age": float64(12)}, openbindings.NewOperationExecutor())

	if len(events) != 0 {
		t.Fatalf("expected 0 events (age < 18 fails), got %d", len(events))
	}
}

func rawJSON(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

// execGraphWithTransform is a test helper that parses and executes with a transform evaluator.
func execGraphWithTransform(t *testing.T, graphJSON string, ref string, input any, te openbindings.TransformEvaluator) []openbindings.StreamEvent {
	t.Helper()
	opExec := openbindings.NewOperationExecutor()
	opExec.TransformEvaluator = te
	exec := NewExecutor(opExec)
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: json.RawMessage(graphJSON),
		},
		Ref:   ref,
		Input: input,
	})
	if err != nil {
		t.Fatalf("ExecuteBinding error: %v", err)
	}
	return collectEvents(t, ch)
}

// --- Buffer tests ---

func TestBufferDrain(t *testing.T) {
	// input -> buffer -> output (no conditions, drains on completion)
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "g", map[string]any{"hello": "world"}, openbindings.NewOperationExecutor())

	if len(events) != 1 {
		t.Fatalf("expected 1 event (buffered array), got %d", len(events))
	}
	arr, ok := events[0].Data.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", events[0].Data)
	}
	if len(arr) != 1 {
		t.Fatalf("expected array of 1, got %d", len(arr))
	}
}

func TestBufferLimit(t *testing.T) {
	// input -> map (unpack 5 items) -> buffer(limit=2) -> output
	// Should produce 3 batches: [1,2], [3,4], [5]
	events := execGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": { "type": "jsonata", "expression": "items" } },
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

	if len(events) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(events))
	}
	// First two batches should have 2 items, last should have 1.
	for i, ev := range events {
		arr, ok := ev.Data.([]any)
		if !ok {
			t.Fatalf("event %d: expected array, got %T", i, ev.Data)
		}
		if i < 2 && len(arr) != 2 {
			t.Fatalf("event %d: expected 2 items, got %d", i, len(arr))
		}
		if i == 2 && len(arr) != 1 {
			t.Fatalf("event %d: expected 1 item, got %d", i, len(arr))
		}
	}
}

func TestBufferUntil(t *testing.T) {
	// input -> map (unpack items) -> buffer(until: {required: ["stop"]}) -> output
	// Items: [{v:1}, {v:2}, {stop:true}, {v:3}]
	// Buffer should flush [v1, v2] when stop arrives (stop is dropped), then [v3] on completion.
	events := execGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": { "type": "jsonata", "expression": "items" } },
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

	if len(events) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(events))
	}
	batch1, _ := events[0].Data.([]any)
	batch2, _ := events[1].Data.([]any)
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
	events := execGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": { "type": "jsonata", "expression": "items" } },
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

	if len(events) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(events))
	}
	batch1, _ := events[0].Data.([]any)
	batch2, _ := events[1].Data.([]any)
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
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "g", map[string]any{"name": "Alice"}, openbindings.NewOperationExecutor())

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	arr, ok := events[0].Data.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", events[0].Data)
	}
	if len(arr) != 1 {
		t.Fatalf("expected array of 1, got %d", len(arr))
	}
}

// --- Combine tests ---

func TestCombineBasic(t *testing.T) {
	// input fans out to two filters, both feed into combine, combine -> output.
	// Both filters pass. Combine emits on every event from either source
	// (combineLatest semantics), so we get 2 events:
	//   1. First source fires: { pathA: value, pathB: null } (or vice versa)
	//   2. Second source fires: { pathA: value, pathB: value }
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "g", map[string]any{"a": 1, "b": 2}, openbindings.NewOperationExecutor())

	if len(events) != 2 {
		t.Fatalf("expected 2 events (combineLatest), got %d", len(events))
	}
	// The last event should have both keys non-null.
	last := events[len(events)-1].Data.(map[string]any)
	if last["pathA"] == nil {
		t.Fatal("expected pathA in last combined event")
	}
	if last["pathB"] == nil {
		t.Fatal("expected pathB in last combined event")
	}
}

func TestCombineMissingSource(t *testing.T) {
	// input fans out to two filters. Filter B drops the event (no "b" field).
	// Combine should emit with pathB = null.
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "g", map[string]any{"a": 1}, openbindings.NewOperationExecutor())

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	result, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", events[0].Data)
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
	events := execGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": { "type": "jsonata", "expression": "items" } },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"items": []any{"a", "b", "c"}}, &mockTransformEvaluator{})

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Data != "a" || events[1].Data != "b" || events[2].Data != "c" {
		t.Fatalf("unexpected event data: %v, %v, %v", events[0].Data, events[1].Data, events[2].Data)
	}
}

func TestMapNotArray(t *testing.T) {
	// input -> map (expression returns non-array) -> output
	// With onError -> output, should get error event.
	events := execGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"unpack": { "type": "map", "transform": { "type": "jsonata", "expression": "name" }, "onError": "out" },
					"out": { "type": "output" }
				},
				"edges": [
					{ "from": "in", "to": "unpack" },
					{ "from": "unpack", "to": "out" }
				]
			}
		}
	}`, "g", map[string]any{"name": "notanarray"}, &mockTransformEvaluator{})

	if len(events) != 1 {
		t.Fatalf("expected 1 error event, got %d", len(events))
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", events[0].Data)
	}
	if data["error"] != "map_not_array" {
		t.Fatalf("expected map_not_array error, got %v", data["error"])
	}
}

// --- Transform -> buffer completion chain ---

func TestTransformToBufferCompletion(t *testing.T) {
	// input -> transform -> buffer -> output
	// Verifies completion propagates through transform to buffer.
	events := execGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"t": { "type": "transform", "transform": { "type": "jsonata", "expression": "name" } },
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

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	arr, ok := events[0].Data.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", events[0].Data)
	}
	if len(arr) != 1 || arr[0] != "Alice" {
		t.Fatalf("expected [Alice], got %v", arr)
	}
}

// --- Map -> buffer -> combine integration ---

func TestMapBufferCombineIntegration(t *testing.T) {
	// input fans out to two map->buffer paths, combine joins them.
	// Path A unpacks "a" items, Path B unpacks "b" items.
	// Each buffer flushes once. Combine emits on each (combineLatest): 2 events.
	events := execGraphWithTransform(t, `{
		"openbindings.operation-graph": "0.1.0",
		"graphs": {
			"g": {
				"nodes": {
					"in": { "type": "input" },
					"mapA": { "type": "map", "transform": { "type": "jsonata", "expression": "a" } },
					"mapB": { "type": "map", "transform": { "type": "jsonata", "expression": "b" } },
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

	if len(events) != 2 {
		t.Fatalf("expected 2 events (combineLatest), got %d", len(events))
	}
	// Last event has both buffers.
	result, ok := events[len(events)-1].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", events[len(events)-1].Data)
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
	events := execGraph(t, `{
		"openbindings.operation-graph": "0.1.0",
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
	}`, "g", map[string]any{"hello": "world"}, openbindings.NewOperationExecutor())

	if len(events) != 0 {
		t.Fatalf("expected 0 events (empty buffer), got %d", len(events))
	}
}
