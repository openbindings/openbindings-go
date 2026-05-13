package openbindings

import (
	"context"
	"strings"
	"testing"
)

// schemaValidator is shared across these tests and wired into the
// OperationInvoker via the SchemaValidator field. The SDK no longer
// ships a default; see ../schema_validator.go.


// stubInvoker returns a single event with the provided data.
type stubInvoker struct {
	data any
}

func (s *stubInvoker) Formats() []FormatInfo {
	return []FormatInfo{{Token: "openapi@3.1"}}
}

func (s *stubInvoker) InvokeBinding(_ context.Context, _ *BindingInvocationInput) (<-chan InvocationOutput, error) {
	ch := make(chan InvocationOutput, 1)
	ch <- InvocationOutput{Output: s.data}
	close(ch)
	return ch, nil
}

func testInterfaceWithInputSchema() *Interface {
	return &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"createUser": {
				Input: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"age":  map[string]any{"type": "integer"},
					},
					"required": []any{"name"},
				},
			},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"createUser.api": {
				Operation: "createUser",
				Source:    "api",
				Ref:       "#/paths/~1users/post",
			},
		},
	}
}

func testInterfaceWithOutputSchema() *Interface {
	return &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getUser": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "string"},
						"name": map[string]any{"type": "string"},
					},
					"required": []any{"id", "name"},
				},
			},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"getUser.api": {
				Operation: "getUser",
				Source:    "api",
				Ref:       "#/paths/~1users~1{id}/get",
			},
		},
	}
}

func testInterfaceWithSchemaRef() *Interface {
	return &Interface{
		OpenBindings: "0.1.0",
		Schemas: map[string]JSONSchema{
			"User": {
				"type": "object",
				"properties": map[string]any{
					"id":   map[string]any{"type": "string"},
					"name": map[string]any{"type": "string"},
				},
				"required": []any{"id", "name"},
			},
		},
		Operations: map[string]Operation{
			"getUser": {
				Input: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "string"},
					},
					"required": []any{"id"},
				},
				Output: JSONSchema{
					"$ref": "#/schemas/User",
				},
			},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"getUser.api": {
				Operation: "getUser",
				Source:    "api",
				Ref:       "#/paths/~1users~1{id}/get",
			},
		},
	}
}

func TestOBIT07_InputValidation_RejectsInvalid(t *testing.T) {
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"ok": true}})
	iface := testInterfaceWithInputSchema()

	// Missing required "name" field.
	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "createUser",
		Input:     map[string]any{"age": 25},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error == nil {
		t.Fatal("expected validation error, got data event")
	}
	if ev.Error.Code != ErrCodeValidationFailed {
		t.Fatalf("expected code %q, got %q", ErrCodeValidationFailed, ev.Error.Code)
	}
}

func TestOBIT07_InputValidation_AcceptsValid(t *testing.T) {
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"ok": true}})
	iface := testInterfaceWithInputSchema()

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "createUser",
		Input:     map[string]any{"name": "alice", "age": float64(30)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
	if ev.Output == nil {
		t.Fatal("expected data in event")
	}
}

func TestOBIT07_InputValidation_SkippedWhenNoSchema(t *testing.T) {
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"ok": true}})
	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"ping": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"ping.api": {
				Operation: "ping",
				Source:    "api",
				Ref:       "#/paths/~1ping/get",
			},
		},
	}

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "ping",
		Input:     map[string]any{"anything": "goes"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
}

func TestOBIT07_InputValidation_RejectsNilInput(t *testing.T) {
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"ok": true}})
	iface := testInterfaceWithInputSchema()

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "createUser",
		Input:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error == nil {
		t.Fatalf("expected validation error event")
	}
	if ev.Error.Code != ErrCodeValidationFailed {
		t.Fatalf("error code = %q, want %q", ev.Error.Code, ErrCodeValidationFailed)
	}
}

func TestOBIT08_OutputValidation_YieldsDataWithError(t *testing.T) {
	// The stub returns data that doesn't match the output schema. The SDK
	// yields the underlying response alongside the error so callers may
	// inspect or render it (e.g. UI debugger).
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"invalid": true}})
	iface := testInterfaceWithOutputSchema()

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getUser",
		Input:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error == nil {
		t.Fatal("expected validation error for invalid output")
	}
	if ev.Error.Code != ErrCodeValidationFailed {
		t.Fatalf("expected code %q, got %q", ErrCodeValidationFailed, ev.Error.Code)
	}
	gotData, ok := ev.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected data to be surfaced alongside the validation error, got %#v", ev.Output)
	}
	if gotData["invalid"] != true {
		t.Errorf("expected data {invalid:true}, got %#v", gotData)
	}
}

func TestOBIT08_OutputValidation_AcceptsValid(t *testing.T) {
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"id": "1", "name": "alice"}})
	iface := testInterfaceWithOutputSchema()

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getUser",
		Input:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
	if ev.Output == nil {
		t.Fatal("expected data in event")
	}
}

func TestOBIT08_OutputValidation_SkippedWhenNoSchema(t *testing.T) {
	// Operation has no output schema, any data should pass through.
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"anything": true}})
	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"ping": {},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"ping.api": {
				Operation: "ping",
				Source:    "api",
				Ref:       "#/paths/~1ping/get",
			},
		},
	}

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "ping",
		Input:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
}

func TestOBIT08_OutputValidation_WithSchemaRef(t *testing.T) {
	// Output schema uses $ref to #/schemas/User.
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"id": "1", "name": "alice"}})
	iface := testInterfaceWithSchemaRef()

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getUser",
		Input:     map[string]any{"id": "1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
}

