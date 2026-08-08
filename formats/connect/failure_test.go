package connect

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFailureEvidenceFromHTTP(t *testing.T) {
	body := []byte(`{"code":"unauthenticated","message":"expired"}`)
	resp := &http.Response{
		StatusCode: 401,
		Status:     "401 Unauthorized",
		Header:     http.Header{"X-Request-Id": {"req-1"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	evidence, ok := FailureEvidenceFrom(connectHTTPError(resp, body, false))
	if !ok || evidence.HTTPResponse == nil {
		t.Fatal("Connect HTTP evidence not found")
	}
	if evidence.HTTPResponse.Status != 401 || !bytes.Equal(evidence.HTTPResponse.Body, body) || evidence.Error["code"] != "unauthenticated" {
		t.Errorf("evidence = %+v", evidence)
	}
}

func TestFailureEvidenceFromEndStreamAndRejectsLocal(t *testing.T) {
	payload := []byte(`{"error":{"code":"resource_exhausted","message":"quota"}}`)
	err := &openbindings.InvocationError{
		Code: openbindings.ErrCodeUnavailable,
		Details: map[string]any{"connect": map[string]any{"endStream": map[string]any{
			"error":   map[string]any{"code": "resource_exhausted", "message": "quota"},
			"payload": map[string]any{"base64": base64.StdEncoding.EncodeToString(payload), "byteLength": len(payload)},
		}}},
	}
	evidence, ok := FailureEvidenceFrom(err)
	if !ok || evidence.EndStream == nil || !bytes.Equal(evidence.EndStream.Payload, payload) {
		t.Fatalf("evidence = %+v, ok = %v", evidence, ok)
	}
	if _, ok := FailureEvidenceFrom(&openbindings.InvocationError{Code: openbindings.ErrCodeRuntime}); ok {
		t.Fatal("local error unexpectedly had Connect evidence")
	}
}
