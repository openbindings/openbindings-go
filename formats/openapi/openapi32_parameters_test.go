package openapi

import (
	"net/http"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

func TestOpenAPI32AdapterMapsQueryStringThroughQualifiedEnvelope(t *testing.T) {
	const source = `
openapi: 3.2.0
info: {title: parameters, version: "1"}
servers: [{url: https://api.example}]
paths:
  /search:
    get:
      parameters:
        - name: payload
          in: querystring
          required: true
          content:
            application/x-www-form-urlencoded:
              schema:
                type: object
                properties:
                  page: {type: string}
                  tag: {type: string}
        - name: payload
          in: header
          schema: {type: string}
      responses: {'204': {description: ok}}
`
	capture := &openAPI32OperationCapture{}
	invoker := NewInvokerWithClient(&http.Client{Transport: capture})
	selector := "#/paths/~1search/get"
	input := map[string]any{"parameters": map[string]any{
		"querystring/payload": map[string]any{"page": "a b", "tag": "x/y"},
		"header/payload":      "header-value",
	}}
	if err := invokeOpenAPI32Operation(t, invoker, source, selector, input); err != nil {
		t.Fatalf("invoke querystring: %v", err)
	}
	requests, _ := capture.snapshot()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if got := requests[0].URL.RawQuery; got != "page=a+b&tag=x%2Fy" {
		t.Errorf("raw query = %q", got)
	}
	if got := requests[0].Header.Get("payload"); got != "header-value" {
		t.Errorf("payload header = %q", got)
	}

	for _, invalid := range []any{
		map[string]any{"parameters": map[string]any{"payload": "bare"}},
		map[string]any{"parameters": map[string]any{"header/payload": "missing required querystring"}},
	} {
		if err := invokeOpenAPI32Operation(t, invoker, source, selector, invalid); err == nil || err.Code != invoke.ErrCodeRefused {
			t.Errorf("input %#v error = %#v, want refused", invalid, err)
		}
	}
	requests, _ = capture.snapshot()
	if len(requests) != 1 {
		t.Fatalf("invalid envelope dispatched: %d requests", len(requests))
	}
}
