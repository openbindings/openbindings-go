package openbindings

import (
	"encoding/json"
	"testing"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

func TestExactNumbersAtSchemaBoundary(t *testing.T) {
	for _, tc := range []struct {
		schema, input string
		valid         bool
	}{
		{`{"type":"integer"}`, `9007199254740993`, true},
		{`{"type":"integer"}`, `1.00000000000000000001`, false},
		{`{"type":"number"}`, `1e400`, true},
		{`{"minimum":9007199254740993}`, `9007199254740992`, false},
		{`{"minimum":9007199254740993}`, `9007199254740993`, true},
		{`{"exclusiveMaximum":0.10000000000000000002}`, `0.10000000000000000001`, true},
		{`{"exclusiveMaximum":0.10000000000000000002}`, `0.10000000000000000002`, false},
		{`{"multipleOf":0.00000000000000000001}`, `0.00000000000000000003`, true},
		{`{"multipleOf":0.00000000000000000002}`, `0.00000000000000000003`, false},
		{`{"const":9007199254740993}`, `9007199254740992`, false},
		{`{"enum":[9007199254740993]}`, `9007199254740993`, true},
		{`{"enum":[9007199254740993]}`, `9007199254740992`, false},
		{`{"uniqueItems":true}`, `[9007199254740992,9007199254740993]`, true},
		{`{"uniqueItems":true}`, `[9007199254740993,9007199254740993.0]`, false},
	} {
		t.Run(tc.schema+"/"+tc.input, func(t *testing.T) {
			var schema, input any
			if err := jsonvalue.Unmarshal([]byte(tc.schema), &schema); err != nil {
				t.Fatal(err)
			}
			if err := jsonvalue.Unmarshal([]byte(tc.input), &input); err != nil {
				t.Fatal(err)
			}
			if err := ValidateAgainstSchema(input, schema, nil); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestInterfaceKnownJSONFieldsRetainNumbers(t *testing.T) {
	raw := []byte(`{"openbindings":"0.2.0","operations":{"test":{"input":{"type":"number","minimum":0.10000000000000000001},"examples":{"wide":{"input":9007199254740993,"output":1e400}}}}}`)
	var iface Interface
	if err := json.Unmarshal(raw, &iface); err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["test"]
	if op.Input.(map[string]any)["minimum"] != json.Number("0.10000000000000000001") {
		t.Fatalf("minimum changed: %#v", op.Input)
	}
	if op.Examples["wide"].Input != json.Number("9007199254740993") || op.Examples["wide"].Output != json.Number("1e400") {
		t.Fatalf("examples changed: %#v", op.Examples)
	}
	if _, err := json.Marshal(iface); err != nil {
		t.Fatal(err)
	}
}
