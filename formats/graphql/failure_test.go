package graphql

import (
	"bytes"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFailureEvidenceFromHTTPAndWebSocket(t *testing.T) {
	httpErr := &openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Diagnostics: map[string]any{
		"httpResponse": map[string]any{
			"status": 500, "headers": map[string][]string{"x-id": {"1"}},
			"body": map[string]any{"base64": "AP+AQQ==", "byteLength": 4},
		},
		"graphql": map[string]any{"mediaType": "application/json"},
	}}
	evidence, ok := FailureEvidenceFrom(httpErr)
	if !ok || evidence.HTTPResponse == nil || !bytes.Equal(evidence.HTTPResponse.Body, []byte{0x00, 0xff, 0x80, 0x41}) || evidence.MediaType != "application/json" {
		t.Fatalf("evidence = %+v, ok = %v", evidence, ok)
	}

	wsErr := &openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Diagnostics: map[string]any{
		"graphqlTransportWs": map[string]any{"type": "error", "payload": []any{map[string]any{"message": "rejected"}}},
	}}
	evidence, ok = FailureEvidenceFrom(wsErr)
	if !ok || evidence.TransportWS == nil || evidence.TransportWS.Type != "error" {
		t.Fatalf("evidence = %+v, ok = %v", evidence, ok)
	}
	if _, ok := FailureEvidenceFrom(&openbindings.InvocationError{Code: openbindings.ErrCodeRuntime}); ok {
		t.Fatal("local error unexpectedly had GraphQL evidence")
	}
}
