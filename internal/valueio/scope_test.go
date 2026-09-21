package valueio_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
)

func TestOnlyIndependentPublicDrainCanRelievePressure(t *testing.T) {
	scope, _ := valueio.NewScope(valueio.Limits{MaxLiveUnits: 256})
	retained := scope.NewReservation()
	if err := retained.Adjust(context.Background(), nil, 200); err != nil {
		t.Fatal(err)
	}
	next := scope.NewReservation()
	var limit *value.LimitError
	if err := next.Adjust(context.Background(), nil, 100); !errors.As(err, &limit) {
		t.Fatalf("persistent retention should fail: %v", err)
	}
	retained.MarkDrainable()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- next.Adjust(ctx, nil, 100) }()
	select {
	case err := <-result:
		t.Fatalf("expected backpressure, got %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	retained.Release()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	next.Release()
	if live, drain := scope.Usage(); live != 0 || drain != 0 {
		t.Fatalf("leaked %d/%d", live, drain)
	}
}
func TestCaptureFailureAndCancellationRelease(t *testing.T) {
	scope, _ := valueio.NewScope(valueio.Limits{MaxValueUnits: 512, MaxLiveUnits: 1024})
	if _, err := valueio.Capture(context.Background(), nil, scope, make([]any, 30)); err == nil {
		t.Fatal("oversized value accepted")
	}
	if live, _ := scope.Usage(); live != 0 {
		t.Fatal(live)
	}
	p, err := valueio.Capture(context.Background(), nil, scope, map[string]any{"x": "y"})
	if err != nil {
		t.Fatal(err)
	}
	copy, err := p.Retain(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Release()
	if live, _ := scope.Usage(); live != copy.Snapshot.Cost() {
		t.Fatal(live)
	}
	copy.Release()
}
func TestPublicRootDoesNotInheritAmbientScope(t *testing.T) {
	scope, _ := valueio.NewScope(valueio.Limits{})
	ctx := valueio.WithScope(context.Background(), scope)
	if valueio.ScopeFrom(valueio.RootContext(ctx)) != nil {
		t.Fatal("public call inherited scope")
	}
	if valueio.ScopeFrom(valueio.RootContext(valueio.ChildContext(ctx))) != scope {
		t.Fatal("SDK child lost shared scope")
	}
}

type access = valueio.Access
type facade struct{ access }
type foreign struct{ *facade }

func TestPrivateAccessRejectsForeignWrapper(t *testing.T) {
	own := &facade{}
	ep := &valueio.Endpoint{}
	own.access = valueio.NewAccess(own, ep)
	if valueio.From(own) != ep {
		t.Fatal("owned endpoint unavailable")
	}
	if valueio.From(&foreign{own}) != nil {
		t.Fatal("foreign wrapper inherited bypass")
	}
}
