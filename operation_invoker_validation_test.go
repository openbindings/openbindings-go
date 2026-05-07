package openbindings

import (
	"context"
	"testing"
)

// stubInvoker returns a single event with the provided data.
type stubInvoker struct {
	data any
}

func (s *stubInvoker) Formats() []FormatInfo {
	return []FormatInfo{{Token: "openapi@3.1"}}
}

func (s *stubInvoker) InvokeBinding(_ context.Context, _ *BindingInvocationInput) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Data: s.data}
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
	if ev.Data == nil {
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

func TestOBIT08_OutputValidation_RejectsInvalid(t *testing.T) {
	// The stub returns data that doesn't match the output schema.
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
	if ev.Data == nil {
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

func TestOBIT08_OutputValidation_WithSchemaRef_RejectsInvalid(t *testing.T) {
	// Output doesn't match the $ref'd schema.
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
	if ev.Data == nil {
		t.Fatal("expected data in event")
	}
}

func TestOBIT08_OutputValidation_AfterTransform_RejectsInvalid(t *testing.T) {
	// The transform produces invalid output.
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
}
