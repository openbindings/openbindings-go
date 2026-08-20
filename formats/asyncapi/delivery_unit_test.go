package asyncapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"

	"github.com/coder/websocket"
)

// TestDeliveryUnitBound_UnaryOverflowRefused verifies the consumer
// delivery-unit bound on the unary publish reply: a tiny bound set via
// BindingInvocationArgs.MaxDeliveryUnitBytes refuses a ~2KB body with the
// lane's unchanged abstract error identity (ERR_RESPONSE_ERROR).
func TestDeliveryUnitBound_UnaryOverflowRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" || r.Method != "POST" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pad":"` + strings.Repeat("x", 2048) + `"}`))
	}))
	t.Cleanup(srv.Close)

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:               invoke.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(makeAsyncAPISpec(srv.URL))},
		Ref:                  "#/operations/sendOpenMessage",
		MaxDeliveryUnitBytes: 1024,
	})
	if err := call.Write(bg(), map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	var ie *invoke.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %v", err)
	}
	if ie.Code != invoke.ErrCodeResponseError {
		t.Errorf("error code = %q, want %q", ie.Code, invoke.ErrCodeResponseError)
	}
	if ie.HasData() {
		t.Errorf("implementation-local bound evidence crossed as abstract data: %#v", ie.Data)
	}
}

// TestDeliveryUnitBound_WSLargeMessagePassesAtDefault proves the
// accidental-cap fix: a 64 KiB WebSocket message — roughly double the
// library's ~32 KiB default read limit that previously applied because no
// read limit was ever set — is delivered intact under the SDK's default
// delivery-unit bound (10 MiB).
func TestDeliveryUnitBound_WSLargeMessagePassesAtDefault(t *testing.T) {
	payload := strings.Repeat("x", 64<<10)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		if err := writeWSJSON(ctx, conn, map[string]any{"pad": payload}); err != nil {
			t.Logf("server write: %v", err)
		}
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source: wsSource(srv, nil),
		Ref:    "#/operations/subscribe",
	})
	outs, err := drainOutputs(t, call)
	if err != nil {
		t.Fatalf("expected a clean close, got %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(outs))
	}
	m, _ := outs[0].(map[string]any)
	if got, _ := m["pad"].(string); got != payload {
		t.Fatalf("payload corrupted: got %d bytes, want %d", len(got), len(payload))
	}
}

// TestDeliveryUnitBound_WSTinyBoundRefusesLoudly verifies the bound is live
// on the WebSocket lane: with a 1 KiB consumer bound, a ~2 KiB message trips
// the socket's read limit and the subscription fails loudly
// (ERR_STREAM_ERROR), never a silent truncation.
func TestDeliveryUnitBound_WSTinyBoundRefusesLoudly(t *testing.T) {
	payload := strings.Repeat("x", 2048)
	srv := wsTestServer(t, func(ctx context.Context, conn *websocket.Conn, r *http.Request) {
		if err := writeWSJSON(ctx, conn, map[string]any{"pad": payload}); err != nil {
			t.Logf("server write: %v", err)
		}
		<-ctx.Done() // hold the socket open; the client's read-limit trip closes it
	})

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:               wsSource(srv, nil),
		Ref:                  "#/operations/subscribe",
		MaxDeliveryUnitBytes: 1024,
	})
	outs, err := drainOutputs(t, call)
	if len(outs) != 0 {
		t.Fatalf("expected no outputs, got %d", len(outs))
	}
	var ie *invoke.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %v", err)
	}
	if ie.Code != invoke.ErrCodeStreamError {
		t.Errorf("error code = %q, want %q", ie.Code, invoke.ErrCodeStreamError)
	}
	if ie.HasData() {
		t.Errorf("native read-limit evidence crossed as abstract data: %#v", ie.Data)
	}
}
