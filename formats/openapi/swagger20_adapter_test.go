package openapi

import (
	"context"
	"testing"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestSwagger20Pass1AdapterOwnsExactLoadAndSelectorGates(t *testing.T) {
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
			name: "prepared target remains unwarranted",
			artifact: `{"swagger":"2.0","info":{"title":"valid","version":"1"},` +
				`"paths":{"/pets":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1pets/get",
			wantCode: ErrCodeUnsupportedBindingSpec,
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
