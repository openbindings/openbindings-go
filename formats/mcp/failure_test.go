package mcp

import (
	"bytes"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFailureEvidenceFrom(t *testing.T) {
	err := &openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Diagnostics: map[string]any{
		"mcp": map[string]any{
			"result": map[string]any{"isError": true, "structuredContent": map[string]any{"reason": "policy"}},
			"jsonrpcError": map[string]any{
				"code": -32042, "message": "quota", "data": map[string]any{"limit": 10},
			},
		},
		"httpResponse": map[string]any{
			"status": 503, "headers": map[string][]string{"x-id": {"1"}},
			"body": map[string]any{"base64": "AP+AQQ==", "byteLength": 4},
		},
	}}
	evidence, ok := FailureEvidenceFrom(err)
	if !ok || evidence.Result["isError"] != true || evidence.JSONRPCError == nil || evidence.JSONRPCError.Code != -32042 {
		t.Fatalf("evidence = %+v, ok = %v", evidence, ok)
	}
	if evidence.HTTPResponse == nil || !bytes.Equal(evidence.HTTPResponse.Body, []byte{0x00, 0xff, 0x80, 0x41}) {
		t.Fatalf("HTTP evidence = %+v", evidence.HTTPResponse)
	}
	if _, ok := FailureEvidenceFrom(&openbindings.InvocationError{Code: openbindings.ErrCodeRuntime}); ok {
		t.Fatal("local runtime error unexpectedly had MCP evidence")
	}
}
