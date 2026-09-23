package openbindings

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
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

func TestInterfaceValidate_HostObjectCannotDecideD01(t *testing.T) {
	iface, err := ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"tasks.create":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	report, err := iface.Validate(ValidateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Conclusion != ConclusionConformanceUndetermined {
		t.Fatalf("conclusion = %s, want conformance-undetermined", report.Conclusion)
	}
	if !reflect.DeepEqual(report.Inconclusive, []string{"OBI-D-01"}) {
		t.Fatalf("inconclusive = %v, want only OBI-D-01", report.Inconclusive)
	}
}

const documentWithBinding = `{
	"openbindings": "0.2.0",
	"operations": {"tasks.create": {}},
	"sources": {"api": {"bindingSpec": "example.rest@1", "location": "https://api.example.com/openapi.json"}},
	"bindings": {"tasks.create.api": {"operation": "tasks.create", "source": "api", "selector": "#/paths/~1tasks/post"}}
}`

func TestValidateDocument_BindingIdentifiabilityIsLeftToTheBindingSpecification(t *testing.T) {
	_, report, err := ValidateDocument([]byte(documentWithBinding), ValidateOptions{})
	if err != nil {
		t.Fatalf("an inconclusive rule is not a violation; ValidateDocument = %v", err)
	}
	if report.Conclusion != ConclusionConformanceUndetermined {
		t.Fatalf("conclusion = %s, want conformance-undetermined", report.Conclusion)
	}
	if !reflect.DeepEqual(report.Inconclusive, []string{"OBI-D-13"}) {
		t.Fatalf("inconclusive = %v, want only OBI-D-13", report.Inconclusive)
	}
	if checks := report.InconclusiveChecks(); len(checks) != 1 || checks[0].Path != "/bindings" {
		t.Fatalf("want one OBI-D-13 finding at /bindings: %+v", checks)
	}
}

func TestConcludeConformance_CallerEvidenceCompletesAReport(t *testing.T) {
	report := mustValidateDocument(t, documentWithBinding)
	// A binding specification implementation that decides OBI-D-13 for its
	// own sources supplies the evidence this SDK cannot.
	report.Evidence["OBI-D-13"] = EvidenceSatisfied
	if got := ConcludeConformance(report.Evidence).Conclusion; got != ConclusionConformant {
		t.Fatalf("conclusion with OBI-D-13 decided = %s, want conformant", got)
	}
}

func TestValidateDocument_AViolationIsDecisiveAndInconclusiveRulesAreRetained(t *testing.T) {
	document := strings.Replace(documentWithBinding, `"operation": "tasks.create", "source"`, `"operation": "tasks.missing", "source"`, 1)
	report := mustValidateDocument(t, document)
	if report.Conclusion != ConclusionNonConformant {
		t.Fatalf("conclusion = %s, want non-conformant", report.Conclusion)
	}
	if report.Evidence["OBI-D-08"] != EvidenceViolated {
		t.Fatalf("OBI-D-08 = %s, want violated", report.Evidence["OBI-D-08"])
	}
	if report.Evidence["OBI-D-13"] != EvidenceInconclusive {
		t.Fatalf("OBI-D-13 = %s, want inconclusive and retained", report.Evidence["OBI-D-13"])
	}
	violations := report.Violations()
	if len(violations) != 1 || violations[0].Path != `/bindings/tasks.create.api/operation` {
		t.Fatalf("violations = %+v, want one located at the binding's operation", violations)
	}
}

func TestValidateDocument_SourceLocationForms(t *testing.T) {
	tests := []struct {
		location string
		want     RuleEvidenceStatus
	}{
		{"https://api.example.com/openapi.json", EvidenceSatisfied},
		{"grpc.example.com:443", EvidenceSatisfied}, // a well-formed absolute URI
		{"10.0.0.1:443", EvidenceInconclusive},      // only its binding specification can say
		{"[::1]:443", EvidenceInconclusive},
		{"./openapi.json", EvidenceViolated},
		{"example.com", EvidenceViolated},
		{"https://example.com/<bad>/openapi.json", EvidenceViolated},
	}
	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"sources":{"api":{"bindingSpec":"example.grpc@1","location":"`+tt.location+`"}}}`)
			if got := report.Evidence["OBI-D-05"]; got != tt.want {
				t.Fatalf("OBI-D-05 = %s, want %s; findings %+v", got, tt.want, report.Findings)
			}
		})
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

func TestValidateDocument_ExampleScope(t *testing.T) {
	t.Run("a graph reaching an external resource puts its examples outside the rule", func(t *testing.T) {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{
			"input":{"$ref":"https://schemas.example.com/task.json"},
			"examples":{"one":{"input":42}}}}}`)
		if report.Evidence["OBI-D-11"] != EvidenceSatisfied || len(report.Findings) != 0 {
			t.Fatalf("OBI-D-11 = %s, findings %+v; want out of scope and silent", report.Evidence["OBI-D-11"], report.Findings)
		}
	})
	t.Run("an unrelated external reference no longer hides an in-scope mismatch", func(t *testing.T) {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0",
			"schemas":{"Remote":{"$ref":"https://schemas.example.com/remote.json"}},
			"operations":{"a":{"input":{"type":"string"},"examples":{"one":{"input":42}}}}}`)
		if report.Evidence["OBI-D-11"] != EvidenceViolated {
			t.Fatalf("OBI-D-11 = %s, want violated; findings %+v", report.Evidence["OBI-D-11"], report.Findings)
		}
	})
	t.Run("a reference through the schemas map is followed", func(t *testing.T) {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0",
			"schemas":{"Title":{"type":"string"}},
			"operations":{"a":{"input":{"$ref":"#/schemas/Title"},"examples":{"one":{"input":42}}}}}`)
		if report.Evidence["OBI-D-11"] != EvidenceViolated {
			t.Fatalf("OBI-D-11 = %s, want violated; findings %+v", report.Evidence["OBI-D-11"], report.Findings)
		}
	})
	t.Run("a reference into an embedded schema resource stays within the document", func(t *testing.T) {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0",
			"schemas":{"Title":{"$id":"https://schemas.example.com/title.json","type":"string"}},
			"operations":{"a":{"input":{"$ref":"https://schemas.example.com/title.json"},"examples":{"one":{"input":42}}}}}`)
		if report.Evidence["OBI-D-11"] != EvidenceViolated {
			t.Fatalf("OBI-D-11 = %s, want violated; findings %+v", report.Evidence["OBI-D-11"], report.Findings)
		}
	})
	t.Run("an unresolvable reference leaves the examples inconclusive, not passed", func(t *testing.T) {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{
			"input":{"$ref":"#/schemas/Missing"},"examples":{"one":{"input":42}}}}}`)
		if report.Evidence["OBI-D-16"] != EvidenceViolated {
			t.Fatalf("OBI-D-16 = %s, want violated", report.Evidence["OBI-D-16"])
		}
		if report.Evidence["OBI-D-11"] != EvidenceInconclusive {
			t.Fatalf("OBI-D-11 = %s, want inconclusive; findings %+v", report.Evidence["OBI-D-11"], report.Findings)
		}
	})
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
		{"invalid pointer escape at an OBI position", `{"$ref":"#/$defs/~2","$defs":{"~2":{}}}`, EvidenceViolated},
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
		if report.Evidence["OBI-D-12"] != EvidenceViolated {
			t.Fatalf("%q: OBI-D-12 = %s, want violated", version, report.Evidence["OBI-D-12"])
		}
	}
}

