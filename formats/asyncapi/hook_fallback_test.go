package asyncapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	asyncapiclient "github.com/openbindings/asyncapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestP04HookBridgeDecisions(t *testing.T) {
	for _, mode := range []string{"absent", "decline", "wrapped-decline", "override", "nil", "fail", "panic"} {
		t.Run(mode, func(t *testing.T) {
			var order []string
			engine := invoke.NewOperationInvoker()
			engine.OutputDecoder = func(site invoke.InvokeSite, raw invoke.RawResult) (any, error) {
				order = append(order, "invoker")
				if site.Target != "ws://peer" || raw.Status != nil || string(raw.Body) != "payload" || !reflect.DeepEqual(raw.Meta, invoke.Metadata{"content-type": {"text/plain"}, "X-Case": {"a", "b"}}) {
					t.Fatalf("P04_METADATA: %#v %#v", site, raw)
				}
				// Consumer mutation must not affect native fallback inputs.
				raw.Body[0] = 'X'
				raw.Meta["X-Case"][0] = "changed"
				switch mode {
				case "override":
					return "handled", nil
				case "nil":
					return nil, nil
				case "fail":
					return nil, &invoke.InvocationError{Code: invoke.ErrCodeResponseError, Data: map[string]any{"reason": "consumer"}}
				case "panic":
					panic("consumer panic")
				case "wrapped-decline":
					return nil, fmt.Errorf("decline: %w", invoke.ErrUseDefault)
				default:
					return nil, invoke.ErrUseDefault
				}
			}
			if mode == "absent" {
				engine.OutputDecoder = nil
			}
			perCall := func(invoke.InvokeSite, invoke.RawResult) (any, error) {
				order = append(order, "call")
				return nil, invoke.ErrUseDefault
			}
			hooks := engine.SnapshotHooks(perCall, nil, nil)
			raw := asyncapiclient.RawResult{Body: []byte("payload"), Meta: asyncapiclient.Metadata{"content-type": {"text/plain"}, "X-Case": {"a", "b"}}}
			value, handled, err := bridgeHooks(&invoke.BindingInvocationArgs{Hooks: hooks}).Decode(asyncapiclient.HookSite{Target: "ws://peer"}, raw)
			wantOrder := []string{"call", "invoker"}
			if mode == "absent" {
				wantOrder = []string{"call"}
			}
			if !reflect.DeepEqual(order, wantOrder) {
				t.Fatalf("P04_TIERS: %v", order)
			}
			if string(raw.Body) != "payload" || raw.Meta["X-Case"][0] != "a" {
				t.Fatal("P04_METADATA: consumer mutation leaked")
			}
			switch mode {
			case "fail", "panic":
				var failure *asyncapiclient.ExecutionError
				if !handled || !errors.As(err, &failure) {
					t.Fatalf("P04_FAILURE: %v %v", handled, err)
				}
				wantCode := invoke.ErrCodeResponseError
				if mode == "panic" {
					wantCode = invoke.ErrCodeRuntime
				}
				if failure.Code != wantCode {
					t.Fatalf("P04_FAILURE_CODE: %#v", failure)
				}
				if mode == "fail" && (!failure.DetailsPresent || !reflect.DeepEqual(failure.Details, map[string]any{"reason": "consumer"})) {
					t.Fatalf("P04_ERROR_DATA: %#v", failure)
				}
			case "override", "nil":
				var want any = "handled"
				if mode == "nil" {
					want = nil
				}
				if !handled || err != nil || value != want || hooks.DecodeDecidedBy() != "hook" {
					t.Fatalf("P04_OVERRIDE: %#v %v %v", value, handled, err)
				}
			default:
				if handled || err != nil || value != nil || hooks.DecodeDecidedBy() != "builtin" {
					t.Fatalf("P04_DECLINE: %#v handled=%v err=%v", value, handled, err)
				}
			}
		})
	}
	// A successful per-call hook must suppress the invoker hook, even for nil.
	engine := invoke.NewOperationInvoker()
	engine.OutputDecoder = func(invoke.InvokeSite, invoke.RawResult) (any, error) { t.Fatal("lower tier called"); return nil, nil }
	hooks := engine.SnapshotHooks(func(invoke.InvokeSite, invoke.RawResult) (any, error) { return nil, nil }, nil, nil)
	value, handled, err := bridgeHooks(&invoke.BindingInvocationArgs{Hooks: hooks}).Decode(asyncapiclient.HookSite{}, asyncapiclient.RawResult{})
	if !handled || value != nil || err != nil {
		t.Fatalf("nil override: %v %v %v", value, handled, err)
	}
}

