package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// outcome names what validating a value against an operation's input gives.
func outcome(err error) string {
	switch {
	case err == nil:
		return "valid"
	case errors.As(err, new(*SchemaValidationError)):
		return "mismatch"
	case errors.As(err, new(*SchemaGraphUnavailableError)):
		return "unavailable"
	}
	return "error: " + err.Error()
}

func inputOutcome(t *testing.T, document string, value any) string {
	t.Helper()
	return outcome(ValidateOperationInput(value, mustDecodeInterface(t, document), "op"))
}

// The legacy definitions and the schema values of the legacy dependencies are
// OBI positions (§7): the 2020-12 meta-schema validates them as schemas, so
// the reference rules judge what they hold as they judge any schema's.
func TestLegacyDefinitionsAreOBIPositions(t *testing.T) {
	for name, tc := range map[string]struct {
		entry, rule string
	}{
		"relative $ref":  {`{"$ref":"other.json"}`, "OBI-D-05"},
		"$dynamicRef":    {`{"$dynamicRef":"#x"}`, "OBI-D-12"},
		"unresolved ref": {`{"$ref":"#/nope"}`, "OBI-D-12"},
		"relative $id":   {`{"$id":"rel"}`, "OBI-D-05"},
	} {
		for _, keyword := range []string{"definitions", "dependencies"} {
			document := `{"openbindings":"0.2.0","schemas":{"A":{"` + keyword + `":{"d":` + tc.entry + `}}},"operations":{}}`
			if report := validateBytes(t, document); !reflect.DeepEqual(report.Violated, []string{tc.rule}) {
				t.Errorf("%s under %s: violated %v, want %s", name, keyword, report.Violated, tc.rule)
			}
		}
	}
	shape := validateBytes(t, `{"openbindings":"0.2.0","schemas":{"A":{"definitions":{"d":{"$schema":"http://json-schema.org/draft-07/schema#"}}}},"operations":{}}`)
	if shape.Evidence["OBI-D-06"] != EvidenceViolated {
		t.Errorf("$schema under definitions: OBI-D-06 %q", shape.Evidence["OBI-D-06"])
	}
	// A plain name declared under definitions belongs to the document
	// resource, and a pointer lands on an entry there.
	for _, ref := range []string{"#a", "#/schemas/A/definitions/d"} {
		document := `{"openbindings":"0.2.0","schemas":{"A":{"definitions":{"d":{"$anchor":"a","type":"string"}}}},"operations":{"op":{"input":{"$ref":"` + ref + `"}}}}`
		if report := validateBytes(t, document); report.Conclusion != ConclusionConformant {
			t.Errorf("%s: conclusion %s; findings %+v", ref, report.Conclusion, report.Findings)
		}
	}
	// An absolute reference is JSON Schema's, even to an $id under
	// definitions; this one resolves nowhere, so a value reaches no verdict.
	external := `{"openbindings":"0.2.0","schemas":{"A":{"definitions":{"d":{"$id":"https://ex.test/d","type":"string"}}}},
		"operations":{"op":{"input":{"$ref":"https://ex.test/d#/nope"}}}}`
	if report := validateBytes(t, external); report.Evidence["OBI-D-12"] != EvidenceSatisfied {
		t.Errorf("an $id under definitions: OBI-D-12 %q", report.Evidence["OBI-D-12"])
	}
	if got := inputOutcome(t, external, 5); got != "unavailable" {
		t.Errorf("an $id under definitions: %s", got)
	}
}