func TestFindingPaths_AreJSONPointers(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0",
		"schemas":{"T":{"type":"object","properties":{"a/b~c":{"minLength":"3"}}}},
		"operations":{"a":{"output":{"$ref":"#/schemas/T"},"examples":{"one":{"output":{"n":"x"}}}},
		              "b":{"input":{"type":"object","properties":{"n":{"type":"integer"}}},"examples":{"two":{"input":{"n":"x"}}}}},
		"sources":{"s":{"bindingSpec":"x@1"}}}`)
	want := map[string]string{
		"/schemas/T/properties/a~1b~0c/minLength": "OBI-D-17",
		"/operations/b/examples/two/input/n":      "OBI-D-11",
		"/sources/s":                              "OBI-D-02",
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
	data := []byte(`{"openbindings":"0.2.0","operations":{},"sources":{"s":{"bindingSpec":"x@1"}}}`)
	_, parseErr := ParseDocument(data)
	_, _, validateErr := ValidateDocument(data, ValidateOptions{})
	var parsed, validated *ValidationError
	if !errors.As(parseErr, &parsed) || !errors.As(validateErr, &validated) {
		t.Fatalf("want ValidationErrors, got %v and %v", parseErr, validateErr)
	}
	if !reflect.DeepEqual(parsed.Problems, validated.Problems) {
		t.Fatalf("ParseDocument and ValidateDocument word the same OBI-D-02 violation differently:\n%q\n%q", parsed.Problems, validated.Problems)
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
		"operations":{"a":{"input":null,"description":null}},
		"sources":{"s":{"bindingSpec":"x@1","location":"./local.json"}},
		"bindings":{"b":{"operation":"missing","source":"s"}}}`), ValidateOptions{})

	if iface != nil {
		t.Fatal("the model cannot carry a null input; no Interface is returned")
	}
	for rule, want := range map[string]RuleEvidenceStatus{
		"OBI-D-02": EvidenceViolated,
		"OBI-D-05": EvidenceViolated,
		"OBI-D-08": EvidenceViolated,
		"OBI-D-12": EvidenceSatisfied,
		"OBI-D-17": EvidenceViolated,
		"OBI-D-19": EvidenceSatisfied,
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
		"transforms":{"t":"$","n":7},"sources":{"s":{"bindingSpec":"x@1","location":7}},
		"bindings":{"b":{"operation":"a","source":42,"inputTransform":{"$ref":"#/transforms/missing"},"outputTransform":9},
		"c":{"operation":"a","source":"s","inputTransform":{"$ref":"https://a.example/doc#/transforms/t"}}}}`)
	found := map[string]RuleEvidenceStatus{}
	for _, finding := range report.Findings {
		found[finding.Rule+" "+finding.Path] = finding.Status
	}
	for key, want := range map[string]RuleEvidenceStatus{
		"OBI-D-02 /operations":                     EvidenceViolated,
		"OBI-D-08 /bindings/b/operation":           EvidenceViolated,
		"OBI-D-09 /bindings/b/source":              EvidenceViolated,
		"OBI-D-10 /bindings/b/inputTransform/$ref": EvidenceViolated,
		"OBI-D-05 /bindings/c/inputTransform/$ref": EvidenceViolated,
		"OBI-D-05 /sources/s/location":             EvidenceViolated,
		"OBI-D-18 /transforms/n":                   EvidenceViolated,
	} {
		if got := found[key]; got != want {
			t.Errorf("%s = %q, want %s; findings %+v", key, got, want, report.Findings)
		}
	}
	for _, rule := range []string{"OBI-D-03", "OBI-D-04", "OBI-D-11", "OBI-D-17"} {
		if report.Evidence[rule] != EvidenceSatisfied {
			t.Errorf("%s = %s: an operations member that is not an object holds nothing it judges", rule, report.Evidence[rule])
		}
	}
	if _, ok := found["OBI-D-18 /bindings/b/outputTransform"]; ok {
		t.Error("a transform that is neither an expression nor a reference is outside OBI-D-18")
	}
}

// OBI-T-02 covers every OBI-defined object, the $ref object form of a binding
// transform included.
func TestValidateDocument_DiagnosesUnknownMembersOfTransformReferences(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{}},"transforms":{"t":"$"},
		"sources":{"s":{"bindingSpec":"x@1","content":{}}},
		"bindings":{"b":{"operation":"a","source":"s","inputTransform":{"$ref":"#/transforms/t","bogus":1,"x-kept":2}}}}`)
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Path == "/bindings/b/inputTransform" && strings.Contains(diagnostic.Message, "bogus") && !strings.Contains(diagnostic.Message, "x-kept") {
			return
		}
	}
	t.Fatalf("no OBI-T-02 diagnostic for the reference object; diagnostics %+v", report.Diagnostics)
}

