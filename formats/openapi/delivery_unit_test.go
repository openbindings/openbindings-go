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
		Source:   invoke.InvocationSource{BindingSpec: bindingSpecForTestDocument(spec), Content: openbindings.TextContent(spec)},
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
					"responses": map[string]any{"200": map[string]any{
						"description": "OK",
						"content": map[string]any{
							"application/json": map[string]any{"schema": map[string]any{"type": "object"}},
						},
					}},
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
	if ierr.Code != invoke.ErrCodeExecutionFailed {
		t.Errorf("error code = %q, want %q", ierr.Code, invoke.ErrCodeExecutionFailed)
	}
	if ierr.HasData() {
		t.Errorf("native size-limit evidence crossed as abstract data: %#v", ierr.Data)
	}
}

// An SSE response is one HTTP delivery unit. Event delimiters do not split the
// operation boundary, so a body over the default bound refuses as a whole.
func TestDeliveryUnitBound_SSEIsOneCumulativeUnit(t *testing.T) {
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
	if ierr == nil || len(vals) != 0 {
		t.Fatalf("oversized unary SSE = outputs %d, error %v", len(vals), ierr)
	}
	if ierr.Code != invoke.ErrCodeExecutionFailed {
		t.Fatalf("error code = %q, want %q", ierr.Code, invoke.ErrCodeExecutionFailed)
	}
}

// A consumer-supplied small delivery-unit bound applies to the complete SSE
// body and refuses loudly before any partial value is emitted.
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
	if ierr.Code != invoke.ErrCodeExecutionFailed {
		t.Errorf("error code = %q, want %q", ierr.Code, invoke.ErrCodeExecutionFailed)
	}
	if ierr.Error() != invoke.ErrCodeExecutionFailed {
		t.Errorf("error text = %q, want the abstract code", ierr.Error())
	}
}
