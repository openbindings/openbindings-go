package openbindings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func mustValidateDocument(t *testing.T, document string) ValidationReport {
	t.Helper()
	_, report, err := ValidateDocument([]byte(document), ValidateOptions{})
	var violation *ValidationError
	if err != nil && !errors.As(err, &violation) {
		t.Fatalf("ValidateDocument: %v", err)
	}
	if got := len(report.Evidence); got != len(documentRules) {
		t.Fatalf("report carries evidence for %d rules, want every document rule (%d)", got, len(documentRules))
	}
	return report
}

func TestValidateDocument_ConformantWhenEveryRuleIsDecided(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"tasks.create":{"input":{"type":"object"}}}}`)
	if report.Conclusion != ConclusionConformant {
		t.Fatalf("conclusion = %s, want conformant; findings %+v", report.Conclusion, report.Findings)
	}
	for _, rule := range DocumentRules() {
		if report.Evidence[rule] != EvidenceSatisfied {
			t.Fatalf("%s = %s, want satisfied", rule, report.Evidence[rule])
		}
	}
}

// A report names the release whose text it applies and, while that release
// is a working draft, the source-control revision of the text (OBI-T-09),
// from Interface.Validate and ValidateDocument alike.
func TestValidationReport_NamesTheTextApplied(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{}}`
	fromDocument := mustValidateDocument(t, document)
	fromInterface, err := mustDecodeInterface(t, document).Validate(ValidateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []ValidationReport{fromDocument, fromInterface} {
		if report.Version != "0.2.0" || len(report.Revision) != 40 || strings.Trim(report.Revision, "0123456789abcdef") != "" {
			t.Errorf("version %q, revision %q: want 0.2.0 and a full commit hash while 0.2.0 is a working draft", report.Version, report.Revision)
		}
	}
	if report := ConcludeConformance(map[string]RuleEvidenceStatus{"OBI-D-01": EvidenceSatisfied}); report.Version != "" || report.Revision != "" {
		t.Errorf("a report from evidence alone names %q at %q", report.Version, report.Revision)
	}
}