func TestP04LiveDeclaredFallback(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		want                    any
		bad                     bool
	}{
		{"json", "application/json", `{"n":9007199254740993,"large":1e400,"tiny":1e-400}`, map[string]any{"n": json.Number("9007199254740993"), "large": json.Number("1e400"), "tiny": json.Number("1e-400")}, false},
		{"suffix", "application/problem+json", `9007199254740993`, json.Number("9007199254740993"), false},
		{"text", "text/plain", `{"n":9007199254740993}`, `{"n":9007199254740993}`, false},
		{"empty", "application/json", "", nil, false},
		{"null", "application/json", "null", nil, false},
		{"invalid", "application/json", "1 2", nil, true},
	} {
		for _, mode := range []string{"no-hook", "decline", "override", "nil", "fail"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.CloseNow()
					if _, _, err = conn.Read(r.Context()); err != nil {
						return
					}
					if err = conn.Write(r.Context(), websocket.MessageText, []byte(tc.body)); err != nil {
						return
					}
					_, _, _ = conn.Read(r.Context())
				}))
				defer server.Close()
				doc := fmt.Sprintf(`{"asyncapi":"3.0.0","info":{"title":"P04 hook fallback","version":"1"},"servers":{"s":{"host":%q,"protocol":"ws"}},"channels":{"c":{"address":"/exchange","messages":{"M":{"contentType":%q,"payload":{}}}}},"operations":{"exchange":{"action":"receive","channel":{"$ref":"#/channels/c"},"messages":[{"$ref":"#/channels/c/messages/M"}],"reply":{"channel":{"$ref":"#/channels/c"},"messages":[{"$ref":"#/channels/c/messages/M"}]}}}}`, strings.TrimPrefix(server.URL, "http://"), tc.contentType)
				format := NewInvoker()
				defer format.Close()
				engine := invoke.NewOperationInvoker(format)
				var calls atomic.Int32
				if mode != "no-hook" {
					engine.OutputDecoder = func(_ invoke.InvokeSite, raw invoke.RawResult) (any, error) {
						calls.Add(1)
						if string(raw.Body) != tc.body {
							return nil, fmt.Errorf("wrong hook bytes: %q", raw.Body)
						}
						switch mode {
						case "override":
							return "override", nil
						case "nil":
							return nil, nil
						case "fail":
							return nil, &invoke.InvocationError{Code: invoke.ErrCodeResponseError}
						default:
							return nil, invoke.ErrUseDefault
						}
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				call := engine.InvokeBinding(ctx, &invoke.BindingInvocationArgs{Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: json.RawMessage(doc)}, Selector: openbindings.Present("#/operations/exchange"), Context: map[string]any{"configuration": map[string]any{"websocketMessageType": "text"}}})
				defer call.Cancel()
				var input any = map[string]any{"request": true}
				if tc.contentType == "text/plain" {
					input = "request"
				}
				if err := call.Write(ctx, input); err != nil {
					t.Fatal(err)
				}
				value, err := call.Outputs().Read(ctx)
				if mode != "no-hook" && calls.Load() != 1 {
					t.Fatalf("hook count: %d", calls.Load())
				}
				if mode == "fail" || (tc.bad && (mode == "no-hook" || mode == "decline")) {
					var failure *invoke.InvocationError
					if !errors.As(err, &failure) || failure.Code != invoke.ErrCodeResponseError {
						t.Fatalf("P04_LIVE_FAILURE: %#v %v", value, err)
					}
					return
				}
				want := tc.want
				if mode == "override" {
					want = "override"
				}
				if mode == "nil" {
					want = nil
				}
				// This reply path emits nil for both empty and JSON null. Raw hook
				// bytes above distinguish them; the seam test distinguishes nil handling.
				if err != nil || !reflect.DeepEqual(value, want) {
					t.Fatalf("P04_LIVE_FALLBACK: got %T %#v want %#v: %v", value, value, want, err)
				}
			})
		}
	}
}
