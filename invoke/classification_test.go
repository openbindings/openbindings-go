package invoke

import (
	"encoding/json"
	"math"
	"testing"
)

func TestInvocationErrorWireIsExactlyCodeAndOptionalData(t *testing.T) {
	terminal := InvocationError{
		Code: ErrCodeExecutionFailed,
	}
	raw, marshalErr := json.Marshal(terminal)
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
	if len(wire) != 1 || wire["code"] != ErrCodeExecutionFailed {
		t.Fatalf("code-only error changed shape: %s", raw)
	}

	nullRaw, marshalErr := json.Marshal(NewInvocationErrorWithData(ErrCodeExecutionFailed, nil))
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if string(nullRaw) != `{"code":"ERR_EXECUTION_FAILED","data":null}` &&
		string(nullRaw) != `{"data":null,"code":"ERR_EXECUTION_FAILED"}` {
		t.Fatalf("explicit null data was not preserved: %s", nullRaw)
	}
	var decoded InvocationError
	if err := json.Unmarshal(nullRaw, &decoded); err != nil || !decoded.HasData() || decoded.Data != nil {
		t.Fatalf("explicit null did not round-trip: %#v, %v", decoded, err)
	}
}

func TestInvocationErrorRejectsNonJSONDataAndMalformedChallenges(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	for name, value := range map[string]any{
		"non-finite": math.Inf(1),
		"bytes":      []byte{1, 2},
		"map-key":    map[int]string{1: "one"},
		"cycle":      cycle,
		"function":   func() {},
	} {
		if got := NewInvocationErrorWithData(ErrCodeExecutionFailed, value); got.Code != ErrCodeRuntime || got.HasData() {
			t.Errorf("%s produced %#v, want code-only ERR_RUNTIME", name, got)
		}
	}
	if got := NewInvocationError(""); got.Code != ErrCodeRuntime {
		t.Fatalf("empty code produced %#v", got)
	}
	if got := NewInvocationError(ErrCodeContextRequired); got.Code != ErrCodeRuntime {
		t.Fatalf("data-free CONTEXT_REQUIRED produced %#v", got)
	}
	for name, raw := range map[string]string{
		"extra error member":     `{"code":"ERR_EXECUTION_FAILED","message":"native prose"}`,
		"empty requirement name": `{"code":"CONTEXT_REQUIRED","data":{"target":"","alternatives":[{"requirements":[{"type":"auth.bearer","name":""}]}]}}`,
		"wrong durable type":     `{"code":"CONTEXT_REQUIRED","data":{"target":"","alternatives":[{"requirements":[{"type":"auth.bearer","durable":"yes"}]}]}}`,
	} {
		var decoded InvocationError
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Errorf("%s wire was accepted: %#v", name, decoded)
		}
	}
}

func TestInvocationErrorCanonicalizesGoObjectsBeforeExposure(t *testing.T) {
	type carrier struct {
		Portable string `json:"portable"`
		Native   int    `json:"-"`
	}
	err := NewInvocationErrorWithData("APPLICATION_FAILURE", carrier{
		Portable: "declared",
		Native:   599,
	})
	data, ok := err.Data.(map[string]any)
	if !ok || len(data) != 1 || data["portable"] != "declared" {
		t.Fatalf("data was not canonicalized: %#v", err.Data)
	}
	raw, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var wire map[string]any
	if json.Unmarshal(raw, &wire) != nil {
		t.Fatalf("invalid wire: %s", raw)
	}
	if got, _ := wire["data"].(map[string]any); len(got) != 1 || got["portable"] != data["portable"] {
		t.Fatalf("local/wire data differ: local=%#v wire=%#v", data, got)
	}
}

func TestHTTPStatusDoesNotSelectPortableFailureCode(t *testing.T) {
	for _, status := range []int{401, 404, 429, 500, 503, 504} {
		err := HTTPError(status, "")
		if err.Code != ErrCodeExecutionFailed {
			t.Errorf("HTTP %d code = %q, want generic unsuccessful completion", status, err.Code)
		}
		if err.HasData() {
			t.Errorf("HTTP %d leaked native status as abstract data", status)
		}
	}
}

func TestContextRequiredUsesPortableDataOnly(t *testing.T) {
	err := NewContextRequiredError(&ContextRequiredDetails{
		Target:       "service",
		Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.bearer"}}}},
	})
	if ContextRequiredFrom(err) == nil {
		t.Fatal("CONTEXT_REQUIRED data was not preserved")
	}
	if !err.HasData() {
		t.Fatal("context challenge must carry its interface-owned data")
	}
}