// A claim about a value in memory is a claim about its serialization (§10),
// which the model writes only when it decodes back unchanged, so a host
// object decides OBI-D-01 and a well-formed one concludes conformant, whether
// decoded or built in code.
func TestInterfaceValidate_DecidesD01OnTheSerialization(t *testing.T) {
	decoded, err := ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"tasks.create":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	built := Interface{OpenBindings: "0.2.0", Operations: map[string]Operation{"tasks.create": {}}}
	for name, iface := range map[string]Interface{"decoded": *decoded, "built": built} {
		report, err := iface.Validate(ValidateOptions{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if report.Conclusion != ConclusionConformant || report.Evidence["OBI-D-01"] != EvidenceSatisfied || len(report.Inconclusive) != 0 {
			t.Fatalf("%s: conclusion %s, OBI-D-01 %s, inconclusive %v", name, report.Conclusion, report.Evidence["OBI-D-01"], report.Inconclusive)
		}
	}
	// A value with no JSON serialization gets no report.
	broken := Interface{OpenBindings: "0.2.0", Name: Present("\xff"), Operations: map[string]Operation{}}
	if report, err := broken.Validate(ValidateOptions{}); err == nil || report.Evidence != nil {
		t.Fatalf("a string that is not UTF-8: report %+v, err %v", report, err)
	}
}

const documentWithBinding = `{
	"openbindings": "0.2.0",
	"operations": {"tasks.create": {}},
	"sources": {"api": {"kind": "example.rest@1", "content": {"location": "./openapi.json", "$ref": "#anchor"}}},
	"bindings": {"tasks.create.api": {"operation": "tasks.create", "source": "api",
		"content": {"$ref": "other.json", "inputTransform": "{ \"title\": name }"}}}
}`

// No document rule takes binding-specification knowledge: a source's and a
// binding's content are the binding specification's (§5.3, §5.4), so
// nothing within them, relative addresses and $ref members included, is
// judged, and a document with bindings is decided in full.
func TestValidateDocument_BindingsNeedNoKindificationKnowledge(t *testing.T) {
	_, report, err := ValidateDocument([]byte(documentWithBinding), ValidateOptions{})
	if err != nil {
		t.Fatalf("ValidateDocument = %v", err)
	}
	if report.Conclusion != ConclusionConformant || len(report.Findings) != 0 {
		t.Fatalf("conclusion = %s, want conformant; findings %+v", report.Conclusion, report.Findings)
	}
}

// A member the core no longer defines, such as a 0.1 source's location, is an
// unprefixed name the specification reserves (§12): an OBI-D-02 violation,
// with no separate advisory diagnostic.
func TestValidateDocument_ASourceLocationViolatesD02(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"sources":{"s":{"kind":"x@1","location":"./openapi.json"}}}`)
	if violations := report.Violations(); len(violations) != 1 || violations[0].Rule != "OBI-D-02" || violations[0].Path != "/sources/s" || !strings.Contains(violations[0].Message, "location") {
		t.Fatalf("want one OBI-D-02 violation at the source naming location, got %+v", violations)
	}
}

// hostReport validates a document as a host object, judging its
// serialization.
func hostReport(t *testing.T, document string) (ValidationReport, error) {
	t.Helper()
	var iface Interface
	if err := json.Unmarshal([]byte(document), &iface); err != nil {
		t.Fatal(err)
	}
	return iface.Validate(ValidateOptions{})
}

func TestValidateDocument_AViolationIsDecisiveAndInconclusiveRulesAreRetained(t *testing.T) {
	// A schema nested past the meta-schema check's depth limit leaves
	// OBI-D-10 inconclusive (§10.4).
	deep := strings.Repeat(`{"not":`, schemaDepthLimit+1) + `{}` + strings.Repeat(`}`, schemaDepthLimit+1)
	document := strings.Replace(documentWithBinding, `"operation": "tasks.create", "source"`, `"operation": "tasks.missing", "source"`, 1)
	document = strings.Replace(document, `"operations": {"tasks.create": {}}`, `"operations": {"tasks.create": {}}, "schemas": {"Deep": `+deep+`}`, 1)
	report, err := hostReport(t, document)
	if !errors.As(err, new(*ValidationError)) || report.Conclusion != ConclusionNonConformant {
		t.Fatalf("conclusion = %s, want non-conformant", report.Conclusion)
	}
	if report.Evidence["OBI-D-07"] != EvidenceViolated {
		t.Fatalf("OBI-D-07 = %s, want violated", report.Evidence["OBI-D-07"])
	}
	if report.Evidence["OBI-D-10"] != EvidenceInconclusive {
		t.Fatalf("OBI-D-10 = %s, want inconclusive and retained", report.Evidence["OBI-D-10"])
	}
	violations := report.Violations()
	if len(violations) != 1 || violations[0].Path != `/bindings/tasks.create.api/operation` {
		t.Fatalf("violations = %+v, want one located at the binding's operation", violations)
	}
}

func TestValidate_VersionRefusalIsNotAConclusion(t *testing.T) {
	document := `{"openbindings":"9.0.0","operations":{}}`
	iface, report, err := ValidateDocument([]byte(document), ValidateOptions{})
	var refusal *VersionRefusalError
	if !errors.As(err, &refusal) || refusal.Version != "9.0.0" {
		t.Fatalf("ValidateDocument error = %v, want a version refusal for 9.0.0", err)
	}
	if iface != nil || report.Conclusion != "" || report.Evidence != nil {
		t.Fatalf("a refused document has no interpretation and no conclusion: %v %+v", iface, report)
	}

	host := Interface{OpenBindings: "9.0.0", Operations: map[string]Operation{}}
	hostReport, err := host.Validate(ValidateOptions{})
	if !errors.As(err, &refusal) || hostReport.Evidence != nil {
		t.Fatalf("Validate = %+v, %v; want a version refusal and no report", hostReport, err)
	}
	if _, err := ParseDocument([]byte(document)); !errors.As(err, &refusal) {
		t.Fatalf("ParseDocument error = %v, want the same version refusal", err)
	}
}

func TestValidateDocument_InputThatIsNotAJSONDocumentViolatesD01(t *testing.T) {
	for name, input := range map[string][]byte{
		"invalid UTF-8":   {'{', '"', 0xff, '"', ':', '1', '}'},
		"duplicate keys":  []byte(`{"openbindings":"0.2.0","operations":{},"operations":{}}`),
		"byte-order mark": append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"openbindings":"0.2.0","operations":{}}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			iface, report, err := ValidateDocument(input, ValidateOptions{})
			var violation *ValidationError
			if !errors.As(err, &violation) || iface != nil {
				t.Fatalf("ValidateDocument = %v, %v; want a report, its violation, and no document", iface, err)
			}
			if report.Conclusion != ConclusionNonConformant || !reflect.DeepEqual(report.Violated, []string{"OBI-D-01"}) {
				t.Fatalf("conclusion %s violated %v, want non-conformant on OBI-D-01 alone", report.Conclusion, report.Violated)
			}
			if len(report.Inconclusive) != len(documentRules)-1 {
				t.Fatalf("inconclusive = %v, want every other rule", report.Inconclusive)
			}
		})
	}
}

// Value validation reaches a verdict only where the document's own schemas
// decide it (OBI-T-08): a graph reaching a resource the document does not
// embed, or a reference that resolves nowhere, reaches none.
func TestInputContract_Scope(t *testing.T) {
	for name, tc := range map[string]struct{ document, want string }{
		"a graph reaching an external resource": {`{"openbindings":"0.2.0","operations":{"a":{"input":{"$ref":"https://schemas.example.com/task.json"}}}}`, "no verdict"},
		"an unrelated external reference": {`{"openbindings":"0.2.0",
			"schemas":{"Remote":{"$ref":"https://schemas.example.com/remote.json"}},
			"operations":{"a":{"input":{"type":"string"}}}}`, "mismatch"},
		"a reference through the schemas map": {`{"openbindings":"0.2.0",
			"schemas":{"Title":{"type":"string"}},
			"operations":{"a":{"input":{"$ref":"#/schemas/Title"}}}}`, "mismatch"},
		"a reference into an embedded schema resource": {`{"openbindings":"0.2.0",
			"schemas":{"Title":{"$id":"https://schemas.example.com/title.json","type":"string"}},
			"operations":{"a":{"input":{"$ref":"https://schemas.example.com/title.json"}}}}`, "mismatch"},
		"an unresolvable reference": {`{"openbindings":"0.2.0","operations":{"a":{"input":{"$ref":"#/schemas/Missing"}}}}`, "no verdict"},
	} {
		if got := inputVerdict(t, tc.document, "a", json.Number("42")); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestValidateDocument_ReferencesInsideASchemaResourceAreItsOwn(t *testing.T) {
	withInput := func(input string) string {
		return `{"openbindings":"0.2.0","operations":{"a":{"input":` + input + `}}}`
	}
	tests := []struct {
		name  string
		input string
		want  RuleEvidenceStatus
	}{
		{"relative $ref beside the $id", `{"$id":"https://example.com/s/task.json","$ref":"person.json"}`, EvidenceSatisfied},
		{"relative $ref below the $id", `{"$id":"https://example.com/s/task.json","properties":{"owner":{"$ref":"person.json"}}}`, EvidenceSatisfied},
		{"relative $ref at an OBI position", `{"properties":{"owner":{"$ref":"person.json"}}}`, EvidenceViolated},
		// Inside a resource, references and nested $ids resolve per JSON Schema,
		// exactly as for an externally fetched schema: they are not
		// OBI-defined references, so OBI-D-05 judges neither form nor
		// well-formedness there (§7).
		{"malformed $ref inside a resource", `{"$id":"https://example.com/s/task.json","properties":{"owner":{"$ref":"per son.json"}}}`, EvidenceSatisfied},
		{"malformed nested $id", `{"$id":"https://example.com/s/task.json","properties":{"owner":{"$id":"per son.json"}}}`, EvidenceSatisfied},
		{"dynamic pair inside a resource", `{"$id":"https://example.com/s/tree.json","$dynamicAnchor":"n","items":{"$dynamicRef":"#n"}}`, EvidenceSatisfied},
		{"malformed $id at an OBI position", `{"$id":"https://example.com/s/ta sk.json"}`, EvidenceViolated},
		{"unparseable $id at an OBI position", `{"$id":"https://[::1"}`, EvidenceViolated},
		{"relative $id at an OBI position", `{"$id":"task.json"}`, EvidenceViolated},
		// A same-document reference is well-formed; OBI-D-12 judges its pointer.
		{"invalid pointer escape at an OBI position", `{"$ref":"#/$defs/~2","$defs":{"~2":{}}}`, EvidenceSatisfied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := mustValidateDocument(t, withInput(tt.input))
			if got := report.Evidence["OBI-D-05"]; got != tt.want {
				t.Fatalf("OBI-D-05 = %s, want %s; findings %+v", got, tt.want, report.Findings)
			}
		})
	}
}

func TestValidateDocument_WhitespaceAroundTheVersionViolatesD12(t *testing.T) {
	for _, version := range []string{" 0.2.0", "0.2.0 ", "0.2.0\n"} {
		report := mustValidateDocument(t, `{"openbindings":`+strconv.Quote(version)+`,"operations":{}}`)
		if report.Evidence["OBI-D-09"] != EvidenceViolated {
			t.Fatalf("%q: OBI-D-09 = %s, want violated", version, report.Evidence["OBI-D-09"])
		}
	}
}

func TestFindingPaths_AreJSONPointers(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0",
		"schemas":{"T":{"type":"object","properties":{"a/b~c":{"minLength":"3"}}}},
		"operations":{"a":{"output":{"$ref":"#/schemas/T"},"examples":{"one":{"output":{"n":"x"}}}},
		              "b":{"input":{"type":"object","properties":{"n":{"type":"integer"}}},"examples":{"two":{"input":{"n":"x"}}}}},
		"sources":{"s":{"kind":""}}}`)
	want := map[string]string{
		"/schemas/T/properties/a~1b~0c/minLength": "OBI-D-10",
		"/sources/s/kind":                         "OBI-D-02",
	}
	got := map[string]string{}
	for _, finding := range report.Violations() {
		got[finding.Path] = finding.Rule
	}
	for path, rule := range want {
		if got[path] != rule {
			t.Fatalf("want %s at %q; violations %+v", rule, path, report.Violations())
		}
	}
	root := mustValidateDocument(t, `{"operations":{}}`)
	var rootFinding bool
	for _, finding := range root.Violations() {
		if finding.Rule == "OBI-D-02" && finding.Path == "" && strings.Contains(finding.Message, "'openbindings'") {
			rootFinding = true
		}
	}
	if !rootFinding {
		t.Fatalf("a missing top-level member is reported at the document, the empty pointer; got %+v", root.Violations())
	}
}

func TestParseDocument_RefusesBeforeApplyingTheSchema(t *testing.T) {
	var refusal *VersionRefusalError
	if _, err := ParseDocument([]byte(`{"openbindings":"0.3.0","name":"future"}`)); !errors.As(err, &refusal) {
		t.Fatalf("an unsupported version is refused, not judged by the 0.2 schema; got %v", err)
	}
	data := []byte(`{"openbindings":"0.2.0","operations":{},"sources":{"s":{"kind":""}}}`)
	_, parseErr := ParseDocument(data)
	_, _, validateErr := ValidateDocument(data, ValidateOptions{})
	var parsed, validated *ValidationError
	if !errors.As(parseErr, &parsed) || !errors.As(validateErr, &validated) {
		t.Fatalf("want ValidationErrors, got %v and %v", parseErr, validateErr)
	}
	if !reflect.DeepEqual(parsed.Findings, validated.Findings) {
		t.Fatalf("ParseDocument and ValidateDocument word the same OBI-D-02 violation differently:\n%+v\n%+v", parsed.Findings, validated.Findings)
	}
}

func TestValidate_GatesOnTheDocumentSchema(t *testing.T) {
	// A present empty version violates only the document schema; the model
	// carries it, so Validate sees it.
	iface := &Interface{
		OpenBindings: "0.2.0",
		Version:      Present(""),
		Operations:   map[string]Operation{"op": {}},
	}
	if _, err := iface.Validate(ValidateOptions{}); err == nil {
		t.Fatal("a present empty version violates the document schema")
	}
}

