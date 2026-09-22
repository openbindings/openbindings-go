package operationgraph

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

func BenchmarkGraphValueMigration(b *testing.B) {
	for _, width := range []int{1, 8} {
		b.Run(fmt.Sprintf("Fanout%d", width), func(b *testing.B) {
			one := 1
			graph := &Graph{Nodes: map[string]*Node{"in": {Type: "input"}, "out": {Type: "output"}}}
			for n := 0; n < width; n++ {
				key := fmt.Sprint(n)
				graph.Nodes[key] = &Node{Type: "buffer", Limit: &one}
				graph.Edges = append(graph.Edges, Edge{From: "in", To: key}, Edge{From: key, To: "out"})
			}
			input := map[string]any{"name": "item", "flags": []any{true, false}, "id": "9007199254740993"}
			op := invoke.NewOperationInvoker()
			schemas := newSchemaCache()
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				call := invoke.NewInvocationImpl[any, any](ctx)
				eng := newEngine(graph, op, &invoke.BindingInvocationArgs{}, nil, schemas)
				done := make(chan struct{})
				go func() { defer close(done); eng.execute(ctx, call) }()
				if err := call.Write(ctx, input); err != nil {
					b.Fatal(err)
				}
				_ = call.Close()
				out := call.Outputs()
				outputs := 0
				for {
					_, err := out.Read(ctx)
					if err == io.EOF {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
					outputs++
				}
				<-done
				if outputs != width {
					b.Fatal(outputs)
				}
			}
		})
	}
}
