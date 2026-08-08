package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFailureEvidenceFromInProcessAndJSONFrame(t *testing.T) {
	details := map[string]any{
		"status": 500,
		"httpResponse": map[string]any{
			"status":  500,
			"headers": map[string]any{"content-type": []string{"application/octet-stream"}},
			"body":    map[string]any{"base64": "AP+AQQ==", "byteLength": 4},
		},
		"openapi": map[string]any{"declared": false},
	}
	err := &openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: "HTTP 500", Details: details}

	assert := func(t *testing.T, err error) {
		t.Helper()
		evidence, ok := FailureEvidenceFrom(err)
		if !ok {
			t.Fatal("expected OpenAPI failure evidence")
		}
		if evidence.HTTPResponse.Status != 500 || !evidence.HTTPResponse.BodyCaptured ||
			!bytes.Equal(evidence.HTTPResponse.Body, []byte{0x00, 0xff, 0x80, 0x41}) || evidence.OpenAPI.Declared {
			t.Fatalf("unexpected evidence: %+v", evidence)
		}
	}
	assert(t, err)

	wire, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var crossed openbindings.InvocationError
	if unmarshalErr := json.Unmarshal(wire, &crossed); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	assert(t, &crossed)
}

func TestFailureEvidenceFromRejectsLocalErrorAndCorruptCapture(t *testing.T) {
	if _, ok := FailureEvidenceFrom(errors.New("local runtime failure")); ok {
		t.Fatal("local errors must not invent source-native evidence")
	}
	corrupt := &openbindings.InvocationError{
		Code: openbindings.ErrCodeExecutionFailed,
		Details: map[string]any{
			"httpResponse": map[string]any{"status": 500, "body": map[string]any{"base64": "AA==", "byteLength": 2}},
			"openapi":      map[string]any{"declared": false},
		},
	}
	if _, ok := FailureEvidenceFrom(corrupt); ok {
		t.Fatal("corrupt lossless capture must not be accepted")
	}
}