// Identifiers resolve by RFC 3986, a base whose path is not hierarchical
// included: b against urn:x:y is urn:b, which OBI-D-13 then compares.
func TestReferencesResolveByRFC3986(t *testing.T) {
	document := func(ref string) string {
		return `{"openbindings":"0.2.0","schemas":{"A":{"$id":"urn:x:y","$defs":{"b":{"$id":"b","type":"string"}}}},
			"operations":{"op":{"input":{"$ref":"` + ref + `"}}}}`
	}
	collision := `{"openbindings":"0.2.0","schemas":{"A":{"$id":"urn:x:y","$defs":{"b":{"$id":"b"}}},"B":{"$id":"urn:b"}},"operations":{}}`
	if report := validateBytes(t, collision); report.Evidence["OBI-D-13"] != EvidenceViolated {
		t.Errorf("b against urn:x:y is urn:b: OBI-D-13 %q", report.Evidence["OBI-D-13"])
	}
	if report := validateBytes(t, document("urn:x:y#/$defs/b")); report.Conclusion != ConclusionConformant {
		t.Errorf("urn:x:y#/$defs/b: conclusion %s; findings %+v", report.Conclusion, report.Findings)
	}
	// The schema library resolves a relative reference under such a base
	// differently, so a graph holding one gets no verdict.
	if got := inputOutcome(t, document("urn:b"), 5); got != "unavailable" {
		t.Errorf("a nested relative $id under urn:x:y: %s", got)
	}
}

// A root member named like a schema keyword is still no schema position, so
// a reference into it reaches no schema; the document root is never handed to
// the library.
func TestReferencesIntoRootMembersNamedLikeKeywords(t *testing.T) {
	document := `{"openbindings":"0.2.0","$defs":{"a":{"type":"string"}},"operations":{"op":{"input":{"$ref":"#/$defs/a"}}}}`
	if got := inputOutcome(t, document, 5); got != "unavailable" {
		t.Errorf("got %s", got)
	}
}

