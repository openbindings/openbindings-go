package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
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
	if report := validateBytes(t, unknownMember); report.Evidence["OBI-D-11"] != EvidenceViolated {
		t.Fatalf("an unknown dependency member must not stop OBI-D-11: %q", report.Evidence["OBI-D-11"])
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

// A same-document reference into any root member not named like a schema
// keyword resolves, for validation as for OBI-D-16.
func TestOperationContracts_ReferencesIntoUnknownMembersResolve(t *testing.T) {
	document := `{"openbindings":"0.2.0","defs":{"S":{"type":"string"}},
		"operations":{"a":{"input":{"$ref":"#/defs/S"},"examples":{"e":{"input":5}}}}}`
	report := validateBytes(t, document)
	if report.Evidence["OBI-D-16"] != EvidenceSatisfied || report.Evidence["OBI-D-11"] != EvidenceViolated {
		t.Fatalf("OBI-D-16 %q, OBI-D-11 %q", report.Evidence["OBI-D-16"], report.Evidence["OBI-D-11"])
	}
	if err := ValidateOperationInput("x", mustDecodeInterface(t, document), "a"); err != nil {
		t.Fatal(err)
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
		if report.Evidence["OBI-D-02"] == EvidenceInconclusive || report.Evidence["OBI-D-11"] != EvidenceViolated {
			t.Errorf("%s: OBI-D-02 %q, OBI-D-11 %q", name, report.Evidence["OBI-D-02"], report.Evidence["OBI-D-11"])
		}
		if err := ValidateOperationInput("ok", mustDecodeInterface(t, document), "op"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	reached := mustDecodeInterface(t, `{"openbindings":"0.2.0","x-lib":{"S":{"maximum":1e10001}},"operations":{"op":{"input":{"$ref":"#/x-lib/S"}}}}`)
	if err := ValidateOperationInput(1, reached, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) || !strings.Contains(err.Error(), "/x-lib/S/maximum") {
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
// that reaches outside the document is outside OBI-D-11 whatever else it
// holds, and one that does not leaves no verdict.
func TestOperationContracts_UncompiledPatterns(t *testing.T) {
	outside := `{"openbindings":"0.2.0","operations":{"op":{"input":{"properties":{
		"a":{"pattern":"^(?=x)"},"b":{"$ref":"https://schemas.example.com/b.json"}}},
		"examples":{"e":{"input":{}}}}}}`
	if report := validateBytes(t, outside); report.Evidence["OBI-D-11"] != EvidenceSatisfied || report.Conclusion != ConclusionConformant {
		t.Fatalf("OBI-D-11 %q, conclusion %q", report.Evidence["OBI-D-11"], report.Conclusion)
	}
	for _, input := range []string{`{"pattern":"^(?=x)"}`, `{"patternProperties":{"^(?=x)":{}}}`} {
		iface := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"input":`+input+`}}}`)
		if err := ValidateOperationInput("x", iface, "op"); !errors.As(err, new(*SchemaGraphUnavailableError)) {
			t.Errorf("%s: want the graph unavailable, got %v", input, err)
		}
	}
}

// The same document reaches the same conclusion on every run.
func TestOperationContracts_ConclusionsAreDeterministic(t *testing.T) {
	document := `{"openbindings":"0.2.0",
		"schemas":{"S":{"$id":"http://json-schema.org/draft-07/schema","pattern":"("}},
		"operations":{"op":{"input":{"type":"object","properties":{
			"a":{"$ref":"http://json-schema.org/draft-07/schema"},
			"b":{"$ref":"https://json-schema.org/draft/2020-12/schema"}}},
			"examples":{"e":{"input":{}}}}}}`
	first := validateBytes(t, document)
	for range 50 {
		report := validateBytes(t, document)
		if report.Conclusion != first.Conclusion || report.Evidence["OBI-D-11"] != first.Evidence["OBI-D-11"] {
			t.Fatalf("run gave %q/%q after %q/%q", report.Conclusion, report.Evidence["OBI-D-11"], first.Conclusion, first.Evidence["OBI-D-11"])
		}
	}
}

// A schema nested deeper than the library compiles quickly is a resource
// limit, reported before the work is done.
func TestOperationContracts_DepthLimit(t *testing.T) {
	nested := strings.Repeat(`{"not":`, 2000) + `{}` + strings.Repeat(`}`, 2000)
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":` + nested + `,"examples":{"e":{"input":1}}}}}`
	start := time.Now()
	report := validateBytes(t, document)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("validation took %v", elapsed)
	}
	if report.Evidence["OBI-D-11"] != EvidenceInconclusive {
		t.Fatalf("OBI-D-11 %q", report.Evidence["OBI-D-11"])
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

// The keywords withheld from the library's root are JSON Schema's own,
// deprecated ones included, and no OBI map but dependencies is among them.
func TestSchemaKeywords(t *testing.T) {
	for _, keyword := range []string{"type", "$defs", "$id", "dependencies", "definitions", "$recursiveRef", "description", "format"} {
		if !schemaKeywords()[keyword] {
			t.Errorf("%s is a keyword", keyword)
		}
	}
	for _, member := range []string{"openbindings", "name", "version", "schemas", "operations", "sources", "bindings", "transforms"} {
		if schemaKeywords()[member] {
			t.Errorf("%s is not a keyword", member)
		}
	}
}

// OBI-D-02 holds only a binding's preference to the numeric limits, because
// it is the only member the document schema does numeric work on. This test
// fails if a schema update adds another.
func TestDocumentSchema_NumericWorkIsOnlyOnPreference(t *testing.T) {
	var schema any
	if err := json.Unmarshal(openbindingsSchemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	var found []string
	var walk func(node any, path string)
	walk = func(node any, path string) {
		switch node := node.(type) {
		case map[string]any:
			for key, value := range node {
				switch key {
				case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
					found = append(found, path)
				case "type":
					if value == "integer" || value == "number" {
						found = append(found, path)
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
	for _, path := range found {
		if path != "/$defs/BindingEntry/properties/preference" {
			t.Errorf("the document schema does numeric work at %s", path)
		}
	}
}
