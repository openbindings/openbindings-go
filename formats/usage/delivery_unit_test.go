package usage

import (
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

// TestDeliveryUnitBound_StdoutOverflowRefused verifies the consumer
// delivery-unit bound on the invocation lane's stdout capture: a tiny bound
// set via BindingInvocationArgs.MaxDeliveryUnitBytes refuses a ~2.5KB stdout
// with the lane's unchanged abstract error identity (ERR_EXECUTION_FAILED).
func TestDeliveryUnitBound_StdoutOverflowRefused(t *testing.T) {
	words := make([]any, 300)
	for i := range words {
		words[i] = strings.Repeat("a", 8)
	}
	_, ierr := invokeUsage(t, NewInvoker(), &invoke.BindingInvocationArgs{
		Source:               testSource(),
		Selector:             "echo",
		MaxDeliveryUnitBytes: 1024,
	}, map[string]any{"words": words})
	if ierr == nil {
		t.Fatal("expected an overflow error, got none")
	}
	if ierr.Code != invoke.ErrCodeExecutionFailed {
		t.Errorf("error code = %q, want %q", ierr.Code, invoke.ErrCodeExecutionFailed)
	}
	if ierr.HasData() {
		t.Errorf("native process-size evidence crossed as abstract data: %#v", ierr.Data)
	}
}
