package invoke

import (
	"encoding/json"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestExactCapabilityIsNotInstanceInvalidity(t *testing.T) {
	value := json.Number("1e100000000000000000000000000000000000000")
	for _, prepared := range []bool{false, true} {
		for _, position := range []string{"input", "output"} {
			t.Run(position+map[bool]string{false: "/lazy", true: "/prepared"}[prepared], func(t *testing.T) {
				op := openbindings.Operation{Input: true, Output: true}
				predicate := map[string]any{"minimum": 0}
				if position == "input" {
					op.Input = predicate
				} else {
					op.Output = predicate
				}
				iface := &openbindings.Interface{
					OpenBindings: "0.2.0",
					Operations:   map[string]openbindings.Operation{"echo": op},
					Sources:      map[string]openbindings.Source{"local": {BindingSpec: "example.prepared@1", Content: json.RawMessage(`{}`)}},
					Bindings:     map[string]openbindings.BindingEntry{"echo": {Operation: "echo", Source: "local"}},
				}
				ctx := shortCtx(t)
				engine := NewOperationInvoker(&preparedEchoBinding{})
				diagnostics := NewDiagnosticCollector(8)
				var call Invocation[any, any]
				if prepared {
					snapshot, err := openbindings.PrepareInterface(iface)
					if err != nil {
						t.Fatal(err)
					}
					binding, _ := snapshot.Binding("echo")
					behavior, err := engine.CompileRealization(ctx, snapshot, binding)
					if err != nil {
						t.Fatal(err)
					}
					call = behavior.Invoke(ctx, WithDiagnosticCollector(diagnostics))
				} else {
					call = Invoke(ctx, engine, iface, NewOperationSignature[any, any]("echo"), WithDiagnosticCollector(diagnostics))
				}
				defer call.Cancel()
				err := call.Write(ctx, value)
				if position == "output" {
					if err != nil {
						t.Fatal(err)
					}
					_, err = Single(ctx, call.Outputs())
				}
				if codeOf(t, err) != ErrCodeRuntime {
					t.Fatalf("capability failure: %v", err)
				}
				records, _ := diagnostics.Snapshot()
				if len(records) != 0 {
					t.Fatalf("capability refusal reported instance diagnostics: %#v", records)
				}
			})
		}
	}
}
