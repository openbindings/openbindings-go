package connect

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

// TestDeliveryUnitBound_UnaryOverflowRefused verifies the consumer
// delivery-unit bound: a tiny bound set via
// BindingInvocationArgs.MaxDeliveryUnitBytes refuses a ~2KB unary body with
// the lane's unchanged abstract error identity (ERR_RESPONSE_ERROR).
func TestDeliveryUnitBound_UnaryOverflowRefused(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK,
		`{"id":"abc","name":"`+strings.Repeat("x", 2048)+`"}`)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.MaxDeliveryUnitBytes = 1024
	inv := invokeWith(t, ctx, NewInvoker(), args, map[string]any{"id": "abc"})

	ierr := mustTerminalError(t, ctx, inv, invoke.ErrCodeResponseError)
	if ierr.HasData() {
		t.Errorf("native size-limit evidence crossed as abstract data: %#v", ierr.Data)
	}
}
