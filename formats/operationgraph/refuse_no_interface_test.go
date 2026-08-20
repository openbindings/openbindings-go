package operationgraph

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// TestInvokeBinding_OperationNodeWithoutInterfaceRefused pins C3f / OG-V-11:
// `operation` and `each` nodes resolve ONLY against the containing OBI's
// operations map (§281). A direct binding invocation supplies no interface —
// hence no operations map — so a graph carrying operation/each nodes is
// unexecutable in that mode and MUST be refused pre-execution with
// ERR_VALIDATION_FAILED, not passed an absent interface downstream into a
// confusing "no operation resolved" error.
func TestInvokeBinding_OperationNodeWithoutInterfaceRefused(t *testing.T) {
	graphDoc := `{"graphs":{"g":{
		"openbindings.operation-graph":"0.2.0",
		"nodes":{
			"in":{"type":"input"},
			"call":{"type":"operation","operation":"items.fetch"},
			"out":{"type":"output"}
		},
		"edges":[{"from":"in","to":"call"},{"from":"call","to":"out"}]
	}}}`

	inv := NewInvoker(invoke.NewOperationInvoker()).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(graphDoc)},
		Ref:    "#/graphs/g",
		// No Interface: a direct binding invocation supplies no operations map.
	})
	_ = inv.Write(context.Background(), map[string]any{})
	_ = inv.Close()

	_, err := invoke.Single[any](context.Background(), inv.Outputs())
	ierr := invoke.AsInvocationError(err)
	if ierr == nil {
		t.Fatalf("expected a pre-execution refusal, got %v", err)
	}
	if ierr.Code != invoke.ErrCodeValidationFailed {
		t.Fatalf("want ERR_VALIDATION_FAILED (OG-V-11), got %s: %s", ierr.Code, ierr.Error())
	}
}

// TestInvokeBinding_PureTransformGraphRunsWithoutInterface is the companion
// positive: a graph with no operation/each nodes validates vacuously and runs
// as a direct binding, so the refusal is scoped to graphs that actually need
// the operations map.
func TestInvokeBinding_PureTransformGraphRunsWithoutInterface(t *testing.T) {
	graphDoc := `{"graphs":{"g":{
		"openbindings.operation-graph":"0.2.0",
		"nodes":{"in":{"type":"input"},"out":{"type":"output"}},
		"edges":[{"from":"in","to":"out"}]
	}}}`
	inv := NewInvoker(invoke.NewOperationInvoker()).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(graphDoc)},
		Ref:    "#/graphs/g",
	})
	if err := inv.Write(context.Background(), map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	_ = inv.Close()
	v, err := invoke.Single[any](context.Background(), inv.Outputs())
	if err != nil {
		t.Fatalf("pure pass-through graph must run without an interface, got %v", err)
	}
	if m, _ := v.(map[string]any); m["ok"] != true {
		t.Fatalf("expected the input to flow through, got %v", v)
	}
}
