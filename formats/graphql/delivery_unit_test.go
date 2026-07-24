package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	openbindings "github.com/openbindings/openbindings-go"
)

func payloadSubscriptionServer(t *testing.T, payloadSize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		_, _, _ = conn.Read(r.Context())
		_ = writeJSON(r.Context(), conn, map[string]any{"type": "connection_ack"})
		_, raw, _ := conn.Read(r.Context())
		var subscribe struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &subscribe)
		_ = writeJSON(r.Context(), conn, map[string]any{
			"id": subscribe.ID, "type": "next",
			"payload": map[string]any{"data": map[string]any{"updates": strings.Repeat("x", payloadSize)}},
		})
		_ = writeJSON(r.Context(), conn, map[string]any{"id": subscribe.ID, "type": "complete"})
	}))
}

func subscriptionArgs(t *testing.T, srv *httptest.Server, limit int64) *openbindings.BindingInvocationArgs {
	t.Helper()
	return &openbindings.BindingInvocationArgs{
		Source:               pinnedInvocationSource(t, srv.URL),
		Ref:                  "subscription/updates",
		MaxDeliveryUnitBytes: limit,
		Context: map[string]any{"configuration": map[string]any{
			"document":           "subscription { updates }",
			"subscriptionTarget": "ws" + strings.TrimPrefix(srv.URL, "http"),
		}},
	}
}

func TestSubscriptionDeliveryUnitDefaultAccepts64KiB(t *testing.T) {
	srv := payloadSubscriptionServer(t, 64<<10)
	defer srv.Close()
	outputs, invocationErr := collectInvocation(context.Background(), NewInvoker().InvokeBinding(
		context.Background(), subscriptionArgs(t, srv, 0),
	), nil, false)
	if invocationErr != nil || len(outputs) != 1 {
		t.Fatalf("outputs = %d, err = %v", len(outputs), invocationErr)
	}
}

func TestSubscriptionDeliveryUnitBoundRefusesOversize(t *testing.T) {
	srv := payloadSubscriptionServer(t, 4<<10)
	defer srv.Close()
	outputs, invocationErr := collectInvocation(context.Background(), NewInvoker().InvokeBinding(
		context.Background(), subscriptionArgs(t, srv, 1024),
	), nil, false)
	if len(outputs) != 0 || invocationErr == nil || invocationErr.Code != openbindings.ErrCodeStreamError {
		t.Fatalf("outputs = %d, err = %v", len(outputs), invocationErr)
	}
}
