package openapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestSwagger20AdapterOwnsExactLoadAndSelectorGates(t *testing.T) {
	tests := []struct {
		name     string
		artifact string
		selector string
		wantCode string
	}{
		{
			name: "exact edition",
			artifact: `{"swagger":"2.0.1","openapi":"3.0.4","info":{"title":"wrong","version":"1"},` +
				`"paths":{"/pets":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1pets/get",
			wantCode: invoke.ErrCodeSourceLoadFailed,
		},
		{
			name: "selector grammar",
			artifact: `{"swagger":"2.0","info":{"title":"selector","version":"1"},` +
				`"paths":{"/pets":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1pets/GET",
			wantCode: invoke.ErrCodeInvalidSelector,
		},
		{
			name: "selected reference cycle",
			artifact: `{"swagger":"2.0","info":{"title":"cycle","version":"1"},` +
				`"a":{"$ref":"#/b"},"b":{"$ref":"#/a"},"paths":{"/pets":{"$ref":"#/a"}}}`,
			selector: "#/paths/~1pets/get",
			wantCode: openapiclient.CodeRefused,
		},
		{
			name: "prepared target needs pass-two server override",
			artifact: `{"swagger":"2.0","info":{"title":"valid","version":"1"},` +
				`"paths":{"/pets":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1pets/get",
			wantCode: openapiclient.CodeRefused,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source: invoke.InvocationSource{
					BindingSpec: BindingSpecOpenAPI20,
					Content:     openbindings.TextContent(testCase.artifact),
				},
				Selector: testCase.selector,
			})
			_, invocationErr := driveSingle(t, call, nil)
			if invocationErr == nil || invocationErr.Code != testCase.wantCode {
				t.Fatalf("invocation error = %#v, want %s", invocationErr, testCase.wantCode)
			}
		})
	}
}

func TestSwagger20AdapterMapsQualifiedKeysAndConfiguration(t *testing.T) {
	roundTripper := &scenarioRoundTripper{peer: map[string]any{"status": 204}}
	client := &http.Client{Transport: roundTripper}
	call := NewInvokerWithOptions(RuntimeOptions{
		HTTPClient: client,
		ParameterConversion: func(value any) (string, error) {
			if value == true {
				return "enabled", nil
			}
			return "", fmt.Errorf("unconfigured value")
		},
	}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{
			BindingSpec: BindingSpecOpenAPI20,
			Content: openbindings.TextContent(`{
  "swagger":"2.0","info":{"title":"adapter","version":"1"},
  "paths":{"/pets/{id}":{"get":{"parameters":[
    {"name":"id","in":"path","required":true,"type":"string"},
    {"name":"id","in":"query","type":"string"},
				{"name":"X~Ready","in":"header","type":"boolean"}
  ],"responses":{"204":{"description":"ok"}}}}}
}`),
		},
		Selector: "#/paths/~1pets~1{id}/get",
		Context: map[string]any{"configuration": map[string]any{
			"server": map[string]any{"baseUrl": "https://peer.example/root"},
		}},
	})
	outputs, invocationErr := driveOutputs(context.Background(), call, map[string]any{"parameters": map[string]any{
		"path/id": "a/b", "query/id": "two words", "header/X~0Ready": true,
	}})
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}
	if len(outputs) != 0 {
		t.Fatalf("outputs = %#v, want empty", outputs)
	}
	if len(roundTripper.dispatches) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(roundTripper.dispatches))
	}
	dispatch := roundTripper.dispatches[0]
	if got, want := dispatch["url"], "https://peer.example/root/pets/a%2Fb?id=two%20words"; got != want {
		t.Fatalf("URL = %v, want %s", got, want)
	}
	headers := dispatch["headers"].(map[string]any)
	if got := headers["x~ready"]; got != "enabled" {
		t.Fatalf("X~Ready = %v, want enabled", got)
	}
}

func TestSwagger20AdapterRefusesEnvelopeAndBodyBeforeDispatch(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		artifact string
		input    any
	}{
		{
			name: "unknown top-level key",
			artifact: `{"swagger":"2.0","info":{"title":"unknown","version":"1"},"paths":{"/pets":{"post":{` +
				`"responses":{"204":{"description":"ok"}}}}}}`,
			input: map[string]any{"extra": true},
		},
		{
			name: "body remains separate from its documentary name",
			artifact: `{"swagger":"2.0","info":{"title":"body","version":"1"},"paths":{"/pets":{"post":{` +
				`"parameters":[{"name":"payload","in":"body","schema":{}}],` +
				`"responses":{"204":{"description":"ok"}}}}}}`,
			input: map[string]any{"body": map[string]any{"name": "Ada"}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			roundTripper := &scenarioRoundTripper{peer: map[string]any{"status": 204}}
			call := NewInvokerWithClient(&http.Client{Transport: roundTripper}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI20, Content: openbindings.TextContent(testCase.artifact)},
				Selector: "#/paths/~1pets/post",
				Context:  map[string]any{"configuration": map[string]any{"server": "https://peer.example"}},
			})
			_, invocationErr := driveOutputs(context.Background(), call, testCase.input)
			if invocationErr == nil || invocationErr.Code != openapiclient.CodeRefused {
				t.Fatalf("error = %#v, want %s", invocationErr, openapiclient.CodeRefused)
			}
			if len(roundTripper.dispatches) != 0 {
				t.Fatalf("dispatched %d requests", len(roundTripper.dispatches))
			}
		})
	}
}
