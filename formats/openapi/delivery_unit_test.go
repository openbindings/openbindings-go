package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestAdapterErrorBoundary_NetworkFailureIsCodeOnly(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: connection refused")
	})}
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{}}}}}}}}`
	call := NewInvokerWithClient(client).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(spec)},
		Selector: "#/paths/~1x/get",
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil {
		t.Fatal("expected connection failure")
	}
	if ierr.Code != invoke.ErrCodeConnectFailed {
		t.Fatalf("error code = %q, want %q", ierr.Code, invoke.ErrCodeConnectFailed)
	}
	if ierr.HasData() {
		t.Fatalf("native network evidence crossed as abstract data: %#v", ierr.Data)
	}
}

// TestDeliveryUnitBound_UnaryOverflowRefused verifies the consumer
// delivery-unit bound: a tiny bound set via
// BindingInvocationArgs.MaxDeliveryUnitBytes refuses a ~2KB unary body with
// the lane's unchanged abstract error identity.
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

	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:               invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(string(specBytes))},
		Selector:             "#/paths/~1big/get",
		MaxDeliveryUnitBytes: 1024,
	})
	_, ierr := driveSingle(t, call, nil)
	if ierr == nil {
		t.Fatal("expected an overflow error, got none")
	}
	if ierr.Code != invoke.ErrCodeResponseError {
		t.Errorf("error code = %q, want %q", ierr.Code, invoke.ErrCodeResponseError)
	}
	if ierr.HasData() {
		t.Errorf("native size-limit evidence crossed as abstract data: %#v", ierr.Data)
	}
}

// TestDeliveryUnitBound_SSEPerEventNotCumulative verifies the SSE bound
// applies per event, never cumulatively (each event is one delivery unit;
// mirrors asyncapi's TestSSEReceiveCapIsPerEvent): events well over 1 KiB
// each — and more than the 10 MiB default in total — keep flowing under
// the default bound.
func TestDeliveryUnitBound_SSEPerEventNotCumulative(t *testing.T) {
	const eventSize = 2 * 1024 * 1024 // 2 MiB per event, 6 events = 12 MiB total
	payload := strings.Repeat("x", eventSize)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 6; i++ {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(sseSpec(srv.URL))},
		Selector: "#/paths/~1events/get",
	})
	vals, ierr := driveOutputs(context.Background(), call, nil)
	if ierr != nil {
		t.Fatalf("a >10MiB cumulative stream of under-bound events must not error: %v", ierr)
	}
	if len(vals) != 6 {
		t.Fatalf("expected 6 events, got %d", len(vals))
	}
}

// TestDeliveryUnitBound_SSETinyBoundRefusesLoudly verifies the consumer
// delivery-unit bound is live on the SSE lane: with a 1 KiB bound set via
// BindingInvocationArgs.MaxDeliveryUnitBytes, a ~4 KiB event refuses loudly
// with the delivery-unit overflow identity (ERR_RESPONSE_ERROR, the same
// message template as asyncapi's per-event cap).
func TestDeliveryUnitBound_SSETinyBoundRefusesLoudly(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", payload)
	}))
	t.Cleanup(srv.Close)

	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:               invoke.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(sseSpec(srv.URL))},
		Selector:             "#/paths/~1events/get",
		MaxDeliveryUnitBytes: 1024,
	})
	vals, ierr := driveOutputs(context.Background(), call, nil)
	if len(vals) != 0 {
		t.Fatalf("expected no outputs, got %d", len(vals))
	}
	if ierr == nil {
		t.Fatal("expected an overflow error, got none")
	}
	if ierr.Code != invoke.ErrCodeResponseError {
		t.Errorf("error code = %q, want %q", ierr.Code, invoke.ErrCodeResponseError)
	}
	if ierr.Error() != invoke.ErrCodeResponseError {
		t.Errorf("error text = %q, want the abstract code", ierr.Error())
	}
}
