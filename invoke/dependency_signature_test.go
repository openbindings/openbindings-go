package invoke

import "testing"

func TestDependencySignatureCarriesOnlyItsKey(t *testing.T) {
	op := NewOperationSignature[struct{ ID string }, struct{ OK bool }]("deliver")
	sig := NewDependencySignatureForOperation("delivery", op)
	if got := sig.Key(); got != "delivery" {
		t.Fatalf("Key() = %q, want delivery", got)
	}
}
