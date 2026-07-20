package graphql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	openbindings "github.com/openbindings/openbindings-go"
)

// prebuiltSubscriptionSchema carries a _query const so the invoker skips
// introspection entirely and dials the WebSocket directly.
var prebuiltSubscriptionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"_query": map[string]any{"type": "string", "const": "subscription { messageStream { id body } }"},
	},
}

// largePayloadSubscriptionServer speaks just enough graphql-transport-ws to
// deliver one `next` payload of the given size, then `complete`.
func largePayloadSubscriptionServer(t *testing.T, payloadSize int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "websocket only", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"graphql-transport-ws"},
		})
		if err != nil {
			t.Logf("websocket accept failed: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "test done")

		ctx := r.Context()
		if err := expectClientMessage(ctx, conn, "connection_init"); err != nil {
			t.Logf("server: %v", err)
			return
		}
		if err := writeServerMessage(ctx, conn, map[string]any{"type": "connection_ack"}); err != nil {
			t.Logf("server: %v", err)
			return
		}
		subID, err := expectClientSubscribe(ctx, conn)
		if err != nil {
			t.Logf("server: %v", err)
			return
		}
		payload := map[string]any{
			"data": map[string]any{
				"messageStream": map[string]any{"id": "1", "body": strings.Repeat("x", payloadSize)},
			},
		}
		if err := writeServerMessage(ctx, conn, map[string]any{"id": subID, "type": "next", "payload": payload}); err != nil {
			t.Logf("server: %v", err)
			return
		}
		_ = writeServerMessage(ctx, conn, map[string]any{"id": subID, "type": "complete"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDeliveryUnitBound_WSLargeMessagePassesAtDefault proves the
// accidental-cap fix on the subscription lane: a 64 KiB `next` message —
// roughly double the library's ~32 KiB default read limit that previously
// applied because no read limit was ever set — is delivered intact under
// the SDK's default delivery-unit bound (10 MiB).
func TestDeliveryUnitBound_WSLargeMessagePassesAtDefault(t *testing.T) {
	srv := largePayloadSubscriptionServer(t, 64<<10)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{BindingSpec: "graphql", Location: srv.URL},
		Ref:         "Subscription/messageStream",
		InputSchema: prebuiltSubscriptionSchema,
	})
	events, ierr := driveOutputs(ctx, call, nil)
	if ierr != nil {
		t.Fatalf("expected a clean close, got %s: %s", ierr.Code, ierr.Message)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	data, _ := events[0].(map[string]any)
	ms, _ := data["messageStream"].(map[string]any)
	if body, _ := ms["body"].(string); len(body) != 64<<10 {
		t.Fatalf("payload corrupted: got %d bytes, want %d", len(body), 64<<10)
	}
}

// TestDeliveryUnitBound_WSTinyBoundRefusesLoudly verifies the bound is live
// on the subscription lane: with a 1 KiB consumer bound set via
// BindingInvocationArgs.MaxDeliveryUnitBytes, a ~4 KiB `next` message trips
// the socket's read limit and the subscription fails loudly with the lane's
// unchanged error identity (ERR_STREAM_ERROR), never a silent truncation.
func TestDeliveryUnitBound_WSTinyBoundRefusesLoudly(t *testing.T) {
	srv := largePayloadSubscriptionServer(t, 4<<10)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	call := NewInvoker().InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:               openbindings.InvocationSource{BindingSpec: "graphql", Location: srv.URL},
		Ref:                  "Subscription/messageStream",
		InputSchema:          prebuiltSubscriptionSchema,
		MaxDeliveryUnitBytes: 1024,
	})
	events, ierr := driveOutputs(ctx, call, nil)
	if len(events) != 0 {
		t.Fatalf("expected no events, got %d", len(events))
	}
	if ierr == nil {
		t.Fatal("expected a stream error, got clean close")
	}
	if ierr.Code != openbindings.ErrCodeStreamError {
		t.Errorf("error code = %q, want %q", ierr.Code, openbindings.ErrCodeStreamError)
	}
}
