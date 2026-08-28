package openapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

type openAPI32OperationCapture struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
}

func (c *openAPI32OperationCapture) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.bodies = append(c.bodies, body)
	c.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    request,
	}, nil
}

func (c *openAPI32OperationCapture) snapshot() ([]*http.Request, [][]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*http.Request(nil), c.requests...), append([][]byte(nil), c.bodies...)
}

func invokeOpenAPI32Operation(t *testing.T, invoker *Invoker, source, selector string, input any) *invoke.InvocationError {
	t.Helper()
	call := invoker.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{
			BindingSpec: BindingSpecOpenAPI32,
			Content:     openbindings.TextContent(source),
		},
		Selector: selector,
	})
	_, invocationErr := driveOutputs(context.Background(), call, input)
	return invocationErr
}

func TestOpenAPI32AdapterPreservesOperationMethodAndCorrespondence(t *testing.T) {
	const source = `
openapi: 3.2.0
info: {title: operations, version: "1"}
servers: [{url: https://api.example}]
paths:
  /query:
    query:
      requestBody:
        required: true
        content:
          application/json:
            schema: {type: object}
      responses: {'204': {description: ok}}
  /custom:
    additionalOperations:
      MiXeD:
        requestBody:
          required: true
          content:
            application/json:
              schema: {type: object}
        responses: {'204': {description: ok}}
`
	capture := &openAPI32OperationCapture{}
	invoker := NewInvokerWithClient(&http.Client{Transport: capture})
	for _, testCase := range []struct {
		selector string
		body     any
	}{
		{"#/paths/~1query/query", map[string]any{"body": map[string]any{"term": "one"}}},
		{"#/paths/~1custom/additionalOperations/MiXeD", map[string]any{"body": map[string]any{"term": "two"}}},
	} {
		if err := invokeOpenAPI32Operation(t, invoker, source, testCase.selector, testCase.body); err != nil {
			requests, bodies := capture.snapshot()
			t.Fatalf("invoke %s: %v; requests=%d bodies=%q", testCase.selector, err, len(requests), bodies)
		}
	}
	requests, bodies := capture.snapshot()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for index, want := range []struct {
		method string
		body   string
	}{{"QUERY", `{"term":"one"}`}, {"MiXeD", `{"term":"two"}`}} {
		if requests[index].Method != want.method || string(bodies[index]) != want.body {
			t.Errorf("request %d = %s %q, want %s %q", index, requests[index].Method, bodies[index], want.method, want.body)
		}
	}

	if err := invokeOpenAPI32Operation(t, invoker, source, "#/paths/~1custom/additionalOperations/MIXED", nil); err == nil || err.Code != invoke.ErrCodeSelectorNotFound {
		t.Fatalf("case-changed selector error = %#v, want selector-not-found", err)
	}
	requests, _ = capture.snapshot()
	if len(requests) != 2 {
		t.Fatalf("case-changed selector dispatched: %d requests", len(requests))
	}
}

func TestOpenAPI32AdapterTRACEAndClosedEnvelope(t *testing.T) {
	const source = `
openapi: 3.2.0
info: {title: trace, version: "1"}
servers: [{url: https://api.example}]
paths:
  /trace:
    trace:
      requestBody:
        required: true
        content:
          application/json:
            schema: {type: object}
      responses: {'204': {description: ok}}
  /empty:
    post:
      responses: {'204': {description: ok}}
`
	capture := &openAPI32OperationCapture{}
	invoker := NewInvokerWithClient(&http.Client{Transport: capture})
	selector := "#/paths/~1trace/trace"
	if err := invokeOpenAPI32Operation(t, invoker, source, selector, nil); err != nil {
		t.Fatalf("body-free TRACE: %v", err)
	}
	requests, bodies := capture.snapshot()
	if len(requests) != 1 || requests[0].Method != "TRACE" || len(bodies[0]) != 0 {
		t.Fatalf("TRACE dispatch = %#v bodies=%q", requests, bodies)
	}

	if err := invokeOpenAPI32Operation(t, invoker, source, selector, map[string]any{"body": map[string]any{"forbidden": true}}); err == nil || err.Code != invoke.ErrCodeRefused {
		t.Errorf("body-bearing TRACE error = %#v, want refused", err)
	}
	for _, input := range []any{
		map[string]any{"unknown": true},
		map[string]any{"parameters": "not-an-object"},
		"not-an-envelope",
	} {
		if err := invokeOpenAPI32Operation(t, invoker, source, "#/paths/~1empty/post", input); err == nil || err.Code != invoke.ErrCodeRefused {
			t.Errorf("input %#v error = %#v, want refused", input, err)
		}
	}
	requests, _ = capture.snapshot()
	if len(requests) != 1 {
		t.Fatalf("invalid TRACE envelope dispatched: %d requests", len(requests))
	}
}
