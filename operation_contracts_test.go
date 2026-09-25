package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

func mustDecodeInterface(t *testing.T, document string) *Interface {
	t.Helper()
	var iface Interface
	if err := json.Unmarshal([]byte(document), &iface); err != nil {
		t.Fatal(err)
	}
	return &iface
}

func validateBytes(t *testing.T, document string) ValidationReport {
	t.Helper()
	_, report, err := ValidateDocument([]byte(document), ValidateOptions{})
	if err != nil && !errors.As(err, new(*ValidationError)) {
		t.Fatalf("ValidateDocument: %v", err)
	}
	return report
}

// The OBI's own dependencies map is named like the deprecated JSON Schema
// keyword. It is not a schema: an unknown member of a dependency entry never
// acts as a keyword, and an entry never stands in for the schema its $id names.
func TestOperationContracts_DependenciesAreNotSchemas(t *testing.T) {
	unknownMember := `{"openbindings":"0.2.0","operations":{"a":{"input":{"type":"string"},"examples":{"e":{"input":5}}}},
		"dependencies":{"d":{"operation":"a","required":true}}}`
	if report := validateBytes(t, unknownMember); report.Evidence["OBI-D-10"] != EvidenceViolated {
		t.Fatalf("an unknown dependency member must not stop OBI-D-10: %q", report.Evidence["OBI-D-10"])
	}
	if err := ValidateOperationInput("x", mustDecodeInterface(t, unknownMember), "a"); err != nil {
		t.Fatalf("an unknown dependency member must not stop validation: %v", err)
	}

	shadow := mustDecodeInterface(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$id":"https://example.com/a","type":"string"}},
		"operations":{"op":{"input":{"$ref":"https://example.com/a"}}},
		"dependencies":{"d":{"operation":"op","$id":"https://example.com/a","type":"number"}}}`)
	if err := ValidateOperationInput("text", shadow, "op"); err != nil {
		t.Fatalf("the embedded schema accepts a string: %v", err)
	}
	if err := ValidateOperationInput(5, shadow, "op"); !errors.As(err, new(*SchemaValidationError)) {
		t.Fatalf("the embedded schema refuses a number, got %v", err)
	}
}

// A same-document reference into a root member the document model does not
// define reaches no schema: OBI-D-12 is violated, and a value gets no verdict.
func TestOperationContracts_ReferencesIntoUnknownMembersReachNoSchema(t *testing.T) {
	document := `{"openbindings":"0.2.0","defs":{"S":{"type":"string"}},
		"operations":{"a":{"input":{"$ref":"#/defs/S"},"examples":{"e":{"input":5}}}}}`
	report := validateBytes(t, document)
	if report.Evidence["OBI-D-12"] != EvidenceViolated || report.Evidence["OBI-D-10"] != EvidenceInconclusive {
		t.Fatalf("OBI-D-12 %q, OBI-D-10 %q", report.Evidence["OBI-D-12"], report.Evidence["OBI-D-10"])
	}
	if got := outcome(ValidateOperationInput("x", mustDecodeInterface(t, document), "a")); got != "unavailable" {
		t.Fatalf("got %s", got)
	}
}

// Only values the schema library can reach are held to the numeric limits: a
// large number elsewhere in the document blocks nothing.
func TestOperationContracts_NumericLimitsCoverOnlyTheReachableGraph(t *testing.T) {
	for name, member := range map[string]string{
		"extension":      `"x-padding":1e10001`,
		"source content": `"sources":{"s":{"bindingSpec":"x@1","content":{"n":1e10001}}}`,
	} {
		document := `{"openbindings":"0.2.0",` + member + `,"operations":{"op":{"input":{"type":"string"},"examples":{"e":{"input":5}}}}}`
		report := validateBytes(t, document)
		if report.Evidence["OBI-D-02"] == EvidenceInconclusive || report.Evidence["OBI-D-10"] != EvidenceViolated {
			t.Errorf("%s: OBI-D-02 %q, OBI-D-10 %q", name, report.Evidence["OBI-D-02"], report.Evidence["OBI-D-10"])
		}
		if err := ValidateOperationInput("ok", mustDecodeInterface(t, document), "op"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	reached := mustDecodeInterface(t, `{"openbindings":"0.2.0","schemas":{"S":{"maximum":1e10001}},"operations":{"op":{"input":{"$ref":"#/schemas/S"}}}}`)
	if err := ValidateOperationInput(1, reached, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) || !strings.Contains(err.Error(), "/schemas/S/maximum") {
		t.Fatalf("a reached schema beyond the limits leaves no verdict, located: %v", err)
	}
}

// Locations whose tokens need percent-encoding are compiled and inspected
// as the tokens they are.
func TestOperationContracts_PercentEncodedTokens(t *testing.T) {
	for _, name := range []string{"ab", "a b", "a%20b"} {
		iface := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{"`+name+`":{"$schema":"http://json-schema.org/draft-07/schema#"}}}}}}`)
		if err := ValidateOperationInput(map[string]any{}, iface, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
			t.Errorf("property %q: another dialect must leave the graph unavailable, got %v", name, err)
		}
	}
	keys := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"a%41":{"input":{"type":"string"}},"aA":{"input":{"type":"number"}}}}`)
	if err := ValidateOperationInput("hello", keys, "a%41"); err != nil {
		t.Fatalf("the operation keyed a%%41 is compiled, not aA: %v", err)
	}
}

// A pattern Go's regexp cannot compile no longer stops compilation: a graph
// that reaches outside the document is outside OBI-D-10 whatever else it
// holds, and one that does not leaves no verdict.
func TestOperationContracts_UncompiledPatterns(t *testing.T) {
	outside := `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{
		"a":{"pattern":"^(?=x)"},"b":{"$ref":"https://schemas.example.com/b.json"}}},
		"examples":{"e":{"input":{}}}}}}`
	if report := validateBytes(t, outside); report.Evidence["OBI-D-10"] != EvidenceSatisfied || report.Conclusion != ConclusionConformant {
		t.Fatalf("OBI-D-10 %q, conclusion %q", report.Evidence["OBI-D-10"], report.Conclusion)
	}
	for _, input := range []string{`{"pattern":"^(?=x)"}`, `{"patternProperties":{"^(?=x)":{}}}`} {
		iface := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":`+input+`}}}`)
		if err := ValidateOperationInput("x", iface, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
			t.Errorf("%s: want the graph unavailable, got %v", input, err)
		}
	}
}

