package graphql

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestSubscriptionExclusionPrecedesDeliveryUnitHandling(t *testing.T) {
	for _, limit := range []int64{0, 1024, 64 << 10} {
		call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
			Source:               pinnedInvocationSource(t, "https://api.example.test/graphql"),
			Ref:                  "subscription/updates",
			MaxDeliveryUnitBytes: limit,
			Context: map[string]any{"configuration": map[string]any{
				"document": "subscription { updates }",
			}},
		})
		outputs, invocationErr := collectInvocation(context.Background(), call, nil, false)
		if len(outputs) != 0 || invocationErr == nil || invocationErr.Code != openbindings.ErrCodeInvalidRef {
			t.Fatalf("limit %d: outputs = %#v, err = %v", limit, outputs, invocationErr)
		}
	}
}
