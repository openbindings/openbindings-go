package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pad":"` + strings.Repeat("x", 2048) + `"}`))
	}))
	t.Cleanup(srv.Close)

	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "Big API", "version": "1.0.0"},
		"servers": []map[string]any{{"url": srv.URL}},
		"paths": map[string]any{
			"/big": map[string]any{
				"get": map[string]any{
					"operationId": "big",
					"responses":   map[string]any{"200": map[string]any{"description": "OK"}},
				},
			},
		},
	}
	specBytes, _ := json.Marshal(spec)

	call := NewInvoker().InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source:               openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(string(specBytes))},
		Ref:                  "#/paths/~1big/get",
		MaxDeliveryUnitBytes: 1024,
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil {
		t.Fatal("expected an overflow error, got none")
	}
	if ierr.Code != openbindings.ErrCodeResponseError {
		t.Errorf("error code = %q, want %q", ierr.Code, openbindings.ErrCodeResponseError)
	}
	if ierr.Message != "response exceeds 1024 byte limit" {
		t.Errorf("error message = %q, want %q", ierr.Message, "response exceeds 1024 byte limit")
	}
}