// Every rule is decided on the bytes, whether or not the document model can
// carry them: a document typed decoding refuses is still judged in full.
func TestValidateDocument_JudgesDocumentsTheModelCannotCarry(t *testing.T) {
	iface, report, _ := ValidateDocument([]byte(`{"openbindings":"0.2.0",
		"operations":{"a":{"input":null,"output":{"$ref":"./local.json"},"description":null}},
		"sources":{"s":{"kind":"x@1"}},
		"bindings":{"b":{"operation":"missing","source":"s"}}}`), ValidateOptions{})

	if iface != nil {
		t.Fatal("the model cannot carry a null input; no Interface is returned")
	}
	for rule, want := range map[string]RuleEvidenceStatus{
		"OBI-D-02": EvidenceViolated,
		"OBI-D-05": EvidenceViolated,
		"OBI-D-07": EvidenceViolated,
		"OBI-D-09": EvidenceSatisfied,
		"OBI-D-10": EvidenceViolated,
		"OBI-D-11": EvidenceSatisfied,
	} {
		if got := report.Evidence[rule]; got != want {
			t.Errorf("%s = %s, want %s", rule, got, want)
		}
	}
}

// Each rule is judged literally on the values present: a member of the wrong
// JSON type is OBI-D-02's violation, and also violates a rule whose predicate
// it fails, while a rule whose domain excludes it has nothing to judge there.
func TestValidateDocument_WrongTypedMembersAreJudgedLiterally(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":5,
		"sources":{"s":{"kind":"x@1"}},
		"bindings":{"b":{"operation":"a","source":42}}}`)
	found := map[string]RuleEvidenceStatus{}
	for _, finding := range report.Findings {
		found[finding.Rule+" "+finding.Path] = finding.Status
	}
	for key, want := range map[string]RuleEvidenceStatus{
		"OBI-D-02 /operations":           EvidenceViolated,
		"OBI-D-07 /bindings/b/operation": EvidenceViolated,
		"OBI-D-08 /bindings/b/source":    EvidenceViolated,
	} {
		if got := found[key]; got != want {
			t.Errorf("%s = %q, want %s; findings %+v", key, got, want, report.Findings)
		}
	}
	for _, rule := range []string{"OBI-D-03", "OBI-D-04", "OBI-D-10", "OBI-D-12", "OBI-D-13"} {
		if report.Evidence[rule] != EvidenceSatisfied {
			t.Errorf("%s = %s: an operations member that is not an object holds nothing it judges", rule, report.Evidence[rule])
		}
	}
}

// Value validation follows an absolute reference with a fragment into a
// resource the document embeds.
func TestInputContract_ThroughAFragmentIntoAnEmbeddedResource(t *testing.T) {
	document := `{"openbindings":"0.2.0",
		"schemas":{"T":{"$id":"https://example.com/t","$defs":{"S":{"type":"string"}}}},
		"operations":{"a":{"input":{"$ref":"https://example.com/t#/$defs/S"}}}}`
	if got := inputVerdict(t, document, "a", json.Number("42")); got != "mismatch" {
		t.Fatalf("%s, want mismatch", got)
	}
}

// A document schema failure on a map key is located at the key. Only the
// key's own token is asserted: santhosh-tekuri/jsonschema v6.0.3 records a
// propertyNames failure's location without copying it, so a later sibling can
// overwrite the tokens above the key.
func TestValidateDocument_KeyFindingsAreLocatedAtTheKey(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a/b~c":{}}}`)
	found := false
	for _, finding := range report.Findings {
		if finding.Rule != "OBI-D-02" {
			continue
		}
		found = true
		if !strings.HasSuffix(finding.Path, "/a~1b~0c") {
			t.Fatalf("OBI-D-02 finding at %q, want it located at the key", finding.Path)
		}
	}
	if !found {
		t.Fatal("want an OBI-D-02 finding for the key")
	}
}

// OBI-D-06 governs every schema in the document, inside schema resources too:
// a resource's internal business is reference resolution, not the document's
// dialect. $vocabulary is JSON Schema's to judge, not a document rule's.
func TestValidateDocument_DialectRulesReachEverySchema(t *testing.T) {
	for name, input := range map[string]string{
		"$schema at an OBI position": `{"$schema":"http://json-schema.org/draft-07/schema#"}`,
		"$schema inside a resource":  `{"$id":"https://example.com/s","properties":{"a":{"$schema":"http://json-schema.org/draft-07/schema#"}}}`,
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{"input":`+input+`}}}`)
		if report.Evidence["OBI-D-06"] != EvidenceViolated {
			t.Errorf("%s: OBI-D-06 = %s, want violated", name, report.Evidence["OBI-D-06"])
		}
	}
	for _, input := range []string{
		`{"$schema":"https://json-schema.org/draft/2020-12/schema"}`,
		`{"$schema":"https://json-schema.org/draft/2020-12/schema#"}`,
		`{"$vocabulary":{}}`,
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{"input":`+input+`}}}`)
		if report.Conclusion != ConclusionConformant {
			t.Errorf("%s: conclusion %s; findings %+v", input, report.Conclusion, report.Findings)
		}
	}
}

// Reference cycles terminate in every walk (OBI-T-06): a recursive type is
// evaluated, and a cycle that never advances into the value reaches no
// verdict rather than hanging (OBI-T-08).
func TestValidateDocument_ReferenceCyclesTerminate(t *testing.T) {
	recursive := `{"openbindings":"0.2.0",
		"schemas":{"Node":{"type":"object","properties":{"next":{"$ref":"#/schemas/Node"}}}},
		"operations":{"a":{"input":{"$ref":"#/schemas/Node"}}}}`
	if got := inputVerdict(t, recursive, "a", map[string]any{"next": map[string]any{"next": json.Number("5")}}); got != "mismatch" {
		t.Fatalf("a recursive type: %s, want mismatch", got)
	}
	loop := `{"openbindings":"0.2.0",
		"schemas":{"A":{"$ref":"#/schemas/B"},"B":{"$ref":"#/schemas/A"}},
		"operations":{"a":{"input":{"$ref":"#/schemas/A"}}}}`
	if report := mustValidateDocument(t, loop); report.Conclusion != ConclusionConformant {
		t.Fatalf("a reference cycle is permitted: %s; findings %+v", report.Conclusion, report.Findings)
	}
	if got := inputVerdict(t, loop, "a", json.Number("1")); got != "no verdict" {
		t.Fatalf("a pure reference loop: %s, want no verdict", got)
	}
}

// The graph value validation evaluates is what evaluation applies: an
// unreferenced definition is not part of it, a plain-name anchor inside an
// embedded resource resolves, and a graph reaching an $id more than one schema
// declares reaches no verdict.
func TestInputContract_GraphIsWhatEvaluationApplies(t *testing.T) {
	for name, document := range map[string]string{
		"unreferenced external definition": `{"openbindings":"0.2.0","operations":{"op":{
			"input":{"type":"string","$defs":{"dead":{"$ref":"https://outside.example/x"}}}}}}`,
		"anchor inside a resource": `{"openbindings":"0.2.0","operations":{"op":{
			"input":{"$id":"https://e.test/S","$ref":"#string","$defs":{"s":{"$anchor":"string","type":"string"}}}}}}`,
		"conflicting ids elsewhere": `{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.test/S"},"B":{"$id":"https://e.test/S"}},
			"operations":{"op":{"input":{"type":"string"}}}}`,
	} {
		if got := inputVerdict(t, document, "op", json.Number("7")); got != "mismatch" {
			t.Errorf("%s: %s, want mismatch", name, got)
		}
	}
	ambiguous := `{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.test/S"},"B":{"$id":"https://e.test/S"}},
		"operations":{"op":{"input":{"$ref":"https://e.test/S"}}}}`
	if got := inputVerdict(t, ambiguous, "op", json.Number("7")); got != "no verdict" {
		t.Fatalf("a graph reaching an ambiguous $id: %s, want no verdict", got)
	}
}

