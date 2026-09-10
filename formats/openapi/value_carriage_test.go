package openapi

import (
	"encoding/json"
	"testing"
)

func TestAdapterDetailsAndSchemaRetainNumbers(t *testing.T) {
	value := map[string]any{"n": json.Number("9007199254740993"), "nested": map[string]any{"n": json.Number("1e400")}}
	for _, clone := range []map[string]any{cloneAnyMap(value), cloneNativeDetails(value)} {
		if clone["n"] != value["n"] || clone["nested"].(map[string]any)["n"] != json.Number("1e400") {
			t.Fatalf("details changed: %#v", clone)
		}
	}
	schema, _, err := projectSwagger20Schema(json.RawMessage(`{"type":"number","minimum":0.10000000000000000001}`), true, "#/definitions/N")
	if err != nil {
		t.Fatal(err)
	}
	if schema.(map[string]any)["minimum"] != json.Number("0.10000000000000000001") {
		t.Fatalf("schema changed: %#v", schema)
	}
}
