package invoke

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

type migrationRecord struct {
	Name  string          `json:"name"`
	Count int64           `json:"count"`
	Tags  []string        `json:"tags"`
	Flags map[string]bool `json:"flags"`
}

func benchmarkOwnedRoundTrip[T any](b *testing.B, input T) {
	b.Helper()
	document, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"run": {Input: map[string]any{"type": "object"}, Output: map[string]any{"type": "object"}}},
		Sources: map[string]openbindings.Source{"local": {BindingSpec: "example.local@1", Content: json.RawMessage(`{}`)}}, Bindings: map[string]openbindings.BindingEntry{"run": {Operation: "run", Source: "local"}},
	})
	if err != nil {
		b.Fatal(err)
	}
	provider, err := PrepareLocalProvider(PrepareLocalProviderOptions{Key: "local", Interface: document, Implementations: map[string]LocalBindingImplementation{"run": LocalUnary(func(_ context.Context, x T) (T, error) { return x, nil })}})
	if err != nil {
		b.Fatal(err)
	}
	defer provider.Close()
	route, err := provider.CloseRealization(context.Background(), "run")
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		call := NewTypedInvocation[T, T](route.Invoke(ctx))
		if err := call.Write(ctx, input); err != nil {
			b.Fatal(err)
		}
		_ = call.Close()
		if _, err := Single(ctx, call.Outputs()); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkOwnedValueMigration(b *testing.B) {
	record := migrationRecord{"item", 7, []string{"green", "active"}, map[string]bool{"visible": true}}
	b.Run("SmallTyped", func(b *testing.B) { benchmarkOwnedRoundTrip(b, record) })
	b.Run("SmallGeneric", func(b *testing.B) {
		benchmarkOwnedRoundTrip(b, map[string]any{"name": "item", "count": json.Number("7"), "tags": []any{"green", "active"}, "flags": map[string]any{"visible": true}})
	})
	b.Run("WideTyped", func(b *testing.B) {
		records := make([]migrationRecord, 1024)
		for i := range records {
			records[i] = record
			records[i].Name = fmt.Sprintf("item-%04d", i)
		}
		benchmarkOwnedRoundTrip(b, struct {
			Records []migrationRecord `json:"records"`
		}{records})
	})
	b.Run("NativeBytes", func(b *testing.B) {
		data := make([]byte, 1<<20)
		for i := range data {
			data[i] = byte(i)
		}
		benchmarkOwnedRoundTrip(b, struct {
			Data []byte `json:"data"`
		}{data})
	})
}