// OBI-D-05 holds URI-form references to RFC 3986's grammar, not a character
// screen.
func TestValidateDocument_ReferencesFollowTheURIGrammar(t *testing.T) {
	for name, tt := range map[string]struct {
		input string
		want  RuleEvidenceStatus
	}{
		"bracket in a fragment":           {`{"$ref":"#/schemas/A/properties/a[0]"}`, EvidenceViolated},
		"second # in a fragment":          {`{"$ref":"#/schemas/A#b"}`, EvidenceViolated},
		"percent-encoded registered name": {`{"$ref":"https://%41.example/s.json"}`, EvidenceSatisfied},
		"IPv6 literal host":               {`{"$ref":"https://[::1]/s.json"}`, EvidenceSatisfied},
		"unterminated IPv6 literal":       {`{"$ref":"https://[::1/s.json"}`, EvidenceViolated},
		"IPvFuture literal":               {`{"$ref":"https://[v1F.a:b]/s.json"}`, EvidenceSatisfied},
		"IPvFuture non-ASCII version":     {`{"$ref":"https://[vŁ.a]/s.json"}`, EvidenceViolated},
		"non-string $ref":                 {`{"$ref":42}`, EvidenceViolated},
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","schemas":{"A":{}},"operations":{"a":{"input":`+tt.input+`}}}`)
		if got := report.Evidence["OBI-D-05"]; got != tt.want {
			t.Errorf("%s: OBI-D-05 = %s, want %s; findings %+v", name, got, tt.want, report.Findings)
		}
	}
}

// A same-document reference in the document resource identifies a schema at
// an OBI position (OBI-D-12): not the OBI document, a value that is not a
// schema, or a location inside a schema that declares $id. OBI positions
// include the legacy definitions and dependencies. An absolute URI is outside
// the rule: JSON Schema resolves it.
func TestValidateDocument_ReferencesReachSchemaPlaces(t *testing.T) {
	d12 := func(document string) []Finding {
		var out []Finding
		for _, finding := range mustValidateDocument(t, document).Findings {
			if finding.Rule == "OBI-D-12" {
				out = append(out, finding)
			}
		}
		return out
	}
	for name, document := range map[string]string{
		"the document root":        `{"openbindings":"0.2.0","operations":{"a":{"input":{"$ref":"#"}}}}`,
		"the empty reference":      `{"openbindings":"0.2.0","operations":{"a":{"input":{"$ref":""}}}}`,
		"a string":                 `{"openbindings":"0.2.0","name":"Task Manager","operations":{"a":{"input":{"$ref":"#/name"}}}}`,
		"x- data":                  `{"openbindings":"0.2.0","x-s":{"T":{"type":"object"}},"operations":{"a":{"input":{"$ref":"#/x-s/T"}}}}`,
		"into a resource":          `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://e.com/t","properties":{"i":{}}}},"operations":{"a":{"input":{"$ref":"#/schemas/T/properties/i"}}}}`,
		"a map of schemas":         `{"openbindings":"0.2.0","schemas":{"T":{"properties":{"i":{}}}},"operations":{"a":{"input":{"$ref":"#/schemas/T/properties"}}}}`,
		"a dependencies array":     `{"openbindings":"0.2.0","schemas":{"T":{"dependencies":{"a":["b"]}}},"operations":{"a":{"input":{"$ref":"#/schemas/T/dependencies/a"}}}}`,
		"an allOf object's member": `{"openbindings":"0.2.0","schemas":{"T":{"allOf":{"x":{}}}},"operations":{"a":{"input":{"$ref":"#/schemas/T/allOf/x"}}}}`,
		"a properties array entry": `{"openbindings":"0.2.0","schemas":{"T":{"properties":[{}]}},"operations":{"a":{"input":{"$ref":"#/schemas/T/properties/0"}}}}`,
		"through a boolean schema": `{"openbindings":"0.2.0","schemas":{"T":{"not":true}},"operations":{"a":{"input":{"$ref":"#/schemas/T/not/not"}}}}`,
		"a name declared nowhere":  `{"openbindings":"0.2.0","operations":{"a":{"input":{"$ref":"#nowhere"}}}}`,
		"a name inside a resource": `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://e.com/t","$anchor":"t"}},"operations":{"a":{"input":{"$ref":"#t"}}}}`,
	} {
		if got := d12(document); len(got) != 1 || got[0].Path != "/operations/a/input/$ref" {
			t.Errorf("%s: want one OBI-D-12 finding at the reference, got %+v", name, got)
		}
	}
	for name, document := range map[string]string{
		"another operation's input":                 `{"openbindings":"0.2.0","operations":{"a":{"input":{"type":"object"}},"b":{"input":{"$ref":"#/operations/a/input"}}}}`,
		"a subschema":                               `{"openbindings":"0.2.0","schemas":{"T":{"properties":{"i":{}}}},"operations":{"a":{"input":{"$ref":"#/schemas/T/properties/i"}}}}`,
		"a legacy definitions entry":                `{"openbindings":"0.2.0","schemas":{"T":{"definitions":{"I":{}}}},"operations":{"a":{"input":{"$ref":"#/schemas/T/definitions/I"}}}}`,
		"a legacy dependencies schema":              `{"openbindings":"0.2.0","schemas":{"T":{"dependencies":{"a":{}}}},"operations":{"a":{"input":{"$ref":"#/schemas/T/dependencies/a"}}}}`,
		"a subschema by $id":                        `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://e.com/t","properties":{"i":{}}}},"operations":{"a":{"input":{"$ref":"https://e.com/t#/properties/i"}}}}`,
		"a non-schema by $id":                       `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://e.com/t","x":1}},"operations":{"a":{"input":{"$ref":"https://e.com/t#/x"}}}}`,
		"a resource by $id":                         `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://e.com/t"}},"operations":{"a":{"input":{"$ref":"https://e.com/t"}}}}`,
		"a schemas entry declaring $id, by pointer": `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://e.com/t"}},"operations":{"a":{"input":{"$ref":"#/schemas/T"}}}}`,
		"a plain name":                              `{"openbindings":"0.2.0","schemas":{"T":{"$anchor":"t"}},"operations":{"a":{"input":{"$ref":"#t"}}}}`,
		"a plain name in definitions":               `{"openbindings":"0.2.0","schemas":{"T":{"definitions":{"I":{"$anchor":"i"}}}},"operations":{"a":{"input":{"$ref":"#i"}}}}`,
	} {
		if got := d12(document); len(got) != 0 {
			t.Errorf("%s: want no OBI-D-12 finding, got %+v", name, got)
		}
	}
}

// No plain name is declared twice in the document resource, each $anchor and
// $dynamicAnchor counting once, and no two schemas the document contains
// declare the same $id (OBI-D-13): each declaration is a finding.
func TestValidateDocument_DuplicateIdentifiersViolateD13(t *testing.T) {
	for name, tc := range map[string]struct {
		document string
		paths    []string
	}{
		"two $ids": {`{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.com/t"},"B":{"$id":"https://e.com/t"}},"operations":{}}`,
			[]string{"/schemas/A/$id", "/schemas/B/$id"}},
		"a nested $id resolving to another": {`{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.com/a/b"},"B":{"$id":"https://e.com/a/c","$defs":{"x":{"$id":"b"}}}},"operations":{}}`,
			[]string{"/schemas/A/$id", "/schemas/B/$defs/x/$id"}},
		"dot segments removed": {`{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.com/t"},"B":{"$id":"https://e.com/x/../t"}},"operations":{}}`,
			[]string{"/schemas/A/$id", "/schemas/B/$id"}},
		"two schemas declaring one name": {`{"openbindings":"0.2.0","schemas":{"A":{"$anchor":"n"},"B":{"$anchor":"n"}},"operations":{}}`,
			[]string{"/schemas/A/$anchor", "/schemas/B/$anchor"}},
		"one schema declaring a name both ways": {`{"openbindings":"0.2.0","schemas":{"A":{"$anchor":"n","$dynamicAnchor":"n"}},"operations":{}}`,
			[]string{"/schemas/A/$anchor", "/schemas/A/$dynamicAnchor"}},
	} {
		var paths []string
		for _, finding := range mustValidateDocument(t, tc.document).Violations() {
			if finding.Rule == "OBI-D-13" {
				paths = append(paths, finding.Path)
			}
		}
		slices.Sort(paths)
		if !reflect.DeepEqual(paths, tc.paths) {
			t.Errorf("%s: OBI-D-13 findings at %v, want %v", name, paths, tc.paths)
		}
	}
	for name, document := range map[string]string{
		"spellings a URI library would merge": `{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.com/t"},"B":{"$id":"HTTPS://e.com/t"}},"operations":{}}`,
		"one name in two resources":           `{"openbindings":"0.2.0","schemas":{"A":{"$anchor":"n"},"B":{"$id":"https://e.com/b","$anchor":"n"}},"operations":{}}`,
		"a malformed nested $id":              `{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.com/a","$defs":{"x":{"$id":"b c"},"y":{"$id":"b c"}}}},"operations":{}}`,
	} {
		if report := mustValidateDocument(t, document); report.Evidence["OBI-D-13"] != EvidenceSatisfied {
			t.Errorf("%s: OBI-D-13 = %s; findings %+v", name, report.Evidence["OBI-D-13"], report.Findings)
		}
	}
}

