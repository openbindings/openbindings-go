package openbindings

import (
	"encoding/json"
	"testing"
)

func mustUnmarshalJSON[T any](t *testing.T, b []byte, v *T) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func mustUnmarshalToMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	return m
}

func mustRoundTripToMap[T any](t *testing.T, in []byte, v *T) map[string]any {
	t.Helper()
	mustUnmarshalJSON(t, in, v)
	out := mustMarshalJSON(t, v)
	return mustUnmarshalToMap(t, out)
}

func assertPreservedExtensionAndUnknown(t *testing.T, outMap map[string]any) {
	t.Helper()
	if outMap["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected x-extensionField preserved, got %#v", outMap["x-extensionField"])
	}
	unknownField, ok := outMap["unknownField"].(map[string]any)
	if !ok {
		t.Fatalf("expected unknownField preserved as object, got %#v", outMap["unknownField"])
	}
	if unknownField["value"] != "unknownFieldValue" {
		t.Fatalf("expected unknownField.value preserved, got %#v", unknownField["value"])
	}
}
