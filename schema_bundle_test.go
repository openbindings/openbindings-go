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

// The answers strict 2020-12 changes for definitions and dependencies, whose
// entries 2020-12 neither evaluates nor finds resources or anchors in.
func TestStrictDefinitions(t *testing.T) {
	// An $id under definitions embeds nothing: an absolute reference to it
	// points outside the document, so its example is outside OBI-D-11, and
	// OBI-D-16 does not judge it.
	external := `{"openbindings":"0.2.0","schemas":{"A":{"definitions":{"d":{"$id":"https://ex.test/d","type":"string"}}}},
		"operations":{"op":{"input":{"$ref":"https://ex.test/d#/nope"},"examples":{"e":{"input":5}}}}}`
	report := validateBytes(t, external)
	if report.Evidence["OBI-D-11"] != EvidenceSatisfied || report.Evidence["OBI-D-16"] != EvidenceSatisfied {
		t.Errorf("an $id under definitions: OBI-D-11 %q, OBI-D-16 %q", report.Evidence["OBI-D-11"], report.Evidence["OBI-D-16"])
	}

	// The reference rules do not look inside definitions or dependencies.
	for name, entry := range map[string]string{
		"relative $ref":  `{"$ref":"other.json"}`,
		"$dynamicRef":    `{"$dynamicRef":"#x"}`,
		"unresolved ref": `{"$ref":"#/nope"}`,
		"relative $id":   `{"$id":"rel"}`,
	} {
		for _, keyword := range []string{"definitions", "dependencies"} {
			document := `{"openbindings":"0.2.0","schemas":{"A":{"` + keyword + `":{"d":` + entry + `}}},"operations":{}}`
			report := validateBytes(t, document)
			if report.Evidence["OBI-D-05"] != EvidenceSatisfied || report.Evidence["OBI-D-16"] != EvidenceSatisfied {
				t.Errorf("%s under %s: OBI-D-05 %q, OBI-D-16 %q", name, keyword, report.Evidence["OBI-D-05"], report.Evidence["OBI-D-16"])
			}
		}
	}
	// The shape rules still follow the meta-schema into them.
	shape := validateBytes(t, `{"openbindings":"0.2.0","schemas":{"A":{"definitions":{"d":{"$schema":"http://json-schema.org/draft-07/schema#"}}}},"operations":{}}`)
	if shape.Evidence["OBI-D-06"] != EvidenceViolated {
		t.Errorf("$schema under definitions: OBI-D-06 %q", shape.Evidence["OBI-D-06"])
	}

	// An anchor under definitions is not its resource's.
	anchor := validateBytes(t, `{"openbindings":"0.2.0","schemas":{"R":{"$id":"https://ex.test/r","definitions":{"d":{"$anchor":"a"}}}},
		"operations":{"op":{"input":{"$ref":"https://ex.test/r#a"}}}}`)
	if anchor.Evidence["OBI-D-16"] != EvidenceViolated {
		t.Errorf("an anchor under definitions: OBI-D-16 %q", anchor.Evidence["OBI-D-16"])
	}
}

// References resolve by RFC 3986, a base whose path is not hierarchical
// included: b against urn:x:y is urn:b.
func TestReferencesResolveByRFC3986(t *testing.T) {
	document := func(ref string) string {
		return `{"openbindings":"0.2.0","schemas":{"A":{"$id":"urn:x:y","$defs":{"b":{"$id":"b","type":"string"}}}},
			"operations":{"op":{"input":{"$ref":"` + ref + `"}}}}`
	}
	if report := validateBytes(t, document("urn:b#/nope")); report.Evidence["OBI-D-16"] != EvidenceViolated {
		t.Errorf("urn:b#/nope: OBI-D-16 %q", report.Evidence["OBI-D-16"])
	}
	if report := validateBytes(t, document("urn:x:y#/$defs/b")); report.Evidence["OBI-D-16"] != EvidenceSatisfied {
		t.Errorf("urn:x:y#/$defs/b: OBI-D-16 %q", report.Evidence["OBI-D-16"])
	}
	// The schema library resolves a relative reference under such a base
	// differently, so a graph holding one gets no verdict.
	if got := inputOutcome(t, document("urn:b"), 5); got != "unavailable" {
		t.Errorf("a nested relative $id under urn:x:y: %s", got)
	}
}

// A reference into a root member named like a schema keyword resolves, since
// core resolves it and the document root is never handed to the library.
func TestReferencesIntoRootMembersNamedLikeKeywords(t *testing.T) {
	document := `{"openbindings":"0.2.0","$defs":{"a":{"type":"string"}},"operations":{"op":{"input":{"$ref":"#/$defs/a"}}}}`
	if got := inputOutcome(t, document, 5); got != "mismatch" {
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
		{"then without if reaching outside",
			`"S":{"then":{"$ref":"https://outside.example/x"},"type":"string"}`,
			`{"$ref":"#/schemas/S"}`, 5, "unavailable"},
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
		{"a pattern Go's regexp cannot compile",
			`"S":{"type":"string","pattern":"^(?=a)"}`, `{"$ref":"#/schemas/S"}`, "a", "unavailable"},
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

// One operation's evidence depends only on its own graph: another operation,
// and the order of operations, change nothing for it; and ValidateDocument
// and CompileOperationSchema agree on every operation.
func TestSchemaGraphIsolation(t *testing.T) {
	operations := map[string]string{
		"ok":        `{"input":{"$ref":"#/schemas/Pet"},"examples":{"e":{"input":{"name":5}}}}`,
		"external":  `{"input":{"$ref":"https://outside.example/x"},"examples":{"e":{"input":1}}}`,
		"broken":    `{"input":{"$ref":"#/schemas/Big"},"examples":{"e":{"input":1}}}`,
		"cycle":     `{"input":{"$ref":"#/schemas/Loop"},"examples":{"e":{"input":1}}}`,
		"resource":  `{"input":{"$ref":"https://ex.test/r"},"examples":{"e":{"input":"s"}}}`,
		"ill-typed": `{"input":{"$ref":"#/schemas/Bad"},"examples":{"e":{"input":1}}}`,
	}
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
	evidenceFor := func(report ValidationReport, key string) []string {
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
		alone := validateBytes(t, build(key))
		if got, want := evidenceFor(together, key), evidenceFor(alone, key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: with the others %v, alone %v", key, got, want)
		}
		var example any
		var op map[string]any
		_ = json.Unmarshal([]byte(operations[key]), &op)
		example = op["examples"].(map[string]any)["e"].(map[string]any)["input"]
		direct := outcome(ValidateOperationInput(example, iface, key))
		var d11 []string
		for _, f := range together.Findings {
			if f.Rule == "OBI-D-11" && strings.HasPrefix(f.Path, "/operations/"+key+"/") {
				d11 = append(d11, string(f.Status))
			}
		}
		var want string
		switch {
		case len(d11) == 0 && key == "external":
			want = "unavailable" // outside OBI-D-11; the graph is not available
		case len(d11) == 0:
			want = "valid"
		case d11[0] == string(EvidenceViolated):
			want = "mismatch"
		default:
			want = "unavailable"
		}
		if direct != want {
			t.Errorf("%s: ValidateOperationInput %s, ValidateDocument's evidence %v", key, direct, d11)
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