// An absolute reference is outside OBI-D-12, whatever it names: JSON Schema
// resolves it, and value validation reaches a verdict only where it resolves
// within the document.
func TestValidateDocument_AbsoluteReferencesAreJSONSchemas(t *testing.T) {
	document := func(ref string) string {
		return `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://example.com/t","$defs":{"S":{"type":"string"}}}},
			"operations":{"a":{"input":{"$ref":"` + ref + `"}}}}`
	}
	for ref, verdict := range map[string]string{
		"https://example.com/t#/$defs/S":       "mismatch",
		"https://example.com/t#/$defs/Missing": "no verdict",
		"https://other.example/x#/nope":        "no verdict",
	} {
		if got := mustValidateDocument(t, document(ref)).Evidence["OBI-D-12"]; got != EvidenceSatisfied {
			t.Errorf("%s: OBI-D-12 = %s", ref, got)
		}
		if got := inputVerdict(t, document(ref), "a", json.Number("42")); got != verdict {
			t.Errorf("%s: %s, want %s", ref, got, verdict)
		}
	}
}

// A resource limit is not evidence of a violation (§10.4). A number the
// schema library would read beyond the numeric limits leaves the schema
// unevaluable, so a value reaches no verdict, though the schema is judged
// well-formed: the meta-schemas are checked against a stand-in.
func TestValidateDocument_ResourceLimitsAreInconclusive(t *testing.T) {
	document := `{"openbindings":"0.2.0","operations":{"a":{"input":{"minLength":1e999999}}}}`
	if report := mustValidateDocument(t, document); report.Evidence["OBI-D-10"] != EvidenceSatisfied {
		t.Fatalf("OBI-D-10 = %s; findings %+v", report.Evidence["OBI-D-10"], report.Findings)
	}
	// Whether an evaluator reads such a number is its own limit: the
	// conformance kit checks one (openbindingstest).
}

