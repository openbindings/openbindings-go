package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func localProviderDocument(t *testing.T) *openbindings.PreparedInterface {
	t.Helper()
	prepared, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"create": {
				Input:  map[string]any{"type": "object", "required": []any{"title"}, "properties": map[string]any{"title": map[string]any{"type": "string"}}},
				Output: map[string]any{"type": "object", "required": []any{"id"}, "properties": map[string]any{"id": map[string]any{"type": "string"}}},
			},
		},
		Sources: map[string]openbindings.Source{
			"local": {BindingSpec: "example.local@1", Content: json.RawMessage(`{}`)},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"create.local": {Operation: "create", Source: "local"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestLocalProviderUsesVerifiedRouteAndPreservesNativeValues(t *testing.T) {
	providerDocument := localProviderDocument(t)
	var received map[string]any
	provider, err := PrepareLocalProvider(PrepareLocalProviderOptions{
		Key:       "local-tasks",
		Interface: providerDocument,
		Implementations: map[string]LocalBindingImplementation{
			"create.local": LocalUnary(func(_ context.Context, input map[string]any) (map[string]any, error) {
				received = input
				return map[string]any{"id": "task_1"}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   providerDocument.InterfaceSnapshot().Operations,
		Dependencies: map[string]openbindings.DependencyEntry{
			"creation": {Operation: "create", BindingSpecs: []string{"example.local@1"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ResolveDependency(
		shortCtx(t), session,
		NewDependencySignatureForOperation("creation", NewOperationSignature[map[string]any, map[string]any]("create")),
	)
	if err != nil || result.Status != DependencyAvailable {
		t.Fatalf("resolution=%#v err=%v", result, err)
	}
	input := map[string]any{"title": "Ship it"}
	call := result.Route.Invoke(shortCtx(t))
	if err := call.Write(shortCtx(t), input); err != nil {
		t.Fatal(err)
	}
	output, err := Single(shortCtx(t), call.Outputs())
	if err != nil || output["id"] != "task_1" {
		t.Fatalf("output=%#v err=%v", output, err)
	}
	received["same-reference"] = true
	if input["same-reference"] != true {
		t.Fatal("local input reference was cloned")
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	disposed := result.Route.Invoke(shortCtx(t))
	_, err = disposed.Outputs().Read(shortCtx(t))
	var invocationErr *InvocationError
	if !errors.As(err, &invocationErr) || invocationErr.Code != ErrCodeRuntime {
		t.Fatalf("disposed route error = %v", err)
	}
}

func TestLocalUnaryRejectsMissingInput(t *testing.T) {
	provider, err := PrepareLocalProvider(PrepareLocalProviderOptions{
		Key:       "local",
		Interface: localProviderDocument(t),
		Implementations: map[string]LocalBindingImplementation{
			"create.local": LocalUnary(func(context.Context, map[string]any) (map[string]any, error) {
				return map[string]any{"id": "unreachable"}, nil
			}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	realization, err := provider.CloseRealization(shortCtx(t), "create.local")
	if err != nil {
		t.Fatal(err)
	}
	call := realization.Invoke(shortCtx(t))
	_ = call.Close()
	_, err = call.Outputs().Read(shortCtx(t))
	var invocationErr *InvocationError
	if !errors.As(err, &invocationErr) || invocationErr.Code != ErrCodeMissingInput {
		t.Fatalf("missing input error = %v", err)
	}
}