// Shapes earlier reviews found, each with the outcome the design gives.
func TestSchemaGraphEdgeCases(t *testing.T) {
	for _, c := range []struct {
		name, schemas, input string
		value                any
		want                 string
	}{
		{"a pointer through an array into an $id resource uses its base",
			`"U":{"allOf":[{"$id":"https://ex.test/r","type":"object","properties":{"a":{"$ref":"#"}}}]}`,
			`{"$ref":"#/schemas/U/allOf/0/properties/a"}`, "hello", "mismatch"},
		{"an enclosing resource's dialect",
			`"R":{"$id":"https://ex.test/r","$schema":"http://json-schema.org/draft-07/schema#","definitions":{"a":{"type":"string"}},"properties":{"p":{"type":"string"}}}`,
			`{"$ref":"#/schemas/R/properties/p"}`, 5, "unavailable"},
		{"an $id outside the schema positions",
			`"S":{"type":"object"}`, `{"$ref":"#/x-lib/T"}`, 5, "unavailable"},
		{"a URI more than one schema declares",
			`"A":{"$id":"https://ex.test/a","type":"string"},"B":{"$id":"https://ex.test/a","type":"integer"}`,
			`{"$ref":"https://ex.test/a"}`, 5, "unavailable"},
		{"an unreached external reference in a resource the library compiles whole",
			`"R":{"$id":"https://ex.test/r","properties":{"far":{"$ref":"https://outside.example/x"}},"$defs":{"near":{"type":"string"}}}`,
			`{"$ref":"https://ex.test/r#/$defs/near"}`, 5, "unavailable"},
		{"an unreached external reference beside a schema without $id",
			`"S":{"properties":{"far":{"$ref":"https://outside.example/x"}},"$defs":{"near":{"type":"string"}}}`,
			`{"$ref":"#/schemas/S/$defs/near"}`, 5, "mismatch"},
		{"then without if does not reach outside",
			`"S":{"then":{"$ref":"https://outside.example/x"},"type":"string"}`,
			`{"$ref":"#/schemas/S"}`, 5, "mismatch"},
		{"a cycle that never advances, under if",
			`"S":{"if":{"$ref":"#/schemas/S"},"then":false}`, `{"$ref":"#/schemas/S"}`, 5, "unavailable"},
		{"a cycle that never advances, under not",
			`"S":{"not":{"not":{"$ref":"#/schemas/S"}}}`, `{"$ref":"#/schemas/S"}`, 5, "unavailable"},
		{"a cycle that advances",
			`"S":{"type":"object","properties":{"n":{"$ref":"#/schemas/S"}}}`, `{"$ref":"#/schemas/S"}`,
			map[string]any{"n": map[string]any{"n": 5.0}}, "mismatch"},
		{"a $dynamicRef applied to property names",
			`"K":{"$id":"https://ex.test/k","$defs":{"name":{"$dynamicAnchor":"name","type":"string"}},"propertyNames":{"$dynamicRef":"#name"}}`,
			`{"$ref":"https://ex.test/k"}`, map[string]any{"b": 1.0}, "unavailable"},
		{"dynamic scope across resources",
			`"K":{"$id":"https://ex.test/k","$defs":{"name":{"$dynamicAnchor":"name","type":"string"}},"items":{"$dynamicRef":"#name"}},
			 "O":{"$id":"https://ex.test/o","$ref":"k","$defs":{"name":{"$dynamicAnchor":"name","pattern":"^a"}}}`,
			`{"$ref":"https://ex.test/o"}`, []any{"b"}, "mismatch"},
		{"strict keywords constrain nothing",
			`"S":{"type":"object","dependencies":{"a":["b"]},"$recursiveRef":"#"}`, `{"$ref":"#/schemas/S"}`,
			map[string]any{"a": 1.0}, "valid"},
		{"a pattern that is not an ECMA-262 regular expression with Unicode semantics",
			`"S":{"type":"string","pattern":"^a{"}`, `{"$ref":"#/schemas/S"}`, "a", "unavailable"},
		{"a meta-schema reached is available",
			`"S":{"$ref":"https://json-schema.org/draft/2020-12/schema"}`, `{"$ref":"#/schemas/S"}`,
			map[string]any{"type": 5.0}, "mismatch"},
	} {
		document := `{"openbindings":"0.2.0","x-lib":{"T":{"$id":"https://ex.test/t","type":"string"}},"schemas":{` + c.schemas + `},"operations":{"op":{"input":` + c.input + `}}}`
		if got := inputOutcome(t, document, c.value); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// A value in an annotation is never a schema position, so a reference that
// names one reaches no schema (JSON Schema 2020-12 §9.4.2): the graph is
// unavailable, whatever the value holds. A reference held in an annotation's
// data leads nowhere, and an annotation no reference names is only carried.
func TestSchemasHeldInAnnotations(t *testing.T) {
	for _, c := range []struct {
		name, operations string
		op               string
		value            any
		want             string
	}{
		{"a named annotation reaches no schema",
			`"op":{"input":{"$ref":"#/operations/op/input/x-note","x-note":{"minimum":100}}}`,
			"op", json.Number("5"), "unavailable"},
		{"a reference in an annotation's data leads nowhere",
			`"op":{"input":{"type":"number","x-note":{"$ref":"#/x-memo"}}}`,
			"op", "bad", "mismatch"},
		{"an annotation no reference names is only carried",
			`"op":{"input":{"type":"string","x-note":{"minimum":1e1000000000,"type":42}}}`,
			"op", "s", "valid"},
		{"another operation naming the annotation reaches no schema",
			`"op":{"input":{"type":"string","x-note":{"minimum":1}}},
			 "other":{"input":{"$ref":"#/operations/op/input/x-note"}}`,
			"other", json.Number("5"), "unavailable"},
	} {
		document := `{"openbindings":"0.2.0","x-memo":"text","operations":{` + c.operations + `}}`
		if got := outcome(ValidateOperationInput(c.value, mustDecodeInterface(t, document), c.op)); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// Data an operation schema only carries costs work in proportion to it,
// however deeply it nests, and so does a reference into it.
func TestCarriedDataIsLinear(t *testing.T) {
	for _, referenced := range []bool{false, true} {
		build := func(depth int) *Interface {
			ref := ""
			if referenced {
				ref = `"$ref":"#/operations/op/input/x-note` + strings.Repeat("/0", depth) + `",`
			}
			return mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{`+ref+`"x-note":`+strings.Repeat("[", depth)+`{"type":"string"}`+strings.Repeat("]", depth)+`}}}}`)
		}
		small, large := build(2000), build(8000)
		compile := func(i *Interface) func() {
			return func() {
				// A reference into carried data reaches no schema position,
				// so its graph is unavailable; either way the work is linear.
				if _, err := CompileOperationSchema(i, "op", "input"); err != nil && !(referenced && errors.As(err, new(*SchemaGraphUnavailableError))) {
					t.Fatal(err)
				}
			}
		}
		if ratio := float64(allocated(compile(large))) / float64(allocated(compile(small))); ratio > 6 {
			t.Errorf("referenced %v: 4 times the depth allocated %.1f times the memory", referenced, ratio)
		}
	}
}

// Many referenced schemas entries, each its own copy, cost work in proportion
// to their number.
func TestManyReferencedSchemasAreLinear(t *testing.T) {
	build := func(n int) *Interface {
		var lib, refs []string
		for i := range n {
			lib = append(lib, fmt.Sprintf(`"s%d":true`, i))
			refs = append(refs, fmt.Sprintf(`{"$ref":"#/schemas/s%d"}`, i))
		}
		return mustDecodeInterface(t, `{"openbindings":"0.2.0","schemas":{`+strings.Join(lib, ",")+`},"operations":{"op":{"input":{"allOf":[`+strings.Join(refs, ",")+`]}}}}`)
	}
	small, large := build(1000), build(4000)
	compile := func(i *Interface) func() {
		return func() {
			if _, err := CompileOperationSchema(i, "op", "input"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ratio := float64(allocated(compile(large))) / float64(allocated(compile(small))); ratio > 6 {
		t.Errorf("4 times the referenced schemas allocated %.1f times the memory", ratio)
	}
}

// A reference into a value no schema position holds reaches no schema, while
// the operations that reach only schema positions are judged as usual: one
// operation's unreachable reference changes nothing for another.
func TestAnUnreachableReferenceStaysWithItsOperation(t *testing.T) {
	document := `{"openbindings":"0.2.0",
		"schemas":{"M":{"type":"string"},
		           "S":{"x-note":{"properties":{"p":{"type":"string"}}}}},
		"operations":{"a":{"input":{"$ref":"#/schemas/S/x-note"}},
		              "b":{"input":{"$ref":"#/schemas/M"}}}}`
	iface := mustDecodeInterface(t, document)
	if got := outcome(ValidateOperationInput(json.Number("5"), iface, "b")); got != "mismatch" {
		t.Errorf("b: %s", got)
	}
	if got := outcome(ValidateOperationInput("a", iface, "a")); got != "unavailable" {
		t.Errorf("a: %s", got)
	}
}

// A value a keyword holds as data (const, enum) or as the legacy dependencies
// is no schema position, so a reference into it reaches no schema, while a
// property named like a keyword is a subschema and is evaluated.
func TestSchemasTheBundleCarriesAsWritten(t *testing.T) {
	for _, c := range []struct {
		schemas, input string
		want, says     string
	}{
		{`"A":{"const":{"$ref":"#/schemas/T"}},"T":{"type":"string"}`, `{"$ref":"#/schemas/A/const"}`, "unavailable", "not a schema position"},
		{`"A":{"const":{"type":"string"}}`, `{"$ref":"#/schemas/A/const"}`, "unavailable", "not a schema position"},
		{`"A":{"enum":[{"type":"string"}]}`, `{"$ref":"#/schemas/A/enum/0"}`, "unavailable", "not a schema position"},
		{`"A":{"dependencies":{"x":{"type":"string"}}}`, `{"$ref":"#/schemas/A/dependencies/x"}`, "unavailable", "not a schema position"},
		{`"A":{"properties":{"const":{"type":"string"}}}`, `{"$ref":"#/schemas/A/properties/const"}`, "mismatch", ""},
	} {
		document := `{"openbindings":"0.2.0","schemas":{` + c.schemas + `},"operations":{"op":{"input":` + c.input + `}}}`
		err := ValidateOperationInput(json.Number("5"), mustDecodeInterface(t, document), "op")
		if got := outcome(err); got != c.want || c.says != "" && !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: got %s (%v), want %s", c.input, got, err, c.want)
		}
	}
}

// One operation's outcome depends only on its own graph: another operation,
// and the order of operations, change nothing for it, in the document's
// findings or in value validation.
func TestSchemaGraphIsolation(t *testing.T) {
	operations := map[string]string{
		"ok":        `{"input":{"$ref":"#/schemas/Pet"}}`,
		"external":  `{"input":{"$ref":"https://outside.example/x"}}`,
		"broken":    `{"input":{"$ref":"#/schemas/Big"}}`,
		"cycle":     `{"input":{"$ref":"#/schemas/Loop"}}`,
		"resource":  `{"input":{"$ref":"https://ex.test/r"}}`,
		"ill-typed": `{"input":{"$ref":"#/schemas/Bad"}}`,
	}
	values := map[string]any{"ok": map[string]any{"name": json.Number("5")}, "external": json.Number("1"), "broken": json.Number("1"),
		"cycle": json.Number("1"), "resource": "s", "ill-typed": json.Number("1")}
	want := map[string]string{"ok": "mismatch", "external": "unavailable", "broken": "unavailable",
		"cycle": "unavailable", "resource": "mismatch", "ill-typed": "unavailable"}
	schemas := `"Pet":{"type":"object","properties":{"name":{"type":"string"}}},
		"Big":{"maximum":1e10001},"Loop":{"allOf":[{"$ref":"#/schemas/Loop"}]},
		"R":{"$id":"https://ex.test/r","type":"integer"},"Bad":{"type":42}`
	build := func(keys ...string) string {
		var entries []string
		for _, key := range keys {
			entries = append(entries, `"`+key+`":`+operations[key])
		}
		return `{"openbindings":"0.2.0","schemas":{` + schemas + `},"operations":{` + strings.Join(entries, ",") + `}}`
	}
	all := []string{"ok", "external", "broken", "cycle", "resource", "ill-typed"}
	findingsFor := func(report ValidationReport, key string) []string {
		var out []string
		for _, f := range report.Findings {
			if strings.HasPrefix(f.Path, "/operations/"+key+"/") {
				out = append(out, fmt.Sprintf("%s %s %s %s", f.Rule, f.Status, f.Path, f.Message))
			}
		}
		return out
	}
	together := validateBytes(t, build(all...))
	iface := mustDecodeInterface(t, build(all...))
	for _, key := range all {
		if got, alone := findingsFor(together, key), findingsFor(validateBytes(t, build(key)), key); !reflect.DeepEqual(got, alone) {
			t.Errorf("%s: with the others %v, alone %v", key, got, alone)
		}
		withOthers := outcome(ValidateOperationInput(values[key], iface, key))
		alone := outcome(ValidateOperationInput(values[key], mustDecodeInterface(t, build(key)), key))
		if withOthers != want[key] || alone != want[key] {
			t.Errorf("%s: with the others %s, alone %s, want %s", key, withOthers, alone, want[key])
		}
	}
}

// The same bytes give the same report, however the library orders members.
func TestReportsAreDeterministicForAdditionalProperties(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"object","additionalProperties":false},
		"examples":{"e":{"input":{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6}}}}}}`
	first := validateBytes(t, document)
	for range 100 {
		if report := validateBytes(t, document); !reflect.DeepEqual(report, first) {
			t.Fatalf("run gave\n%+v\nafter\n%+v", report, first)
		}
	}
}

// Validating an ordinary document, where each operation references its own
// schema and has an example, takes work in proportion to the document.
func TestValidateDocument_OrdinaryDocumentsAreLinear(t *testing.T) {
	build := func(n int) []byte {
		var schemas, operations []string
		for i := range n {
			schemas = append(schemas, fmt.Sprintf(`"S%d":{"type":"object","properties":{"name":{"type":"string"},"next":{"$ref":"#/schemas/S%d"}}}`, i, (i+1)%n))
			operations = append(operations, fmt.Sprintf(`"op%d":{"input":{"$ref":"#/schemas/S%d"},"examples":{"e":{"input":{"name":"x"}}}}`, i, i))
		}
		return []byte(`{"openbindings":"0.2.0","schemas":{` + strings.Join(schemas, ",") + `},"operations":{` + strings.Join(operations, ",") + `}}`)
	}
	small, large := build(250), build(1000)
	ratio := float64(allocated(func() { ValidateDocument(large, ValidateOptions{}) })) / float64(allocated(func() { ValidateDocument(small, ValidateOptions{}) }))
	if ratio > 6 {
		t.Errorf("4 times the operations allocated %.1f times the memory", ratio)
	}
}