// A document schema finding about a map key is located at the key, the same
// way every time.
func TestValidateDocument_KeyFindingPathsAreDeterministic(t *testing.T) {
	t.Skip("santhosh-tekuri/jsonschema v6.0.3 records a propertyNames failure's location without copying it, so a later sibling overwrites it; fixed upstream by cloning the location")
	for range 50 {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"schemas":{"bad key":{}},"sources":{},"name":"n","description":"d"}`)
		for _, finding := range report.Findings {
			if finding.Rule == "OBI-D-02" && finding.Path != "/schemas/bad key" {
				t.Fatalf("OBI-D-02 finding at %q", finding.Path)
			}
		}
	}
}

// The version is read, and an unsupported one refused, even from input
// OBI-D-01 refuses.
func TestValidateDocument_RefusesVersionsBeforeJudgingBytes(t *testing.T) {
	if _, _, err := ValidateDocument([]byte(`{"openbindings":"9.0.0","operations":{},"a":1,"a":2}`), ValidateOptions{}); !errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("want a version refusal, got %v", err)
	}
	if _, err := ParseDocument([]byte(`{"openbindings":"9.0.0","operations":{},"a":1,"a":2}`)); !errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("ParseDocument: want a version refusal, got %v", err)
	}
}

// A version is read before OBI-D-01 only where it is established: a repeated
// openbindings member declares none.
func TestValidateDocument_RepeatedVersionIsNotRead(t *testing.T) {
	_, report, err := ValidateDocument([]byte(`{"openbindings":"0.2.0","openbindings":"0.3.0","operations":{}}`), ValidateOptions{})
	if errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("a repeated openbindings member establishes no version: %v", err)
	}
	if report.Evidence["OBI-D-01"] != EvidenceViolated {
		t.Fatalf("OBI-D-01 = %s", report.Evidence["OBI-D-01"])
	}
}

// OBI-D-12 judges every same-document fragment after percent-decoding it once,
// and a plain name the document resource declares twice still qualifies,
// though OBI-D-13 reports the repetition.
func TestValidateDocument_ReferenceResolutionIsJudgedForEveryFragment(t *testing.T) {
	for ref, want := range map[string]RuleEvidenceStatus{
		"#/schemas/Nope%20x":    EvidenceViolated,
		"#/schemas/T%61sk":      EvidenceSatisfied,
		"#%2Fschemas%2FTask":    EvidenceSatisfied,
		"#/schemas/T%2561sk":    EvidenceViolated,
		"#/schemas/~2":          EvidenceViolated,
		"#nothere":              EvidenceViolated,
		"#t%61sk":               EvidenceSatisfied,
		"#%FF":                  EvidenceViolated,
		"https://ex.test/a#dup": EvidenceSatisfied,
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","schemas":{"Task":{"$anchor":"task"}},"operations":{"a":{"input":{"$ref":"`+ref+`"}}}}`)
		if got := report.Evidence["OBI-D-12"]; got != want {
			t.Errorf("%s: OBI-D-12 = %s, want %s", ref, got, want)
		}
	}
	report := mustValidateDocument(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$anchor":"dup"},"B":{"$anchor":"dup"}},
		"operations":{"a":{"input":{"$ref":"#dup"}}}}`)
	if report.Evidence["OBI-D-12"] != EvidenceSatisfied || report.Evidence["OBI-D-13"] != EvidenceViolated {
		t.Fatalf("a name declared twice: OBI-D-12 = %s, OBI-D-13 = %s", report.Evidence["OBI-D-12"], report.Evidence["OBI-D-13"])
	}
}

// ParseDocument checks a document whatever numbers it holds: one missing a
// required member is refused, and a number beyond the numeric limits where
// an alias belongs is no alias.
func TestParseDocument_ChecksNumbersBeyondTheLimits(t *testing.T) {
	if _, err := ParseDocument([]byte(`{"openbindings":"0.2.0","x-padding":1e10001}`)); !errors.As(err, new(*ValidationError)) {
		t.Fatalf("a missing operations member is an OBI-D-02 violation, got %v", err)
	}
	alias := `{"openbindings":"0.2.0","operations":{"a":{"aliases":["b",1e10001]}}}`
	if iface, err := ParseDocument([]byte(alias)); iface != nil || !errors.As(err, new(*ValidationError)) || !strings.Contains(err.Error(), "/operations/a/aliases/1") {
		t.Fatalf("a number is no alias: %v", err)
	}
	if _, err := ParseDocument([]byte(`{"a":1,"a":2}`)); !errors.As(err, new(*ValidationError)) || !strings.Contains(err.Error(), "OBI-D-01") {
		t.Fatalf("an OBI-D-01 violation is a *ValidationError, got %T %v", err, err)
	}
}

// Input nested deeper than the decoder reads is still read in full for
// OBI-D-01, a token at a time, but cannot be decoded: every rule but OBI-D-09
// meets a resource limit and is inconclusive (§10.4). Its declared version is
// read however deep the input and wherever the member lies, so an unsupported
// one is refused (OBI-T-04) and a missing or malformed one violates OBI-D-09.
func TestValidateDocument_NestingLimitIsInconclusive(t *testing.T) {
	nested := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	deep := `{"openbindings":"0.2.0","operations":{},"x-deep":` + nested + `}`
	_, report, err := ValidateDocument([]byte(deep), ValidateOptions{})
	if err != nil || report.Evidence["OBI-D-01"] != EvidenceSatisfied || report.Evidence["OBI-D-09"] != EvidenceSatisfied || report.Evidence["OBI-D-02"] != EvidenceInconclusive || report.Conclusion != ConclusionConformanceUndetermined {
		t.Fatalf("err %v, OBI-D-01 %q, OBI-D-09 %q, OBI-D-02 %q, conclusion %q", err, report.Evidence["OBI-D-01"], report.Evidence["OBI-D-09"], report.Evidence["OBI-D-02"], report.Conclusion)
	}
	for member, want := range map[string]Finding{
		``:                                 {Rule: "OBI-D-09", Status: EvidenceViolated, Message: "missing the required openbindings member"},
		`"openbindings":"0.2",`:            {Rule: "OBI-D-09", Status: EvidenceViolated, Path: "/openbindings", Message: `"0.2" is not a valid SemVer 2.0.0 string`},
		`"openbindings":[` + nested + `],`: {Rule: "OBI-D-09", Status: EvidenceViolated, Path: "/openbindings", Message: "must be a SemVer 2.0.0 string; got array"},
	} {
		_, report, err := ValidateDocument([]byte(`{`+member+`"operations":{},"x-deep":`+nested+`}`), ValidateOptions{})
		if !errors.As(err, new(*ValidationError)) || !reflect.DeepEqual(report.Violations(), []Finding{want}) {
			t.Errorf("%.40s: violations %+v", member, report.Violations())
		}
	}
	for name, input := range map[string]string{
		"a repeated name before it": `{"openbindings":"0.2.0","a":1,"a":2,"x-deep":` + nested + `}`,
		"a repeated name inside it": `{"openbindings":"0.2.0","x-deep":` + strings.Repeat("[", 10001) + `{"a":1,"a":2}` + strings.Repeat("]", 10001) + `}`,
		"a syntax error after it":   `{"openbindings":"0.2.0","x-deep":` + nested + `,"a":}`,
		"trailing data":             `{"openbindings":"0.2.0","x-deep":` + nested + `} []`,
	} {
		if _, report, err := ValidateDocument([]byte(input), ValidateOptions{}); !errors.As(err, new(*ValidationError)) || report.Evidence["OBI-D-01"] != EvidenceViolated {
			t.Errorf("%s: OBI-D-01 %q, err %v", name, report.Evidence["OBI-D-01"], err)
		}
	}
	if _, err := ParseDocument([]byte(deep)); err == nil || errors.As(err, new(*ValidationError)) {
		t.Fatalf("want a refusal that is not a violation, got %v", err)
	}
	unsupported := `{"x-deep":` + nested + `,"operations":{},"openbindings":"0.9.0"}`
	if _, _, err := ValidateDocument([]byte(unsupported), ValidateOptions{}); !errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("ValidateDocument: want a version refusal, got %v", err)
	}
	if _, err := ParseDocument([]byte(unsupported)); !errors.As(err, new(*VersionRefusalError)) {
		t.Fatalf("ParseDocument: want a version refusal, got %v", err)
	}
}

// The version is read from exactly one JSON value, with no byte-order mark,
// whose root object has one openbindings member holding a string, at any
// depth (OBI-T-04).
func FuzzDeclaredVersion(f *testing.F) {
	for _, seed := range []string{`{"openbindings":"0.9.0"}`, `{"openbindings":"0.9.0","openbindings":"0.9.0"}`, `{"a":[{"openbindings":"0.9.0"}],"openbindings":"1.0.0"}`,
		`{"openbindings":"0.9.0"} {}`, `{"openbindings":"0.9.0",}`, `{"\u006fpenbindings":"0.9.0"}`, `[{"openbindings":"0.9.0"}]`, `{"openbindings":{"a":1}}`, `{"openbindings":"0.9.0"`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if bytes.Count(data, []byte("["))+bytes.Count(data, []byte("{")) >= 10000 {
			return // past the depth json.Valid reads, the reference below does not apply
		}
		got, gotDeclared := declaredVersion(data)
		want, wantDeclared := "", false
		if json.Valid(data) { // splitObject reads valid JSON only; a byte-order mark is not JSON
			if entries, err := splitObject(data); err == nil {
				var declared []json.RawMessage
				for _, entry := range entries {
					if entry.name == "openbindings" {
						declared = append(declared, entry.value)
					}
				}
				if len(declared) == 1 && declared[0][0] == '"' {
					want, _ = exactString(declared[0])
					wantDeclared = true
				}
			}
		}
		if got != want || gotDeclared != wantDeclared {
			t.Fatalf("%q: read %q, %v; want %q, %v", data, got, gotDeclared, want, wantDeclared)
		}
	})
}

// A number beyond the numeric limits is never handed to the schema library:
// a preference holding one is decided exactly, and any other is checked as a
// stand-in the document schema cannot tell from it, so every violation is
// found and no finding is lost.
func TestValidateDocument_NumbersBeyondTheLimitsAreChecked(t *testing.T) {
	var items []string
	for i := range 21 { // more than 20 items, which the library hashes
		items = append(items, fmt.Sprintf(`"a%d"`, i))
	}
	list := strings.Join(append(items, "1e1000001"), ",")
	for name, tc := range map[string]struct {
		document string
		violated []string
	}{
		"an alias": {
			document: `{"openbindings":"0.2.0","name":5,"operations":{"op":{"aliases":[` + list + `]}}}`,
			violated: []string{"/name", "/operations/op/aliases/21"},
		},
		"aliases equal only as numbers": {
			document: `{"openbindings":"0.2.0","operations":{"op":{"aliases":["a",1e99999,10e99998]}}}`,
			violated: []string{"/operations/op/aliases", "/operations/op/aliases/1", "/operations/op/aliases/2"},
		},
		"a kinds item": {
			document: `{"openbindings":"0.2.0","name":5,"operations":{"op":{}},"dependencies":{"d":{"operation":"op","kinds":[` + list + `]}}}`,
			violated: []string{"/dependencies/d/kinds/21", "/name"},
		},
		"an empty kinds item beside one": {
			document: `{"openbindings":"0.2.0","operations":{"op":{}},"dependencies":{"d":{"operation":"op","kinds":["",1e99999]}}}`,
			violated: []string{"/dependencies/d/kinds/0", "/dependencies/d/kinds/1"},
		},
		"a preference out of range": {
			document: `{"openbindings":"0.2.0","name":5,"operations":{"op":{}},"sources":{"s":{"kind":"x@1","content":{}}},
				"bindings":{"b":{"operation":"op","source":"s","preference":1e10001}}}`,
			violated: []string{"/bindings/b/preference", "/name"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, report, _ := ValidateDocument([]byte(tc.document), ValidateOptions{})
			var violated []string
			for _, finding := range report.Findings {
				if finding.Rule == "OBI-D-02" {
					if finding.Status != EvidenceViolated {
						t.Errorf("OBI-D-02 %s at %q: %s", finding.Status, finding.Path, finding.Message)
					}
					violated = append(violated, finding.Path)
				}
			}
			slices.Sort(violated)
			if !slices.Equal(violated, tc.violated) {
				t.Fatalf("OBI-D-02 violated at %v", violated)
			}
			if _, err := ParseDocument([]byte(tc.document)); !errors.As(err, new(*ValidationError)) {
				t.Fatalf("ParseDocument: want the violation, got %v", err)
			}
		})
	}

	inRange := `{"openbindings":"0.2.0","operations":{"op":{}},"sources":{"s":{"kind":"x@1","content":{}}},
		"bindings":{"b":{"operation":"op","source":"s","preference":1.` + strings.Repeat("0", 5000) + `}}}`
	if report := mustValidateDocument(t, inRange); report.Evidence["OBI-D-02"] != EvidenceSatisfied {
		t.Fatalf("a preference of 1 in 5002 characters: OBI-D-02 %q", report.Evidence["OBI-D-02"])
	}
	if iface, err := ParseDocument([]byte(inRange)); err != nil || Value(iface.Bindings["b"].Preference) != 1 {
		t.Fatalf("ParseDocument: %v", err)
	}
}

// A repeated member name is located at the object that repeats it.
func TestValidateDocument_DuplicateNamesAreLocated(t *testing.T) {
	document := []byte(`{"openbindings":"0.2.0","operations":{"op":{"examples":{"e":{"input":1,"input":2}}}}}`)
	want := Finding{Rule: "OBI-D-01", Status: EvidenceViolated, Path: "/operations/op/examples/e", Message: `repeats the member name "input"`}
	if _, report, _ := ValidateDocument(document, ValidateOptions{}); !reflect.DeepEqual(report.Violations(), []Finding{want}) {
		t.Fatalf("violations %+v", report.Violations())
	}
	var violation *ValidationError
	if _, err := ParseDocument(document); !errors.As(err, &violation) || !reflect.DeepEqual(violation.Findings, []Finding{want}) {
		t.Fatalf("ParseDocument: %v", err)
	}
}

// A leading byte-order mark is named as what OBI-D-01 refuses, and a text
// beginning with one declares no version (OBI-T-04), so it is never refused.
func TestValidateDocument_ByteOrderMarkIsNamed(t *testing.T) {
	for _, version := range []string{"0.2.0", "9.0.0"} {
		input := append([]byte{0xef, 0xbb, 0xbf}, `{"openbindings":"`+version+`","operations":{}}`...)
		_, report, err := ValidateDocument(input, ValidateOptions{})
		if errors.As(err, new(*VersionRefusalError)) {
			t.Fatalf("%s: a text with a byte-order mark declares no version: %v", version, err)
		}
		if violations := report.Violations(); len(violations) != 1 || !strings.Contains(violations[0].Message, "byte-order mark") {
			t.Fatalf("%s: violations %+v", version, violations)
		}
		if _, err := ParseDocument(input); !errors.As(err, new(*ValidationError)) {
			t.Fatalf("%s: ParseDocument: want the OBI-D-01 violation, got %v", version, err)
		}
	}
}

// A dialect other than 2020-12 violates OBI-D-06 alone: the meta-schemas
// check a $schema's type, and its format is an annotation (OBI-D-10).
func TestValidateDocument_DialectIsD06Alone(t *testing.T) {
	_, report, _ := ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{"a":{"input":{"properties":{"a":{"$schema":"http://json-schema.org/draft-07/schema#"}}}}}}`), ValidateOptions{})
	if !reflect.DeepEqual(report.Violated, []string{"OBI-D-06"}) {
		t.Fatalf("violated %v; findings %+v", report.Violated, report.Findings)
	}
}

