package openbindings

import (
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
			if finding.Rule == "OBI-D-17" {
				if finding.Status != EvidenceViolated {
					t.Errorf("%s: OBI-D-17 %s at %s: %s", schema, finding.Status, finding.Path, finding.Message)
				}
				got = append(got, finding)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: OBI-D-17 findings %+v", schema, got)
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
// stand-in where the schema graph tells numbers apart only by type and
// equality; where it compares them otherwise, no verdict is reached. The same
// holds of an example (OBI-D-11).
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
		document := `{"openbindings":"0.2.0","schemas":{"N":{"maximum":5}},"operations":{"op":{"input":` + tc.input + `,"examples":{"e":{"input":` + tc.value + `}}}}}`
		err := ValidateOperationInput(decodeValue(t, []byte(tc.value)), mustDecodeInterface(t, document), "op")
		if got := outcome(err); got != tc.want {
			t.Errorf("%s against %s: %s, want %s (%v)", tc.value, tc.input, got, tc.want, err)
		}
		if tc.want == "unavailable" && !strings.Contains(err.Error(), "compares numbers with") {
			t.Errorf("%s against %s: %v", tc.value, tc.input, err)
		}
		example := map[string]RuleEvidenceStatus{"valid": EvidenceSatisfied, "mismatch": EvidenceViolated, "unavailable": EvidenceInconclusive}[tc.want]
		if got := mustValidateDocument(t, document).Evidence["OBI-D-11"]; got != example {
			t.Errorf("%s against %s: OBI-D-11 %s, want %s", tc.value, tc.input, got, example)
		}
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
			"operations":{"op":{"input":` + input + `,"examples":{"e":{"input":5}}}}}`
		report := mustValidateDocument(t, document)
		if report.Evidence["OBI-D-17"] != EvidenceSatisfied || report.Evidence["OBI-D-11"] == EvidenceInconclusive {
			t.Errorf("%s: OBI-D-17 %s, OBI-D-11 %s; findings %+v", input, report.Evidence["OBI-D-17"], report.Evidence["OBI-D-11"], report.Findings)
		}
		iface := mustDecodeInterface(t, document)
		if got := outcome(ValidateOperationInput(decodeValue(t, []byte(`"s"`)), iface, "op")); got != "valid" {
			t.Errorf("%s: \"s\" gave %s", input, got)
		}
	}
}
