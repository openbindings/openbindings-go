package openbindings

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The 2020-12 meta-schemas tell numbers apart only by type, by equality, and
// by comparison with zero, which a stand-in for a number beyond the numeric
// limits of schema evaluation keeps (checkAgainstMetaSchema). This test fails
// if the schema library's copy of them compares numbers otherwise.
func TestMetaSchema_ComparesNumbersOnlyWithZero(t *testing.T) {
	seen := map[*jsonschema.Schema]bool{}
	var comparisons []string
	var visit func(v reflect.Value)
	visit = func(v reflect.Value) {
		if !v.IsValid() || !v.CanInterface() {
			return
		}
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				return
			}
			if schema, ok := v.Interface().(*jsonschema.Schema); ok {
				if seen[schema] {
					return
				}
				seen[schema] = true
				for keyword, limit := range map[string]*big.Rat{"minimum": schema.Minimum, "maximum": schema.Maximum, "exclusiveMinimum": schema.ExclusiveMinimum, "exclusiveMaximum": schema.ExclusiveMaximum, "multipleOf": schema.MultipleOf} {
					if limit != nil && (keyword == "multipleOf" || limit.Sign() != 0) {
						comparisons = append(comparisons, schema.Location+" "+keyword)
					}
				}
				if schema.Const != nil && holdsNumber(*schema.Const) || schema.Enum != nil && holdsNumber(schema.Enum.Values) {
					comparisons = append(comparisons, schema.Location+" const or enum")
				}
			}
			visit(v.Elem())
		case reflect.Interface:
			visit(v.Elem())
		case reflect.Struct:
			for i := range v.NumField() {
				visit(v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				visit(v.Index(i))
			}
		case reflect.Map:
			for iter := v.MapRange(); iter.Next(); {
				visit(iter.Value())
			}
		}
	}
	visit(reflect.ValueOf(compiledMetaSchema))
	if len(seen) < 50 {
		t.Fatalf("walked only %d schemas of the meta-schemas", len(seen))
	}
	if len(comparisons) > 0 {
		slices.Sort(comparisons)
		t.Fatalf("the meta-schemas compare numbers otherwise: %v", comparisons)
	}
}

// A schema holding a number beyond the numeric limits is checked against the
// meta-schemas as a stand-in, so every violation is found, and a finding on
// the stand-in states the number.
func TestValidateDocument_WellFormednessWithNumbersBeyondTheLimits(t *testing.T) {
	for schema, want := range map[string][]Finding{
		`{"type":42,"default":1e99999}`:          {{Path: "/schemas/A/type"}},
		`{"minLength":1e999999,"const":1e99999}`: nil,
		`{"maxLength":-1e99999}`:                 {{Path: "/schemas/A/maxLength", Message: "not a well-formed JSON Schema 2020-12 schema: minimum: got -1e99999, want 0"}},
		`{"required":[1e99999,10e99998]}`:        {{Path: "/schemas/A/required"}, {Path: "/schemas/A/required/0"}, {Path: "/schemas/A/required/1"}},
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"schemas":{"A":`+schema+`}}`)
		var got []Finding
		for _, finding := range report.Findings {
			if finding.Rule == "OBI-D-10" {
				if finding.Status != EvidenceViolated {
					t.Errorf("%s: OBI-D-10 %s at %s: %s", schema, finding.Status, finding.Path, finding.Message)
				}
				got = append(got, finding)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: OBI-D-10 findings %+v", schema, got)
			continue
		}
		for i := range want {
			if got[i].Path != want[i].Path || want[i].Message != "" && got[i].Message != want[i].Message {
				t.Errorf("%s: finding %+v, want %+v", schema, got[i], want[i])
			}
		}
	}
}

// A value holding a number beyond the numeric limits is validated as a
// stand-in where what the library compiles compares no number by order or
// divisibility, holds none in const or enum, and reaches no meta-schema;
// otherwise no verdict is reached.
func TestValidateOperationInput_NumbersBeyondTheLimits(t *testing.T) {
	for _, tc := range []struct {
		input, value, want string
	}{
		{`{"properties":{"a":{"type":"string"}}}`, `{"a":1,"b":1e99999}`, "mismatch"},
		{`{"properties":{"a":{"type":"string"}}}`, `{"a":"x","b":1e99999}`, "valid"},
		{`{"uniqueItems":true}`, `[1e99999,10e99998]`, "mismatch"},
		{`{"uniqueItems":true}`, `[1e99999,1e99998,-1e99999]`, "valid"},
		{`{"type":"integer"}`, `1.5e-99999`, "mismatch"},
		{`{"type":"integer"}`, `1.5e99999`, "valid"},
		{`{"const":"x"}`, `1e99999`, "mismatch"},
		{`{"properties":{"a":{"type":"string"},"c":{"minimum":0}}}`, `{"a":1,"b":1e99999}`, "unavailable"},
		{`{"$ref":"#/schemas/N"}`, `1e99999`, "unavailable"},
		{`{"enum":["x",1]}`, `1e99999`, "unavailable"},
	} {
		document := `{"openbindings":"0.2.0","schemas":{"N":{"maximum":5}},"operations":{"op":{"input":` + tc.input + `}}}`
		err := ValidateOperationInput(decodeValue(t, []byte(tc.value)), mustDecodeInterface(t, document), "op")
		if got := outcome(err); got != tc.want {
			t.Errorf("%s against %s: %s, want %s (%v)", tc.value, tc.input, got, tc.want, err)
		}
		if tc.want == "unavailable" && !strings.Contains(err.Error(), "compares numbers with") {
			t.Errorf("%s against %s: %v", tc.value, tc.input, err)
		}
	}
}

// The stand-in for a number beyond the limits equals another number of the
// value exactly when that number does, whatever Go type holds it.
func TestValidateOperationInput_StandInsAvoidGoNumbers(t *testing.T) {
	document := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"uniqueItems":true}}}}`)
	for _, tc := range []struct {
		value []any
		want  string
	}{
		{[]any{json.Number("1e99999"), 1}, "valid"},
		{[]any{json.Number("1e99999"), uint8(1), 2.0, float32(3)}, "valid"},
		{[]any{json.Number("1e99999"), json.Number("1e99999"), 1}, "mismatch"},
		{[]any{json.Number("1.5e99999"), 1.5}, "valid"},
	} {
		if got := outcome(ValidateOperationInput(tc.value, document, "op")); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.value, got, tc.want)
		}
	}
}