// allocated returns the bytes f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// The work of validation stays linear in the document where hostile input
// once made it quadratic: setting many members aside, walking a deeply
// nested schema, and resolving many anchor references into one resource.
func TestValidateDocument_WorkIsLinear(t *testing.T) {
	scaled := func(name string, build func(n int) string) {
		t.Helper()
		small := []byte(build(1000))
		large := []byte(build(4000))
		ratio := float64(allocated(func() { ValidateDocument(large, ValidateOptions{}) })) / float64(allocated(func() { ValidateDocument(small, ValidateOptions{}) }))
		if ratio > 6 {
			t.Errorf("%s: 4 times the input allocated %.1f times the memory", name, ratio)
		}
	}
	scaled("members set aside", func(n int) string {
		var operations []string
		for i := range n {
			operations = append(operations, fmt.Sprintf(`"o%d":{"aliases":[1e10001]}`, i))
		}
		return `{"openbindings":"0.2.0","operations":{` + strings.Join(operations, ",") + `}}`
	})
	scaled("a chain of nested anchors", func(n int) string {
		chain := `{}`
		for i := range n {
			chain = fmt.Sprintf(`{"$anchor":"a%d","$defs":{"d":%s}}`, i, chain)
		}
		return `{"openbindings":"0.2.0","operations":{},"schemas":{"A":{"$id":"https://ex.test/a","$defs":{"c":` + chain + `}}}}`
	})
	scaled("anchor references", func(n int) string {
		var definitions, operations []string
		for i := range n {
			definitions = append(definitions, fmt.Sprintf(`"d%d":{"$anchor":"a%d"}`, i, i))
			operations = append(operations, fmt.Sprintf(`"o%d":{"input":{"$ref":"https://ex.test/a#x"}}`, i))
		}
		return `{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://ex.test/a","$anchor":"x","$defs":{` + strings.Join(definitions, ",") + `}}},"operations":{` + strings.Join(operations, ",") + `}}`
	})

	deep := `{"openbindings":"0.2.0","operations":{},"schemas":{"A":` + strings.Repeat(`{"not":`, 8000) + `{}` + strings.Repeat(`}`, 8000) + `}}`
	view, err := decodeDocumentBytes([]byte(deep))
	if err != nil {
		t.Fatal(err)
	}
	if bytes := allocated(func() { collectDocumentSchemas(view) }); bytes > 50*uint64(len(deep)) {
		t.Errorf("collecting the schemas of a %d-byte document allocated %d bytes", len(deep), bytes)
	}
	if bytes := allocated(func() { ValidateDocument([]byte(deep), ValidateOptions{}) }); bytes > 1000*uint64(len(deep)) {
		t.Errorf("validating a %d-byte document allocated %d bytes", len(deep), bytes)
	}
}

// A subschema nested deeper than the meta-schema validator checks quickly
// meets a resource limit: OBI-D-10 is inconclusive there, not decided
// (§10.4). What the schema holds above it is still checked.
func TestValidateDocument_WellFormednessHasADepthLimit(t *testing.T) {
	nested := strings.Repeat(`{"not":`, 300) + `{"type":42}` + strings.Repeat(`}`, 300)
	cut := "/schemas/A" + strings.Repeat("/not", 257)
	for _, tc := range []struct {
		schema   string
		evidence RuleEvidenceStatus
		violated []string
	}{
		{schema: nested, evidence: EvidenceInconclusive},
		{schema: `{"type":42,"not":` + nested + `}`, evidence: EvidenceViolated, violated: []string{"/schemas/A/type"}},
	} {
		_, report, _ := ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{},"schemas":{"A":`+tc.schema+`}}`), ValidateOptions{})
		var inconclusive, violated []string
		for _, finding := range report.Findings {
			switch {
			case finding.Rule != "OBI-D-10":
			case finding.Status == EvidenceViolated:
				violated = append(violated, finding.Path)
			default:
				inconclusive = append(inconclusive, finding.Path)
			}
		}
		if report.Evidence["OBI-D-10"] != tc.evidence || !slices.Equal(violated, tc.violated) || !slices.Equal(inconclusive, []string{cut}) {
			t.Fatalf("OBI-D-10 %q, violated at %v, inconclusive at %v", report.Evidence["OBI-D-10"], violated, inconclusive)
		}
	}
}

// A reference that is not a well-formed URI-reference is OBI-D-05's violation;
// it is no same-document reference for OBI-D-12 to judge.
func TestValidateDocument_MalformedReferencesAreD05s(t *testing.T) {
	for _, ref := range []string{"#/schemas/Missing Thing", "#/schemas/A B"} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","schemas":{"A B":{}},"operations":{"op":{"input":{"$ref":"`+ref+`"}}}}`)
		if report.Evidence["OBI-D-05"] != EvidenceViolated || report.Evidence["OBI-D-12"] != EvidenceSatisfied {
			t.Errorf("%s: OBI-D-05 %q, OBI-D-12 %q", ref, report.Evidence["OBI-D-05"], report.Evidence["OBI-D-12"])
		}
	}
}

