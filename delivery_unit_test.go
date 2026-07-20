package openbindings

import (
	"context"
	"testing"
)

// TestDefaultMaxDeliveryUnitBytes pins the exported default to 10 MiB —
// byte-identical to the per-lane constants it replaced, and asserted equal
// across SDKs (the TS mirror pins the same value).
func TestDefaultMaxDeliveryUnitBytes(t *testing.T) {
	if DefaultMaxDeliveryUnitBytes != 10<<20 {
		t.Fatalf("DefaultMaxDeliveryUnitBytes = %d, want %d", DefaultMaxDeliveryUnitBytes, 10<<20)
	}
}

// TestDeliveryUnitLimit verifies the single semantics point: a positive
// value passes through; zero, negative, and a nil receiver all select the
// default (there is no unlimited sentinel).
func TestDeliveryUnitLimit(t *testing.T) {
	cases := []struct {
		name string
		args *BindingInvocationArgs
		want int64
	}{
		{"nil receiver", nil, DefaultMaxDeliveryUnitBytes},
		{"zero selects default", &BindingInvocationArgs{}, DefaultMaxDeliveryUnitBytes},
		{"negative selects default", &BindingInvocationArgs{MaxDeliveryUnitBytes: -1}, DefaultMaxDeliveryUnitBytes},
		{"positive passes through", &BindingInvocationArgs{MaxDeliveryUnitBytes: 1024}, 1024},
	}
	for _, tc := range cases {
		if got := tc.args.DeliveryUnitLimit(); got != tc.want {
			t.Errorf("%s: DeliveryUnitLimit() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestOperationInvokerStampsDeliveryUnitBound verifies the invoker-level
// policy field reaches BindingInvocationArgs on the direct binding-layer
// path (fillBindingArgs), and that a caller-set args value wins.
func TestOperationInvokerStampsDeliveryUnitBound(t *testing.T) {
	e := NewOperationInvoker()
	e.MaxDeliveryUnitBytes = 4096

	args := &BindingInvocationArgs{Source: InvocationSource{BindingSpec: "none"}}
	_ = e.InvokeBinding(context.Background(), args)
	if args.MaxDeliveryUnitBytes != 4096 {
		t.Errorf("stamped MaxDeliveryUnitBytes = %d, want 4096", args.MaxDeliveryUnitBytes)
	}

	preset := &BindingInvocationArgs{Source: InvocationSource{BindingSpec: "none"}, MaxDeliveryUnitBytes: 7}
	_ = e.InvokeBinding(context.Background(), preset)
	if preset.MaxDeliveryUnitBytes != 7 {
		t.Errorf("caller-set MaxDeliveryUnitBytes = %d, want 7 (args-level wins)", preset.MaxDeliveryUnitBytes)
	}
}
