package openbindings

import (
	"encoding/json"
	"testing"
)

func TestInvocationErrorWireIsMinimalAndDiagnosticsAreExplicit(t *testing.T) {
	err := InvocationError{
		Code:        ErrCodeExecutionFailed,
		Message:     "unsuccessful",
		Diagnostics: map[string]any{"httpStatus": 503},
	}
	raw, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var wire map[string]any
	if json.Unmarshal(raw, &wire) != nil {
		t.Fatalf("invalid JSON: %s", raw)
	}
	if _, present := wire["category"]; present {
		t.Fatalf("closed failure category leaked onto wire: %s", raw)
	}
	if _, present := wire["effects"]; present {
		t.Fatalf("retry policy leaked onto wire: %s", raw)
	}
	if wire["diagnostics"] == nil {
		t.Fatalf("explicit diagnostic lane missing: %s", raw)
	}
}

func TestHTTPStatusDoesNotSelectPortableFailureCode(t *testing.T) {
	for _, status := range []int{401, 404, 429, 500, 503, 504} {
		err := HTTPError(status, "")
		if err.Code != ErrCodeExecutionFailed {
			t.Errorf("HTTP %d code = %q, want generic unsuccessful completion", status, err.Code)
		}
		if got, ok := HTTPStatus(err); !ok || got != status {
			t.Errorf("HTTP %d diagnostic status = %d/%v", status, got, ok)
		}
	}
}

func TestContextRequiredUsesPortableDetailsNotDiagnostics(t *testing.T) {
	err := NewContextRequiredError("required", &ContextRequiredDetails{
		Target:       "service",
		Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.bearer"}}}},
	})
	if ContextRequiredFrom(err) == nil {
		t.Fatal("CONTEXT_REQUIRED details were not preserved")
	}
	if err.Diagnostics != nil {
		t.Fatalf("context challenge must not use diagnostics: %#v", err.Diagnostics)
	}
}