// The same document gets the same report on every run, every finding and
// message included.
func TestOperationContracts_ReportsAreDeterministic(t *testing.T) {
	document := `{"openbindings":"0.2.0",
		"schemas":{"S":{"$id":"http://json-schema.org/draft-07/schema","pattern":"("}},
		"operations":{"op":{"input":{"type":"object","properties":{
			"a":{"$ref":"http://json-schema.org/draft-07/schema"},
			"b":{"$ref":"https://json-schema.org/draft/2020-12/schema"}}},
			"examples":{"e":{"input":{}}}}}}`
	first := validateBytes(t, document)
	for range 50 {
		if report := validateBytes(t, document); !reflect.DeepEqual(report, first) {
			t.Fatalf("run gave\n%+v\nafter\n%+v", report, first)
		}
	}
}

// Messages locate a schema by its JSON Pointer in the document, never by the
// URI the schema library is given for it.
func TestOperationContracts_MessagesLocateByDocumentPointer(t *testing.T) {
	report := validateBytes(t, `{"openbindings":"0.2.0",
		"schemas":{"S":{"$id":"https://example.com/s","properties":{"a":{"pattern":"(?=a)"}}}},
		"operations":{"op":{"input":{"$ref":"https://example.com/s"},"examples":{"e":{"input":{}}}}}}`)
	var messages []string
	for _, finding := range report.Findings {
		messages = append(messages, finding.Message)
	}
	if text := strings.Join(messages, "\n"); !strings.Contains(text, "at /schemas/S/properties/a") || strings.Contains(text, "urn:") {
		t.Fatalf("findings: %s", text)
	}
	_, err := CompileOperationSchema(mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":42}}}}`), "op", "input")
	if err == nil || !strings.Contains(err.Error(), "the schema at /operations/op/input is not a well-formed") || strings.Contains(err.Error(), "urn:") {
		t.Fatalf("got %v", err)
	}
}

// A schema nested deeper than the library compiles quickly is a resource
// limit, reported before the work is done.
func TestOperationContracts_DepthLimit(t *testing.T) {
	nested := strings.Repeat(`{"not":`, 2000) + `{}` + strings.Repeat(`}`, 2000)
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":` + nested + `,"examples":{"e":{"input":1}}}}}`
	report := validateBytes(t, document)
	if report.Evidence["OBI-D-10"] != EvidenceInconclusive {
		t.Fatalf("OBI-D-10 %q", report.Evidence["OBI-D-10"])
	}
	if _, err := CompileOperationSchema(mustDecodeInterface(t, document), "op", "input"); !errors.As(err, new(*SchemaGraphUnavailableError)) || !strings.Contains(err.Error(), "deeper than") {
		t.Fatalf("want the depth limit, got %v", err)
	}
}

// Validating a document does not grow process-wide state with the URIs it
// declares.
func TestOperationContracts_MetaSchemaCacheIsBounded(t *testing.T) {
	count := func() int {
		n := 0
		builtInMetaSchemas.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	var schemas []string
	for i := range 100 {
		schemas = append(schemas, fmt.Sprintf(`"S%d":{"$id":"https://json-schema.org/untrusted/%d"}`, i, i))
	}
	validateBytes(t, `{"openbindings":"0.2.0","operations":{},"schemas":{`+strings.Join(schemas, ",")+`}}`)
	before := count()
	for i := range 100 {
		schemas[i] = fmt.Sprintf(`"S%d":{"$id":"https://json-schema.org/other/%d"}`, i, i)
	}
	validateBytes(t, `{"openbindings":"0.2.0","operations":{},"schemas":{`+strings.Join(schemas, ",")+`}}`)
	if after := count(); after != before {
		t.Fatalf("the meta-schema cache grew from %d to %d", before, after)
	}
}

// Evaluation positions are 2020-12's; definitions and dependencies are only
// described by its meta-schema, so the document's shape rules walk them and
// nothing else does.
func TestKeywordTables(t *testing.T) {
	schema := map[string]any{
		"properties": map[string]any{"p": true}, "$defs": map[string]any{"d": true},
		"definitions": map[string]any{"x": true}, "dependencies": map[string]any{"y": true},
		"items": true, "allOf": []any{true},
	}
	var evaluated, described []string
	forEachSubschema(schema, func(_ any, tokens ...string) { evaluated = append(evaluated, tokens[0]) })
	forEachDescribedSubschema(schema, func(_ any, tokens ...string) { described = append(described, tokens[0]) })
	if want := []string{"$defs", "allOf", "items", "properties"}; !slices.Equal(evaluated, want) {
		t.Errorf("evaluation positions %v, want %v", evaluated, want)
	}
	if want := []string{"$defs", "allOf", "definitions", "dependencies", "items", "properties"}; !slices.Equal(described, want) {
		t.Errorf("described positions %v, want %v", described, want)
	}
}

// The document schema tells numbers apart only by type and equality, which
// a stand-in for a number beyond the numeric limits of schema evaluation
// keeps (validateAgainstOBISchema), except at a binding's preference, which
// validation decides exactly. This test fails if a schema update compares
// numbers anywhere else, and the preference range is §5.3's.
func TestDocumentSchema_ComparesNumbersOnlyAtAPreference(t *testing.T) {
	var schema any
	if err := json.Unmarshal(openbindingsSchemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	var holdsNumber func(value any) bool
	holdsNumber = func(value any) bool {
		switch value := value.(type) {
		case float64:
			return true
		case []any:
			return slices.ContainsFunc(value, holdsNumber)
		case map[string]any:
			for _, member := range value {
				if holdsNumber(member) {
					return true
				}
			}
		}
		return false
	}
	found := map[string]bool{}
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch node := node.(type) {
		case map[string]any:
			for key, value := range node {
				switch key {
				case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
					found[path] = true
				case "const", "enum":
					if holdsNumber(value) {
						found[path] = true
					}
				}
				walk(value, path+"/"+key)
			}
		case []any:
			for i, item := range node {
				walk(item, fmt.Sprintf("%s/%d", path, i))
			}
		}
	}
	walk(schema, "")
	if got := slices.Sorted(maps.Keys(found)); !slices.Equal(got, []string{"/$defs/BindingEntry/properties/preference"}) {
		t.Fatalf("the document schema compares numbers at %v", got)
	}
	preference, _ := jsonpointer.Resolve(schema, "/$defs/BindingEntry/properties/preference")
	bounds := preference.(map[string]any)
	if bounds["minimum"] != float64(-maxPreference) || bounds["maximum"] != float64(maxPreference) {
		t.Fatalf("the document schema bounds a preference by %v and %v", bounds["minimum"], bounds["maximum"])
	}
}

// An absolute URI names the resource it resolves to, dot segments removed
// (RFC 3986 §5.2.4), as the schema library resolves it: however the $id and
// the $ref spell it, the reference reaches the embedded schema.
func TestOperationContracts_DotSegmentsAreRemoved(t *testing.T) {
	for _, spelling := range []struct{ id, ref string }{
		{"https://ex.test/x/../a", "https://ex.test/a"},
		{"https://ex.test/a", "https://ex.test/x/../a"},
		{"https://ex.test/./a", "https://ex.test/a"},
	} {
		document := `{"openbindings":"0.2.0","schemas":{"A":{"$id":"` + spelling.id + `","type":"string"}},
			"operations":{"op":{"input":{"$ref":"` + spelling.ref + `"},"examples":{"e":{"input":5}}}}}`
		if report := validateBytes(t, document); report.Evidence["OBI-D-10"] != EvidenceViolated {
			t.Errorf("%s from %s: OBI-D-10 %q, conclusion %q", spelling.ref, spelling.id, report.Evidence["OBI-D-10"], report.Conclusion)
		}
		if err := ValidateOperationInput(json.Number("5"), mustDecodeInterface(t, document), "op"); !errors.As(err, new(*SchemaValidationError)) {
			t.Errorf("%s from %s: want a mismatch, got %v", spelling.ref, spelling.id, err)
		}
		missing := strings.Replace(document, `"$ref":"`+spelling.ref+`"`, `"$ref":"`+spelling.ref+`#/nope"`, 1)
		if report := validateBytes(t, missing); report.Evidence["OBI-D-12"] != EvidenceViolated {
			t.Errorf("%s#/nope from %s: OBI-D-12 %q", spelling.ref, spelling.id, report.Evidence["OBI-D-12"])
		}
	}
}

// A schema the document embeds under a JSON Schema meta-schema's URI is the
// schema a reference to that URI means (§7), not the meta-schema.
func TestOperationContracts_EmbeddedSchemaUnderAMetaSchemaURI(t *testing.T) {
	document := mustDecodeInterface(t, `{"openbindings":"0.2.0",
		"schemas":{"S":{"$id":"http://json-schema.org/draft-07/schema","type":"string"}},
		"operations":{"op":{"input":{"$ref":"http://json-schema.org/draft-07/schema"}}}}`)
	compiled, err := CompileOperationSchema(document, "op", "input")
	if err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate("text"); err != nil {
		t.Errorf("the embedded schema accepts a string: %v", err)
	}
	if err := compiled.Validate(json.Number("5")); !errors.As(err, new(*SchemaValidationError)) {
		t.Errorf("the embedded schema refuses a number, got %v", err)
	}
}