// OBI-D-11 follows an absolute reference with a fragment into a resource the
// document embeds.
func TestValidateDocument_ExamplesThroughAFragmentIntoAnEmbeddedResource(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0",
		"schemas":{"T":{"$id":"https://example.com/t","$defs":{"S":{"type":"string"}}}},
		"operations":{"a":{"input":{"$ref":"https://example.com/t#/$defs/S"},"examples":{"one":{"input":42}}}}}`)
	if report.Evidence["OBI-D-11"] != EvidenceViolated {
		t.Fatalf("OBI-D-11 = %s, want violated; findings %+v", report.Evidence["OBI-D-11"], report.Findings)
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

// OBI-D-06 and OBI-D-07 govern every schema in the document, inside schema
// resources too: a resource's internal business is reference resolution, not
// the document's dialect.
func TestValidateDocument_DialectRulesReachEverySchema(t *testing.T) {
	for name, tt := range map[string]struct {
		input string
		rule  string
	}{
		"$schema at an OBI position":     {`{"$schema":"http://json-schema.org/draft-07/schema#"}`, "OBI-D-06"},
		"$schema inside a resource":      {`{"$id":"https://example.com/s","properties":{"a":{"$schema":"http://json-schema.org/draft-07/schema#"}}}`, "OBI-D-06"},
		"$vocabulary at an OBI position": {`{"$vocabulary":{}}`, "OBI-D-07"},
		"$vocabulary inside a resource":  {`{"$id":"https://example.com/s","$defs":{"m":{"$vocabulary":{}}}}`, "OBI-D-07"},
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{"input":`+tt.input+`}}}`)
		if report.Evidence[tt.rule] != EvidenceViolated {
			t.Errorf("%s: %s = %s, want violated", name, tt.rule, report.Evidence[tt.rule])
		}
	}
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{"input":{"$schema":"https://json-schema.org/draft/2020-12/schema"}}}}`)
	if report.Evidence["OBI-D-06"] != EvidenceSatisfied || report.Evidence["OBI-D-07"] != EvidenceSatisfied {
		t.Fatalf("the 2020-12 dialect satisfies both rules: %v %v", report.Evidence["OBI-D-06"], report.Evidence["OBI-D-07"])
	}
}

// Reference cycles terminate in every walk (OBI-T-11): a recursive type is
// judged, and a pure loop with no schema in it leaves its examples undecided
// rather than hanging.
func TestValidateDocument_ReferenceCyclesTerminate(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0",
		"schemas":{"Node":{"type":"object","properties":{"next":{"$ref":"#/schemas/Node"}}}},
		"operations":{"a":{"input":{"$ref":"#/schemas/Node"},"examples":{"one":{"input":{"next":{"next":5}}}}}}}`)
	if report.Evidence["OBI-D-11"] != EvidenceViolated {
		t.Fatalf("OBI-D-11 = %s, want violated; findings %+v", report.Evidence["OBI-D-11"], report.Findings)
	}
	report = mustValidateDocument(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$ref":"#/schemas/B"},"B":{"$ref":"#/schemas/A"}},
		"operations":{"a":{"input":{"$ref":"#/schemas/A"},"examples":{"one":{"input":1}}}}}`)
	if report.Evidence["OBI-D-11"] == EvidenceSatisfied {
		t.Fatalf("a pure reference loop established no verdict, yet OBI-D-11 = satisfied")
	}
}

// OBI-D-11's graph is what evaluation applies: an unreferenced definition is
// not part of it, and a plain-name anchor inside an embedded resource
// resolves.
func TestValidateDocument_ExampleGraphIsWhatEvaluationApplies(t *testing.T) {
	for name, document := range map[string]string{
		"unreferenced external definition": `{"openbindings":"0.2.0","operations":{"op":{
			"input":{"type":"string","$defs":{"dead":{"$ref":"https://outside.example/x"}}},"examples":{"bad":{"input":7}}}}}`,
		"anchor inside a resource": `{"openbindings":"0.2.0","operations":{"op":{
			"input":{"$id":"https://e.test/S","$ref":"#string","$defs":{"s":{"$anchor":"string","type":"string"}}},"examples":{"bad":{"input":7}}}}}`,
		"conflicting ids elsewhere": `{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.test/S"},"B":{"$id":"https://e.test/S"}},
			"operations":{"op":{"input":{"type":"string"},"examples":{"bad":{"input":7}}}}}`,
	} {
		report := mustValidateDocument(t, document)
		if report.Evidence["OBI-D-11"] != EvidenceViolated {
			t.Errorf("%s: OBI-D-11 = %s, want violated; findings %+v", name, report.Evidence["OBI-D-11"], report.Findings)
		}
	}
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","schemas":{"A":{"$id":"https://e.test/S"},"B":{"$id":"https://e.test/S"}},
		"operations":{"op":{"input":{"$ref":"https://e.test/S"},"examples":{"one":{"input":7}}}}}`)
	if report.Evidence["OBI-D-11"] != EvidenceInconclusive {
		t.Fatalf("a graph reaching an ambiguous $id is undecided; OBI-D-11 = %s", report.Evidence["OBI-D-11"])
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
		"non-string $ref":                 {`{"$ref":42}`, EvidenceViolated},
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","schemas":{"A":{}},"operations":{"a":{"input":`+tt.input+`}}}`)
		if got := report.Evidence["OBI-D-05"]; got != tt.want {
			t.Errorf("%s: OBI-D-05 = %s, want %s; findings %+v", name, got, tt.want, report.Findings)
		}
	}
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"sources":{"s":{"bindingSpec":"x@1","location":""}}}`)
	if report.Evidence["OBI-D-05"] != EvidenceViolated {
		t.Fatalf("an empty location is relative in form: OBI-D-05 = %s", report.Evidence["OBI-D-05"])
	}
}

// OBI-D-16 covers an absolute $ref that matches an embedded schema's $id.
func TestValidateDocument_AbsoluteReferencesIntoEmbeddedResourcesResolve(t *testing.T) {
	document := func(ref string) string {
		return `{"openbindings":"0.2.0","schemas":{"T":{"$id":"https://example.com/t","$defs":{"S":{"type":"string"}}}},
			"operations":{"a":{"input":{"$ref":"` + ref + `"}}}}`
	}
	for ref, want := range map[string]RuleEvidenceStatus{
		"https://example.com/t#/$defs/S":       EvidenceSatisfied,
		"https://example.com/t#/$defs/Missing": EvidenceViolated,
		"https://other.example/x#/nope":        EvidenceSatisfied,
	} {
		if got := mustValidateDocument(t, document(ref)).Evidence["OBI-D-16"]; got != want {
			t.Errorf("%s: OBI-D-16 = %s, want %s", ref, got, want)
		}
	}
}

// A resource limit is not evidence of a violation (§10.5).
func TestValidateDocument_ResourceLimitsAreInconclusive(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{"input":{"minLength":1e999999}}}}`)
	if report.Evidence["OBI-D-17"] != EvidenceInconclusive {
		t.Fatalf("OBI-D-17 = %s, want inconclusive; findings %+v", report.Evidence["OBI-D-17"], report.Findings)
	}
}

