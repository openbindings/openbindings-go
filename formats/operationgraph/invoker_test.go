package operationgraph

import (
	"context"
	"path/filepath"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Creation is inert: InvokeBinding returns the handle synchronously and
// preflight failures (here: an unloadable source) surface as a terminal
// error THROUGH the handle, never as a synchronous load before the handle
// exists (the load may be a network fetch).
func TestInvokeBinding_PreflightErrorsThroughHandle(t *testing.T) {
	inv := NewInvoker(openbindings.NewOperationInvoker()).InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Location: filepath.Join(t.TempDir(), "missing.json")},
		Ref:    "#/graphs/g",
	})
	if inv == nil {
		t.Fatal("expected a handle")
	}
	_, err := openbindings.Single[any](context.Background(), inv.Outputs())
	if err == nil {
		t.Fatal("expected the preflight failure as a terminal error")
	}
	ierr := openbindings.AsInvocationError(err)
	if ierr == nil || ierr.Code != openbindings.ErrCodeSourceLoadFailed {
		t.Fatalf("want %s through the handle, got %v", openbindings.ErrCodeSourceLoadFailed, err)
	}
}
