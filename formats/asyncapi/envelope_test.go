package asyncapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

// The routed envelope's schema shape (§9.2, ruled 2026-08-14; TS twin:
// envelope.test.ts): a location-less channel parameter rides the input
// envelope beside the payload — optional (config pre-fill is the amortized
// supply), while the payload stays required; the subscribe perspective
// gains a parameter-only input; declared headers pair per message.
func TestRoutedEnvelopeSchemas(t *testing.T) {
	artifact := map[string]any{
		"asyncapi": "3.0.0",
		"info":     map[string]any{"title": "Envelope", "version": "1"},
		"servers":  map[string]any{"broker": map[string]any{"host": "broker.example:9092", "protocol": "kafka"}},
		"channels": map[string]any{
			"orders": map[string]any{
				"address": "orders/{region}",
				"parameters": map[string]any{
					"region": map[string]any{"enum": []any{"emea", "amer"}},
				},
				"messages": map[string]any{
					"order": map[string]any{
						"payload": map[string]any{"type": "object"},
						"headers": map[string]any{"type": "object", "properties": map[string]any{"traceId": map[string]any{"type": "string"}}},
					},
				},
			},
		},
		"operations": map[string]any{
			"placeOrder": map[string]any{
				"action":   "receive",
				"channel":  map[string]any{"$ref": "#/channels/orders"},
				"messages": []any{map[string]any{"$ref": "#/channels/orders/messages/order"}},
			},
			"watchOrders": map[string]any{
				"action":   "send",
				"channel":  map[string]any{"$ref": "#/channels/orders"},
				"messages": []any{map[string]any{"$ref": "#/channels/orders/messages/order"}},
			},
		},
	}
	content, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	iface, err := (&Synthesizer{}).SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: BindingSpec, Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}

	input, _ := iface.Operations["placeOrder"].Input.(map[string]any)
	props, _ := input["properties"].(map[string]any)
	if _, present := props["payload"]; !present {
		t.Fatalf("receive input lacks payload field: %v", input)
	}
	if _, present := props["headers"]; !present {
		t.Fatalf("receive input lacks headers field: %v", input)
	}
	region, _ := props["region"].(map[string]any)
	if region["type"] != "string" {
		t.Fatalf("region param schema = %v", props["region"])
	}
	required, _ := input["required"].([]any)
	for _, member := range required {
		if member == "region" {
			t.Fatalf("parameter fields are never required (config pre-fill): %v", required)
		}
	}
	if len(required) != 2 {
		t.Fatalf("required = %v, want payload+headers", required)
	}
	if input["additionalProperties"] != false {
		t.Fatalf("envelope must be closed: %v", input)
	}

	// The subscribe perspective: parameter-only input envelope; the output
	// is the headers envelope (payload paired with its declared headers).
	watchInput, _ := iface.Operations["watchOrders"].Input.(map[string]any)
	watchProps, _ := watchInput["properties"].(map[string]any)
	if len(watchProps) != 1 {
		t.Fatalf("subscribe input = %v, want the parameter-only envelope", watchInput)
	}
	if _, present := watchProps["region"]; !present {
		t.Fatalf("subscribe input lacks the region parameter: %v", watchInput)
	}
	watchOutput, _ := iface.Operations["watchOrders"].Output.(map[string]any)
	outProps, _ := watchOutput["properties"].(map[string]any)
	if _, present := outProps["headers"]; !present {
		t.Fatalf("subscribe output lacks the headers field: %v", watchOutput)
	}
	if _, present := outProps["region"]; present {
		t.Fatalf("parameters never ride the output direction: %v", watchOutput)
	}
}

// Header carriage on the HTTP cell at the SDK invoker surface (client
// twins: TestClientCarriesTraitHeadersOverHTTP /
// TestClientProjectsReplyHeadersIntoOutputEnvelope): the envelope's
// headers ride the request as fields, and a headers-declaring reply comes
// back as the output envelope with declared properties projected.
func TestInvokerCarriesHeadersOverHTTP(t *testing.T) {
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("requestId", "r-9")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	t.Cleanup(srv.Close)
	artifact := map[string]any{
		"asyncapi": "3.0.0",
		"info":     map[string]any{"title": "Headers", "version": "1"},
		"servers":  map[string]any{"api": map[string]any{"host": strings.TrimPrefix(srv.URL, "http://"), "protocol": "http"}},
		"channels": map[string]any{
			"commands": map[string]any{
				"address": "/commands",
				"messages": map[string]any{
					"Command": map[string]any{
						"contentType": "application/json",
						"payload":     map[string]any{"type": "object"},
						"headers":     map[string]any{"type": "object", "properties": map[string]any{"traceId": map[string]any{"type": "string"}}},
					},
					"Result": map[string]any{
						"contentType": "application/json",
						"payload":     map[string]any{"type": "object"},
						"headers":     map[string]any{"type": "object", "properties": map[string]any{"requestId": map[string]any{"type": "string"}}},
					},
				},
			},
		},
		"operations": map[string]any{
			"submit": map[string]any{
				"action":   "receive",
				"channel":  map[string]any{"$ref": "#/channels/commands"},
				"messages": []any{map[string]any{"$ref": "#/channels/commands/messages/Command"}},
				"bindings": map[string]any{"http": map[string]any{"method": "POST"}},
				"reply":    map[string]any{"messages": []any{map[string]any{"$ref": "#/channels/commands/messages/Result"}}},
			},
		},
	}
	binv := NewInvoker()
	defer binv.Close()
	call := binv.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(artifact)},
		Ref:    "#/operations/submit",
	})
	if err := call.Write(context.Background(), map[string]any{
		"payload": map[string]any{"id": 4},
		"headers": map[string]any{"traceId": "t-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	outputs, err := drainOutputs(t, call)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Get("traceId") != "t-1" {
		t.Fatalf("request headers = %v, want the envelope's traceId carried", seen)
	}
	if len(outputs) != 1 {
		t.Fatalf("outputs = %#v", outputs)
	}
	envelope, _ := outputs[0].(map[string]any)
	payload, _ := envelope["payload"].(map[string]any)
	headers, _ := envelope["headers"].(map[string]any)
	if payload["accepted"] != true || headers["requestId"] != "r-9" {
		t.Fatalf("output envelope = %#v", envelope)
	}
}