// A document schema finding about a map key is located at the key, the same
// way every time.
func TestValidateDocument_KeyFindingPathsAreDeterministic(t *testing.T) {
	t.Skip("santhosh-tekuri/jsonschema v6.0.3 records a propertyNames failure's location without copying it, so a later sibling overwrites it; fixed upstream by cloning the location")
	for range 50 {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{},"schemas":{"bad key":{}},"sources":{},"transforms":{},"name":"n","description":"d"}`)
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

// OBI-D-16 judges every same-document fragment, whatever OBI-D-05 says of its
// spelling, and an anchor declared twice leaves a reference to it undecided.
func TestValidateDocument_ReferenceResolutionIsJudgedForEveryFragment(t *testing.T) {
	for ref, want := range map[string]RuleEvidenceStatus{
		"#/schemas/Nope%20x": EvidenceViolated,
		"#/schemas/T%61sk":   EvidenceSatisfied,
		"#/schemas/~2":       EvidenceViolated,
		"#nothere":           EvidenceViolated,
	} {
		report := mustValidateDocument(t, `{"openbindings":"0.2.0","schemas":{"Task":{}},"operations":{"a":{"input":{"$ref":"`+ref+`"}}}}`)
		if got := report.Evidence["OBI-D-16"]; got != want {
			t.Errorf("%s: OBI-D-16 = %s, want %s", ref, got, want)
		}
	}
	report := mustValidateDocument(t, `{"openbindings":"0.2.0",
		"schemas":{"A":{"$id":"https://ex.test/a","$defs":{"p":{"$anchor":"dup"},"q":{"$anchor":"dup"}}}},
		"operations":{"a":{"input":{"$ref":"https://ex.test/a#dup"}}}}`)
	if report.Evidence["OBI-D-16"] != EvidenceInconclusive {
		t.Fatalf("an anchor declared twice: OBI-D-16 = %s, want inconclusive", report.Evidence["OBI-D-16"])
	}
}

// A named-transform reference is decoded before it is resolved; its spelling
// is OBI-D-05's.
func TestValidateDocument_TransformReferencesDecodeTheirFragment(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"a":{}},"transforms":{"ab":"$"},
		"sources":{"s":{"bindingSpec":"x@1","content":{}}},
		"bindings":{"b":{"operation":"a","source":"s","inputTransform":{"$ref":"#/transforms/a%62"}}}}`)
	if report.Evidence["OBI-D-10"] != EvidenceSatisfied || report.Evidence["OBI-D-05"] != EvidenceViolated {
		t.Fatalf("OBI-D-10 = %s, OBI-D-05 = %s", report.Evidence["OBI-D-10"], report.Evidence["OBI-D-05"])
	}
}
