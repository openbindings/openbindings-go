package openbindings

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// recordingInvoker captures the last BindingInvocationInput it received
// and returns a fixed response. Useful for asserting which binding was selected.
type recordingInvoker struct {
	lastInput *BindingInvocationInput
	data      any
	format    string
}

func (r *recordingInvoker) Formats() []FormatInfo {
	return []FormatInfo{{Token: r.format}}
}

func (r *recordingInvoker) InvokeBinding(_ context.Context, in *BindingInvocationInput) (<-chan StreamEvent, error) {
	r.lastInput = in
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Data: r.data}
	close(ch)
	return ch, nil
}

// multiBindingInterface builds an Interface with one operation and multiple
// bindings backed by the same source. Binding keys are "op.<suffix>" where
// suffix comes from the keys map. Each binding gets a distinct ref so tests
// can verify which one was selected.
func multiBindingInterface(opKey string, bindingKeys []string) *Interface {
	bindings := make(map[string]BindingEntry, len(bindingKeys))
	for _, bk := range bindingKeys {
		bindings[bk] = BindingEntry{
			Operation: opKey,
			Source:    "api",
			Ref:       fmt.Sprintf("#/ref/%s", bk),
		}
	}
	return &Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{opKey: {}},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: bindings,
	}
}

func drainEvent(t *testing.T, ch <-chan StreamEvent) StreamEvent {
	t.Helper()
	ev, ok := <-ch
	if !ok {
		t.Fatal("channel closed without an event")
	}
	return ev
}

// --------------------------------------------------------------------------
// Tests
// --------------------------------------------------------------------------

func TestBindingSelection_DefaultSelectorPicksFirst(t *testing.T) {
	rec := &recordingInvoker{data: map[string]any{"ok": true}, format: "openapi@3.1"}
	inv := NewOperationInvoker(rec)

	// Alphabetically, "getItems.alpha" < "getItems.beta" < "getItems.gamma".
	// The default selector breaks ties alphabetically, so alpha should win.
	iface := multiBindingInterface("getItems", []string{
		"getItems.alpha",
		"getItems.beta",
		"getItems.gamma",
	})

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getItems",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := drainEvent(t, ch)
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
	if rec.lastInput == nil {
		t.Fatal("invoker was never called")
	}
	if rec.lastInput.Ref != "#/ref/getItems.alpha" {
		t.Fatalf("expected ref %q, got %q", "#/ref/getItems.alpha", rec.lastInput.Ref)
	}
}

func TestBindingSelection_BindingKeySelectsSpecificBinding(t *testing.T) {
	rec := &recordingInvoker{data: map[string]any{"ok": true}, format: "openapi@3.1"}
	inv := NewOperationInvoker(rec)

	iface := multiBindingInterface("getItems", []string{
		"getItems.alpha",
		"getItems.beta",
		"getItems.gamma",
	})

	// Explicitly request the gamma binding, bypassing the selector.
	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface:  iface,
		Operation:  "getItems",
		BindingKey: "getItems.gamma",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := drainEvent(t, ch)
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
	if rec.lastInput == nil {
		t.Fatal("invoker was never called")
	}
	if rec.lastInput.Ref != "#/ref/getItems.gamma" {
		t.Fatalf("expected ref %q, got %q", "#/ref/getItems.gamma", rec.lastInput.Ref)
	}
}

func TestBindingSelection_BindingKeyNotFoundReturnsError(t *testing.T) {
	rec := &recordingInvoker{data: map[string]any{"ok": true}, format: "openapi@3.1"}
	inv := NewOperationInvoker(rec)

	iface := multiBindingInterface("getItems", []string{"getItems.alpha"})

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface:  iface,
		Operation:  "getItems",
		BindingKey: "getItems.nonexistent",
	})
	if err != nil {
		t.Fatalf("unexpected error (wanted stream error, not call error): %v", err)
	}
	ev := drainEvent(t, ch)
	if ev.Error == nil {
		t.Fatal("expected binding_not_found error event, got data")
	}
	if ev.Error.Code != ErrCodeBindingNotFound {
		t.Fatalf("expected code %q, got %q", ErrCodeBindingNotFound, ev.Error.Code)
	}
}

func TestBindingSelection_NoBindingsReturnsError(t *testing.T) {
	rec := &recordingInvoker{data: map[string]any{"ok": true}, format: "openapi@3.1"}
	inv := NewOperationInvoker(rec)

	// Interface with an operation but zero bindings.
	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations:   map[string]Operation{"getItems": {}},
		Sources: map[string]Source{
			"api": {Format: "openapi@3.1", Location: "https://example.com/api.json"},
		},
		Bindings: map[string]BindingEntry{},
	}

	_, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getItems",
	})
	if err == nil {
		t.Fatal("expected error for operation with no bindings")
	}
	if !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("expected ErrBindingNotFound, got: %v", err)
	}
}

func TestBindingSelection_CustomSelectorIsUsed(t *testing.T) {
	rec := &recordingInvoker{data: map[string]any{"ok": true}, format: "openapi@3.1"}
	inv := NewOperationInvoker(rec)

	iface := multiBindingInterface("getItems", []string{
		"getItems.alpha",
		"getItems.beta",
	})

	// Custom selector that always picks beta.
	var selectorCalled bool
	inv.BindingSelector = func(iface *Interface, opKey string) (string, *BindingEntry, error) {
		selectorCalled = true
		b := iface.Bindings["getItems.beta"]
		return "getItems.beta", &b, nil
	}

	ch, err := inv.Invoke(context.Background(), &OperationInvocationInput{
		Interface: iface,
		Operation: "getItems",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := drainEvent(t, ch)
	if ev.Error != nil {
		t.Fatalf("unexpected error event: %v", ev.Error.Message)
	}
	if !selectorCalled {
		t.Fatal("custom binding selector was not called")
	}
	if rec.lastInput.Ref != "#/ref/getItems.beta" {
		t.Fatalf("expected ref %q, got %q", "#/ref/getItems.beta", rec.lastInput.Ref)
	}
}
