package usage

import (
	"bytes"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestFailureEvidenceFrom(t *testing.T) {
	err := &openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Diagnostics: map[string]any{
		"usage": map[string]any{"process": map[string]any{
			"exitCode": -1, "signal": "SIGTERM",
			"stdout": map[string]any{"base64": "cGFydGlhbA==", "byteLength": 7, "truncated": true},
			"stderr": map[string]any{"base64": "AP+AQQ==", "byteLength": 4},
		}},
	}}
	evidence, ok := FailureEvidenceFrom(err)
	if !ok || evidence.ExitCode != -1 || evidence.Signal != "SIGTERM" || !evidence.Stdout.Truncated {
		t.Fatalf("evidence = %+v, ok = %v", evidence, ok)
	}
	if !bytes.Equal(evidence.Stderr.Bytes, []byte{0x00, 0xff, 0x80, 0x41}) {
		t.Fatalf("stderr = %v", evidence.Stderr.Bytes)
	}
	if _, ok := FailureEvidenceFrom(&openbindings.InvocationError{Code: openbindings.ErrCodeRuntime}); ok {
		t.Fatal("local runtime error unexpectedly had Usage process evidence")
	}
}