func TestOBIT08_OutputValidation_WithSchemaRef_YieldsDataWithError(t *testing.T) {
	// Output doesn't match the $ref'd schema. Data is still surfaced.
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"missing": "fields"}})
	iface := testInterfaceWithSchemaRef()

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getUser",
		Input:     map[string]any{"id": "1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error == nil {
		t.Fatal("expected validation error for invalid output")
	}
	if ev.Error.Code != ErrCodeValidationFailed {
		t.Fatalf("expected code %q, got %q", ErrCodeValidationFailed, ev.Error.Code)
	}
	gotData, ok := ev.Output.(map[string]any)
	if !ok || gotData["missing"] != "fields" {
		t.Errorf("expected data to be surfaced as {missing:\"fields\"}, got %#v", ev.Output)
	}
}

type stubTransformEvaluator struct {
	fn func(expression string, data any) (any, error)
}

func (s *stubTransformEvaluator) Evaluate(expression string, data any) (any, error) {
	return s.fn(expression, data)
}

func TestOBIT08_OutputValidation_AfterTransform(t *testing.T) {
	// The raw output is invalid, but the transform makes it valid.
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"raw": true}})
	inv.TransformEvaluator = &stubTransformEvaluator{
		fn: func(_ string, _ any) (any, error) {
			return map[string]any{"id": "1", "name": "alice"}, nil
		},
	}
	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getUser": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "string"},
						"name": map[string]any{"type": "string"},
					},
					"required": []any{"id", "name"},
				},
			},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"getUser.api": {
				Operation:       "getUser",
				Source:          "api",
				Ref:             "#/paths/~1users~1{id}/get",
				OutputTransform: &TransformOrRef{Inline: "transform-expr"},
			},
		},
	}

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getUser",
		Input:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
	if ev.Output == nil {
		t.Fatal("expected data in event")
	}
}

func TestOBIT08_OutputValidation_AfterTransform_YieldsDataWithError(t *testing.T) {
	// The transform produces invalid output. The post-transform value is what
	// the SDK validated, so it's also what we yield alongside the error.
	inv := NewOperationInvoker(&stubInvoker{data: map[string]any{"raw": true}})
	inv.TransformEvaluator = &stubTransformEvaluator{
		fn: func(_ string, _ any) (any, error) {
			return map[string]any{"wrong": "shape"}, nil
		},
	}
	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getUser": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "string"},
						"name": map[string]any{"type": "string"},
					},
					"required": []any{"id", "name"},
				},
			},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"getUser.api": {
				Operation:       "getUser",
				Source:          "api",
				Ref:             "#/paths/~1users~1{id}/get",
				OutputTransform: &TransformOrRef{Inline: "transform-expr"},
			},
		},
	}

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getUser",
		Input:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error == nil {
		t.Fatal("expected validation error for invalid transformed output")
	}
	if ev.Error.Code != ErrCodeValidationFailed {
		t.Fatalf("expected code %q, got %q", ErrCodeValidationFailed, ev.Error.Code)
	}
	gotData, ok := ev.Output.(map[string]any)
	if !ok || gotData["wrong"] != "shape" {
		t.Errorf("expected data to be post-transform {wrong:\"shape\"}, got %#v", ev.Output)
	}
}

func TestOBIT08_OutputValidation_PokeAPIStyleNullableMismatch(t *testing.T) {
	// The schema declares { type: "string" } for `next`, but the server sent
	// `null` (the original PokéAPI ability_list case). The SDK surfaces both
	// the data the caller might want to render AND the diagnostic explaining
	// why it doesn't match the declared contract.
	inv := NewOperationInvoker(&stubInvoker{
		data: map[string]any{"count": 2, "next": nil, "results": []any{}},
	})
	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"abilityList": {
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"count":   map[string]any{"type": "integer"},
						"next":    map[string]any{"type": "string"},
						"results": map[string]any{"type": "array"},
					},
					"required": []any{"count", "next", "results"},
				},
			},
		},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{
			"abilityList.api": {
				Operation: "abilityList",
				Source:    "api",
				Ref:       "#/paths/~1ability/get",
			},
		},
	}

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "abilityList",
		Input:     nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := <-ch
	if ev.Error == nil {
		t.Fatal("expected validation error for null next")
	}
	if ev.Error.Code != ErrCodeValidationFailed {
		t.Fatalf("expected code %q, got %q", ErrCodeValidationFailed, ev.Error.Code)
	}
	if !strings.Contains(ev.Error.Message, "output validation failed") {
		t.Errorf("expected error message about output validation, got %q", ev.Error.Message)
	}
	data, ok := ev.Output.(map[string]any)
	if !ok {
		t.Fatalf("expected data to be surfaced as a map, got %#v", ev.Output)
	}
	if data["next"] != nil || data["count"] != 2 {
		t.Errorf("expected data {count:2, next:nil, results:[]}, got %#v", data)
	}
	// Structured failures let consumers render per-field diagnostics
	// without parsing the human-readable message string.
	details, ok := ev.Error.Details.(ValidationFailureDetails)
	if !ok {
		t.Fatalf("expected Details to be ValidationFailureDetails, got %T", ev.Error.Details)
	}
	if len(details.Failures) == 0 {
		t.Fatal("expected at least one failure in Details.Failures")
	}
	foundNext := false
	for _, f := range details.Failures {
		if f.Path == "/next" {
			foundNext = true
			if !strings.Contains(strings.ToLower(f.Message), "null") &&
				!strings.Contains(strings.ToLower(f.Message), "string") {
				t.Errorf("failure for /next should mention null or string, got %q", f.Message)
			}
		}
	}
	if !foundNext {
		t.Errorf("expected a failure at path /next, got: %+v", details.Failures)
	}
}