// A count the schema library would convert to an int past math.MaxInt
// reaches no verdict, since the keyword would bound a different count.
func TestValidateOperationInput_CountsBeyondMaxInt(t *testing.T) {
	for _, input := range []string{
		`{"minLength":9223372036854775808}`,
		`{"maxLength":18446744073709551617}`,
		`{"properties":{"a":{"minItems":1e19}}}`,
	} {
		document := `{"openbindings":"0.2.0","operations":{"op":{"input":` + input + `}}}`
		err := ValidateOperationInput(decodeValue(t, []byte(`{"a":[]}`)), mustDecodeInterface(t, document), "op")
		if got := outcome(err); got != "unavailable" || !strings.Contains(err.Error(), "the largest the schema library reads") {
			t.Errorf("%s: %s (%v)", input, got, err)
		}
	}
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"maxLength":9223372036854775807}}}}`
	if got := outcome(ValidateOperationInput("x", mustDecodeInterface(t, document), "op")); got != "valid" {
		t.Errorf("maxLength math.MaxInt: %s", got)
	}
}

// A number the schema library only carries (in default, examples, or a
// keyword it does not know) does not make a schema unavailable, however far
// beyond the numeric limits it lies, even past what math/big reads. v6.0.3
// reads a schema's numbers only as the values of the keywords that compare
// or count (objcompiler.go) and a value's numbers only where a keyword
// compares, counts, or tests equality (util.go, validator.go): never these.
func TestValidateOperationInput_CarriedNumbersAreNeverRead(t *testing.T) {
	const unreadable = "1e999999999999999"
	for _, input := range []string{
		`{"type":"string","default":` + unreadable + `}`,
		`{"type":"string","examples":[` + unreadable + `]}`,
		`{"type":"string","x-note":{"n":` + unreadable + `}}`,
		`{"properties":{"a":{"type":"string","default":` + unreadable + `}}}`,
		`{"$ref":"#/schemas/S"}`,
	} {
		document := `{"openbindings":"0.2.0","schemas":{"S":{"type":"string","default":` + unreadable + `}},
			"operations":{"op":{"input":` + input + `}}}`
		report := mustValidateDocument(t, document)
		if report.Evidence["OBI-D-10"] != EvidenceSatisfied {
			t.Errorf("%s: OBI-D-10 %s; findings %+v", input, report.Evidence["OBI-D-10"], report.Findings)
		}
		if got := outcome(ValidateOperationInput(json.Number("5"), mustDecodeInterface(t, document), "op")); got == "unavailable" {
			t.Errorf("%s: 5 reached no verdict", input)
		}
		iface := mustDecodeInterface(t, document)
		if got := outcome(ValidateOperationInput(decodeValue(t, []byte(`"s"`)), iface, "op")); got != "valid" {
			t.Errorf("%s: \"s\" gave %s", input, got)
		}
	}
}

// A Go value with no JSON reading is refused before validation: it is neither
// valid nor a mismatch.
func TestValidateOperationInput_ValuesThatAreNotJSON(t *testing.T) {
	document := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"maxLength":1,"pattern":"^.$"}}}}`)
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	cyclicSlice := []any{nil}
	cyclicSlice[0] = cyclicSlice
	for _, value := range []any{
		cyclicMap,
		cyclicSlice,
		"\xff",
		map[string]any{"\xff": "x"},
		[]any{"a", "b\xc3"},
		math.NaN(),
		struct{}{},
	} {
		err := ValidateOperationInput(value, document, "op")
		if err == nil || errors.As(err, new(*SchemaValidationError)) || !strings.Contains(err.Error(), "not a JSON value") {
			t.Errorf("%.40v: %v", value, err)
		}
	}
}
