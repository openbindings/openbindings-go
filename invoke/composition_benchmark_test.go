package invoke

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func benchmarkInterface(operationCount int, dependency bool) *openbindings.Interface {
	operations := make(map[string]openbindings.Operation, operationCount)
	dependencies := make(map[string]openbindings.DependencyEntry)
	bindings := make(map[string]openbindings.BindingEntry, operationCount)
	for index := 0; index < operationCount; index++ {
		key := fmt.Sprintf("operation.%05d", index)
		operations[key] = openbindings.Operation{
			Input:  map[string]any{"type": "object"},
			Output: map[string]any{"type": "object"},
		}
		bindings[key] = openbindings.BindingEntry{Operation: key, Source: "local"}
		if dependency {
			dependencies[key] = openbindings.DependencyEntry{Operation: key}
		}
	}
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   operations,
		Dependencies: dependencies,
		Sources: map[string]openbindings.Source{
			"local": {BindingSpec: "example.local@1", Content: json.RawMessage(`{}`)},
		},
		Bindings: bindings,
	}
}

func BenchmarkPrepareInterface(b *testing.B) {
	for _, size := range []int{1, 1000, 5000} {
		b.Run(fmt.Sprintf("operations_%d", size), func(b *testing.B) {
			document := benchmarkInterface(size, true)
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				if _, err := openbindings.PrepareInterface(document); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkLocalRoute(b *testing.B) (*CompositionSession, *PreparedDependencyRoute[any, any], *PreparedRealization) {
	b.Helper()
	providerDocument, err := openbindings.PrepareInterface(benchmarkInterface(1, false))
	if err != nil {
		b.Fatal(err)
	}
	provider, err := PrepareLocalProvider(PrepareLocalProviderOptions{
		Key:       "local",
		Interface: providerDocument,
		Implementations: map[string]LocalBindingImplementation{
			"operation.00000": LocalUnary(func(_ context.Context, input map[string]any) (map[string]any, error) {
				return input, nil
			}),
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	consumer, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"operation.00000": {Input: map[string]any{"type": "object"}, Output: map[string]any{"type": "object"}},
		},
		Dependencies: map[string]openbindings.DependencyEntry{
			"dependency": {Operation: "operation.00000"},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	session, err := NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: provider}},
	})
	if err != nil {
		b.Fatal(err)
	}
	resolution, err := ResolveDependency(context.Background(), session, NewDynamicDependencySignature("dependency"))
	if err != nil || resolution.Status != DependencyAvailable {
		b.Fatalf("resolution=%#v err=%v", resolution, err)
	}
	realization, err := provider.CloseRealization(context.Background(), "operation.00000")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = provider.Close() })
	return session, resolution.Route, realization
}

func BenchmarkPreparedLocalRoute(b *testing.B) {
	_, route, realization := benchmarkLocalRoute(b)
	run := func(b *testing.B, invoke func() Invocation[any, any]) {
		b.Helper()
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			call := invoke()
			input := map[string]any{"index": float64(index)}
			if err := call.Write(context.Background(), input); err != nil {
				b.Fatal(err)
			}
			if _, err := call.Outputs().Read(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("direct_realization", func(b *testing.B) {
		run(b, func() Invocation[any, any] { return realization.Invoke(context.Background()) })
	})
	b.Run("dependency_route", func(b *testing.B) {
		run(b, func() Invocation[any, any] { return route.Invoke(context.Background()) })
	})
}

func BenchmarkWarmDependencyResolution(b *testing.B) {
	session, _, _ := benchmarkLocalRoute(b)
	signature := NewDynamicDependencySignature("dependency")
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := ResolveDependency(context.Background(), session, signature); err != nil {
			b.Fatal(err)
		}
	}
}
