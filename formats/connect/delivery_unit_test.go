package connect

import (
	"net/http"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// TestDeliveryUnitBound_UnaryOverflowRefused verifies the consumer
// delivery-unit bound: a tiny bound set via
// BindingInvocationArgs.MaxDeliveryUnitBytes refuses a ~2KB unary body with
// the lane's unchanged error identity (ERR_RESPONSE_ERROR, same message
// template with the dynamic value).
func TestDeliveryUnitBound_UnaryOverflowRefused(t *testing.T) {
	ctx := testContext(t)
	srv := fakeConnectServer(t, http.StatusOK,
		`{"id":"abc","name":"`+strings.Repeat("x", 2048)+`"}`)

	args := unaryArgs(srv.URL, testProto, "testpkg.TestService/GetItem")
	args.MaxDeliveryUnitBytes = 1024
	inv := invokeWith(t, ctx, NewInvoker(), args, map[string]any{"id": "abc"})

	ierr := mustTerminalError(t, ctx, inv, openbindings.ErrCodeResponseError)
	if ierr.Message != "response exceeds 1024 byte limit" {
		t.Errorf("error message = %q, want %q", ierr.Message, "response exceeds 1024 byte limit")
	}
}
