package openapi

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func TestDecimalParameterConversionVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/parameter-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Value    any
		Expected string
		Refuse   bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		got, err := DecimalParameterConversion(tc.Value)
		if tc.Refuse != (err != nil) || !tc.Refuse && got != tc.Expected {
			t.Fatalf("%#v: %q %v", tc.Value, got, err)
		}
	}
	for _, value := range []any{math.NaN(), math.Inf(1), int64(9007199254740992), json.Number("0.10000000000000000001")} {
		if _, err := DecimalParameterConversion(value); err == nil {
			t.Fatalf("accepted lossy or non-JSON value %#v", value)
		}
	}
	if got, _ := DecimalParameterConversion(math.Copysign(0, -1)); got != "0" {
		t.Fatal(got)
	}
}
