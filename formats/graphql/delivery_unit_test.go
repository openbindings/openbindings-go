package graphql

import (
	"context"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

func TestSubscriptionExclusionPrecedesDeliveryUnitHandling(t *testing.T) {
	for _, limit := range []int64{0, 1024, 64 << 10} {
		call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
			Source:               pinnedInvocationSource(t, "https://api.example.test/graphql"),
			Selector:             "subscription/updates",
			MaxDeliveryUnitBytes: limit,
			Context: map[string]any{"configuration": map[string]any{
				"document": "subscription { updates }",
			}},
		})
		outputs, invocationErr := collectInvocation(context.Background(), call, nil, false)
		if len(outputs) != 0 || invocationErr == nil || invocationErr.Code != invoke.ErrCodeInvalidSelector {
			t.Fatalf("limit %d: outputs = %#v, err = %v", limit, outputs, invocationErr)
		}
	}
}