// The depth limit counts nested subschemas only: data inside a schema, such
// as a const or a default, costs the schema library nothing and does not
// count, so it neither hides a violation nor leaves a graph unavailable.
func TestValidateDocument_DepthCountsSubschemasOnly(t *testing.T) {
	deep := strings.Repeat("[", 300) + strings.Repeat("]", 300)
	for schema, want := range map[string]RuleEvidenceStatus{
		`{"const":` + deep + `}`:             EvidenceSatisfied,
		`{"type":42,"default":` + deep + `}`: EvidenceViolated,
		`{"x-meta":` + deep + `}`:            EvidenceSatisfied,
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"schemas":{"A":`+schema+`}}`)
		if report.Evidence["OBI-D-10"] != want {
			t.Errorf("%.30s: OBI-D-10 %q, want %q", schema, report.Evidence["OBI-D-10"], want)
		}
	}
	document := `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"array","const":` + deep + `,"$defs":{"u":{"default":` + deep + `}}}}}}`
	contract, err := contractsFor(t, mustDecodeInterface(t, document)).CompileInput(context.Background(), "op")
	if err == nil {
		err = contract.Err()
	}
	if err != nil {
		t.Fatalf("deep data is no resource limit: %v", err)
	}
}

// A schema with an $id member is a boundary whatever the member's value (§7):
// the document-resource rules stop there, its anchors are its own, and a
// pointer from the document resource cannot reach inside it. An $id empty once
// its fragment is removed resolves to its base, a URI what encloses it
// already has, so OBI-D-13 finds a duplicate within a resource, and value
// validation reaches no verdict on a graph holding one.
func TestValidateDocument_AnIDMemberIsABoundary(t *testing.T) {
	for _, id := range []string{"", "#"} {
		document := `{"openbindings":"0.2.0","schemas":{"R":{"$id":"https://example.com/r","properties":{"a":{"$id":"` + id + `","type":"string"}}}},
			"operations":{"op":{"input":{"$ref":"https://example.com/r"}}}}`
		report := mustValidateDocument(t, document)
		if report.Evidence["OBI-D-12"] != EvidenceSatisfied || report.Evidence["OBI-D-13"] != EvidenceViolated {
			t.Errorf("$id %q: OBI-D-12 %q, OBI-D-13 %q", id, report.Evidence["OBI-D-12"], report.Evidence["OBI-D-13"])
		}
		if got := inputVerdict(t, document, "op", map[string]any{"a": json.Number("1")}); got != "no verdict" {
			t.Errorf("$id %q: %s, want no verdict", id, got)
		}
		document = `{"openbindings":"0.2.0","schemas":{"A":{"$id":"` + id + `","type":"string"}},"operations":{"op":{"input":{"$ref":"#/schemas/A"}}}}`
		if got := inputVerdict(t, document, "op", json.Number("1")); got != "no verdict" {
			t.Errorf("$id %q in the document resource: %s, want no verdict", id, got)
		}
	}
	for _, tc := range []struct {
		name, schemas string
		want          map[string]RuleEvidenceStatus
	}{
		{"its own references", `{"A":{"$id":"","$ref":"#missing"}}`, map[string]RuleEvidenceStatus{"OBI-D-05": EvidenceViolated, "OBI-D-12": EvidenceSatisfied}},
		{"references within", `{"A":{"$id":"#","properties":{"x":{"$ref":"#/nope"}}}}`, map[string]RuleEvidenceStatus{"OBI-D-12": EvidenceSatisfied}},
		{"its anchors", `{"A":{"$id":"#","$anchor":"t"},"B":{"$anchor":"t"}}`, map[string]RuleEvidenceStatus{"OBI-D-13": EvidenceSatisfied}},
		{"a pointer inside", `{"A":{"$id":"","properties":{"x":{}}},"B":{"$ref":"#/schemas/A/properties/x"}}`, map[string]RuleEvidenceStatus{"OBI-D-12": EvidenceViolated}},
		{"a pointer inside one that is not a string", `{"A":{"$id":42,"properties":{"x":{}}},"B":{"$ref":"#/schemas/A/properties/x"}}`, map[string]RuleEvidenceStatus{"OBI-D-10": EvidenceViolated, "OBI-D-12": EvidenceViolated}},
		{"the base for OBI-D-13", `{"A":{"$id":"https://x/a/","$defs":{"B":{"$id":42,"$defs":{"C":{"$id":"c"}}},"D":{"$id":"c"}}}}`, map[string]RuleEvidenceStatus{"OBI-D-10": EvidenceViolated, "OBI-D-13": EvidenceSatisfied}},
		{"the base for OBI-D-13, compared", `{"A":{"$id":"https://x/a/","$defs":{"B":{"$id":"b/","$defs":{"C":{"$id":"c"}}},"D":{"$id":"b/c"}}}}`, map[string]RuleEvidenceStatus{"OBI-D-13": EvidenceViolated}},
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"schemas":`+tc.schemas+`}`)
		for rule, want := range tc.want {
			if report.Evidence[rule] != want {
				t.Errorf("%s: %s %q, want %q; findings %+v", tc.name, rule, report.Evidence[rule], want, report.Findings)
			}
		}
	}
}

// A reference to a URI more than one schema declares names no one schema:
// OBI-D-13 reports the declarations, OBI-D-12 leaves the absolute reference
// to JSON Schema, and value validation reaches no verdict.
func TestValidateDocument_AmbiguousReferences(t *testing.T) {
	for _, fragment := range []string{"#/$defs/missing", "#/$defs/d"} {
		document := `{"openbindings":"0.2.0","schemas":{
			"A":{"$id":"https://example.com/a","$defs":{"d":{}}},
			"B":{"$id":"https://example.com/x/../a","$defs":{"d":{}}}},
			"operations":{"op":{"input":{"$ref":"https://example.com/a` + fragment + `"}}}}`
		report := mustValidateDocument(t, document)
		if report.Evidence["OBI-D-12"] != EvidenceSatisfied || report.Evidence["OBI-D-13"] != EvidenceViolated {
			t.Errorf("%s: OBI-D-12 %q, OBI-D-13 %q", fragment, report.Evidence["OBI-D-12"], report.Evidence["OBI-D-13"])
		}
		if got := inputVerdict(t, document, "op", json.Number("1")); got != "no verdict" {
			t.Errorf("%s: %s, want no verdict", fragment, got)
		}
	}
}

// Syntax errors in input deeper than encoding/json reads are described as
// encoding/json describes them in shallow input.
func TestValidateDocument_DeepSyntaxErrorsAreDescribed(t *testing.T) {
	open, closed := strings.Repeat("[", 10001), strings.Repeat("]", 10001)
	for input, want := range map[string]string{
		`{"openbindings":"0.2.0","x":` + open:                 "unexpected end of JSON input",
		`{"openbindings":"0.2.0","x":` + open + closed + `}]`: "invalid character ']' after top-level value",
	} {
		_, report, _ := ValidateDocument([]byte(input), ValidateOptions{})
		if violations := report.Violations(); len(violations) != 1 || !strings.Contains(violations[0].Message, want) {
			t.Errorf("want %q, got %+v", want, violations)
		}
	}
}

// Input deeper than encoding/json reads is judged for OBI-D-01 as the same
// input is at ordinary depth: wrapping a value in 10,001 arrays changes only
// where a repeated name or lone surrogate lies, and makes a document that
// OBI-D-01 accepts one the decoder cannot read. Only one JSON value is
// wrapped: wrapping anything else (nothing, or "1,2") can make valid JSON.
func FuzzDeepInput(f *testing.F) {
	for _, seed := range []string{`{}`, `{"a":1,"a":2}`, `["\ud800"]`, `{"a":[1,2,{"b":"c"}]}`, `[{"k":{"k":1,"k":2}}]`, `"x"`, ` 1 `} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if !utf8.ValidString(input) || !json.Valid([]byte(input)) || strings.Count(input, "[")+strings.Count(input, "{") > 100 {
			return
		}
		shallow := verifyExactJSON([]byte(input))
		deep := verifyExactJSON([]byte(strings.Repeat("[", 10001) + input + strings.Repeat("]", 10001)))
		var shallowDuplicate, deepDuplicate *duplicateNameError
		var shallowLone, deepLone *loneSurrogateError
		switch {
		case errors.As(shallow, &shallowDuplicate):
			if !errors.As(deep, &deepDuplicate) || deepDuplicate.location != strings.Repeat("/0", 10001)+shallowDuplicate.location {
				t.Fatalf("%s: shallow %v, deep %v", input, shallow, deep)
			}
		case errors.As(shallow, &shallowLone):
			if !errors.As(deep, &deepLone) || deepLone.location != strings.Repeat("/0", 10001)+shallowLone.location {
				t.Fatalf("%s: shallow %v, deep %v", input, shallow, deep)
			}
		case shallow == nil:
			if !errors.Is(deep, errNestingLimit) {
				t.Fatalf("%s: shallow accepted, deep %v", input, deep)
			}
		default:
			t.Fatalf("%s: valid JSON refused: %v", input, shallow)
		}
	})
}
