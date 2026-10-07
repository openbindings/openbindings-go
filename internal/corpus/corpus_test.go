package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var profile = Profile{Features: map[string]bool{"exact-numbers": true, "draft-07-dialect": false, "supplied-resources": true}}

// The Judging table for value results: a result passes with the same
// result, or with a decline where the value carries orNoVerdict or a
// feature it depends on is declared unsupported; any other decline is a
// SHORTFALL. undefined, external, and no-contract pass with any decline, and
// a result FAILs. A per-value dependsOn replaces the case's.
func TestJudgeValues(t *testing.T) {
	s, f, d := Satisfies, Fails, Decline
	for i, c := range []struct {
		expected string
		answers  []string
		want     string
	}{
		{`{"results":["satisfies","fails"]}`, []string{s, f}, Pass},
		{`{"results":["satisfies"]}`, []string{f}, Fail},
		{`{"results":["satisfies"]}`, []string{d}, Shortfall},
		{`{"results":["undefined"]}`, []string{d}, Pass},
		{`{"results":["undefined"]}`, []string{s}, Fail},
		{`{"results":["external"]}`, []string{d}, Pass},
		{`{"results":["external"]}`, []string{f}, Fail},
		{`{"results":["no-contract"]}`, []string{d}, Pass},
		{`{"results":["no-contract"]}`, []string{s}, Fail},
		{`{"results":[{"result":"satisfies","orNoVerdict":true}]}`, []string{d}, Pass},
		{`{"results":[{"result":"satisfies","orNoVerdict":true}]}`, []string{s}, Pass},
		{`{"results":[{"result":"satisfies","orNoVerdict":true}]}`, []string{f}, Fail},
		// A decline under a feature declared unsupported passes, and so does
		// the right result: no rule requires a decline there.
		{`{"results":["satisfies"],"dependsOn":["draft-07-dialect"]}`, []string{d}, Pass},
		{`{"results":["satisfies"],"dependsOn":["draft-07-dialect"]}`, []string{s}, Pass},
		{`{"results":["satisfies"],"dependsOn":["draft-07-dialect"]}`, []string{f}, Fail},
		{`{"results":["satisfies"],"dependsOn":["supplied-resources","draft-07-dialect"]}`, []string{d}, Pass},
		{`{"results":["satisfies"],"dependsOn":["exact-numbers"]}`, []string{d}, Shortfall},
		{`{"results":[{"result":"satisfies","dependsOn":[]}],"dependsOn":["draft-07-dialect"]}`, []string{d}, Shortfall},
		{`{"results":[{"result":"fails","dependsOn":["draft-07-dialect"]}],"dependsOn":["exact-numbers"]}`, []string{d}, Pass},
		{`{"results":["satisfies"],"dependsOn":["undeclared"]}`, []string{s}, Fail},
		{`{"results":["satisfies","satisfies"]}`, []string{s}, Fail},
		{`{"results":["maybe"]}`, []string{d}, Fail},
		{`{"results":["satisfies"]}`, []string{"valid"}, Fail},
		// A FAIL outweighs a SHORTFALL elsewhere in the case.
		{`{"results":["satisfies","fails"]}`, []string{d, s}, Fail},
	} {
		if got := JudgeValues(json.RawMessage(c.expected), c.answers, profile); got.Category != c.want {
			t.Errorf("case %d (%s %v): %s (%s), want %s", i, c.expected, c.answers, got.Category, got.Detail, c.want)
		}
	}
}

// An example's claim is judged as its value's result: true as satisfies,
// false as fails, undefined and external as themselves, no claim as no
// contract. Every value the case lists is answered, and only those.
func TestJudgeExamples(t *testing.T) {
	expected := json.RawMessage(`{"examples":{"good":{"input":"true","output":"false"},"open":{"input":"undefined"},"far":{"input":"external"},"none":{"output":"no-claim"},"described":{}}}`)
	all := func() map[string]map[string]string {
		return map[string]map[string]string{"good": {"input": Satisfies, "output": Fails}, "open": {"input": Decline}, "far": {"input": Decline}, "none": {"output": Decline}}
	}
	if j := JudgeExamples(expected, all(), profile); j.Category != Pass {
		t.Fatalf("the expected answers: %+v", j)
	}
	for name, c := range map[string]struct {
		change func(map[string]map[string]string)
		want   string
	}{
		"a false claim answered true":      {func(a map[string]map[string]string) { a["good"]["output"] = Satisfies }, Fail},
		"a true claim declined":            {func(a map[string]map[string]string) { a["good"]["input"] = Decline }, Shortfall},
		"an undefined claim answered":      {func(a map[string]map[string]string) { a["open"]["input"] = Fails }, Fail},
		"no claim answered":                {func(a map[string]map[string]string) { a["none"]["output"] = Satisfies }, Fail},
		"a supplied value left unanswered": {func(a map[string]map[string]string) { delete(a, "far") }, Fail},
		"an answer the case does not list": {func(a map[string]map[string]string) {
			a["described"] = map[string]string{"input": Satisfies}
		}, Fail},
	} {
		answers := all()
		c.change(answers)
		if j := JudgeExamples(expected, answers, profile); j.Category != c.want {
			t.Errorf("%s: %+v, want %s", name, j, c.want)
		}
	}
}

// A conforming text passes when concluded conformant, or declined under a
// dependsOn feature declared unsupported (validate-document only); a decline
// otherwise is a SHORTFALL. A non-conforming text passes only when concluded
// non-conformant with every listed rule violated and none of notViolated.
func TestJudgeConformance(t *testing.T) {
	conformant := DocumentAnswer{Conclusion: Conformant}
	declined := DocumentAnswer{Conclusion: Decline}
	violating := func(rules ...string) DocumentAnswer {
		return DocumentAnswer{Conclusion: NonConformant, Violated: rules}
	}
	for i, c := range []struct {
		fixture string
		answer  DocumentAnswer
		want    string
	}{
		{`{"valid":true}`, conformant, Pass},
		{`{"valid":true}`, declined, Shortfall},
		{`{"valid":true}`, violating("OBI-02"), Fail},
		{`{"valid":false,"violates":["OBI-06"]}`, violating("OBI-02", "OBI-06"), Pass},
		{`{"valid":false,"violates":["OBI-06"]}`, violating("OBI-07"), Fail},
		{`{"valid":false,"violates":["OBI-06"]}`, declined, Fail},
		{`{"valid":false,"violates":["OBI-06"]}`, conformant, Fail},
		{`{"valid":false,"violates":["OBI-01"],"notViolated":["OBI-02"]}`, violating("OBI-01"), Pass},
		{`{"valid":false,"violates":["OBI-01"],"notViolated":["OBI-02"]}`, violating("OBI-01", "OBI-02"), Fail},
		{`{"valid":false}`, violating("OBI-09"), Pass},
		{`{}`, conformant, Fail},
	} {
		if got := JudgeFixture(json.RawMessage(c.fixture), c.answer, profile); got.Category != c.want {
			t.Errorf("fixture %d (%s): %s (%s), want %s", i, c.fixture, got.Category, got.Detail, c.want)
		}
	}
	for i, c := range []struct {
		expected string
		answer   DocumentAnswer
		want     string
	}{
		{`{"outcome":"conformant"}`, conformant, Pass},
		{`{"outcome":"conformant"}`, declined, Shortfall},
		{`{"outcome":"conformant","dependsOn":["draft-07-dialect"]}`, declined, Pass},
		{`{"outcome":"conformant","dependsOn":["draft-07-dialect"]}`, conformant, Pass},
		{`{"outcome":"conformant","dependsOn":["draft-07-dialect"]}`, violating("OBI-01"), Fail},
		{`{"outcome":"conformant","dependsOn":["exact-numbers"]}`, declined, Shortfall},
		{`{"outcome":"conformant","dependsOn":["undeclared"]}`, conformant, Fail},
		{`{"outcome":"non-conformant","violates":["OBI-06","OBI-07"]}`, violating("OBI-06", "OBI-07"), Pass},
		{`{"outcome":"non-conformant","violates":["OBI-06","OBI-07"]}`, violating("OBI-06"), Fail},
		{`{"outcome":"non-conformant"}`, declined, Fail},
		{`{"outcome":"interpreted"}`, conformant, Fail},
	} {
		if got := JudgeDocument(json.RawMessage(c.expected), c.answer, profile); got.Category != c.want {
			t.Errorf("validate-document %d (%s): %s (%s), want %s", i, c.expected, got.Category, got.Detail, c.want)
		}
	}
}

func TestReconcile(t *testing.T) {
	c := &Corpus{Cases: []Case{{ID: "a", Action: ActionValidity}, {ID: "b", Action: "resolve-operation"}, {ID: "c", Action: "validate-operation-values"}}}
	var l Ledger
	l.Record("a", Judgment{Category: Pass})
	if problems, _ := c.Reconcile(ModuleRoot, &l); len(problems) != 1 {
		t.Errorf("a case neither executed nor omitted: %v", problems)
	}
	l.Record("b", Judgment{Category: Omitted})
	if problems, _ := c.Reconcile(ModuleRoot, &l); len(problems) != 0 {
		t.Errorf("every root case recorded: %v", problems)
	}
	l.Record("c", Judgment{Category: Pass})
	if problems, _ := c.Reconcile(ModuleRoot, &l); len(problems) != 1 {
		t.Errorf("a case of another module recorded here: %v", problems)
	}
	l.Record("a", Judgment{Category: Pass})
	if j, _ := l.Outcome("a"); j.Category != Fail {
		t.Errorf("a case recorded twice: %v", j)
	}
}

// writeCorpus writes a corpus of the given files, with a manifest counting
// each file's cases, into a new directory.
func writeCorpus(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	var manifest struct {
		Files         []map[string]any `json:"files"`
		ScenarioFiles []map[string]any `json:"scenarioFiles"`
	}
	manifest.Files, manifest.ScenarioFiles = []map[string]any{}, []map[string]any{}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		var counted struct {
			Tests     []any `json:"tests"`
			Scenarios []any `json:"scenarios"`
		}
		_ = json.Unmarshal([]byte(content), &counted)
		if strings.HasPrefix(name, "scenarios/") {
			manifest.ScenarioFiles = append(manifest.ScenarioFiles, map[string]any{"path": name, "scenarios": len(counted.Scenarios)})
		} else {
			manifest.Files = append(manifest.Files, map[string]any{"path": name, "tests": len(counted.Tests)})
		}
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const resolveScenario = `{"id": "NAMES-01", "description": "d", "action": "resolve-operation", "given": {"document": {"openbindings": "0.2.0", "operations": {"a": {}}}, "name": "a"}, "expected": {"outcome": "resolved", "operationKey": "a", "bindingKeys": []}}`

// Load reads scenario format @3 alone: a scenario file in format @2, the
// format of the corpus before the document-model text, is refused.
func TestLoad_ReadsFormatV3Alone(t *testing.T) {
	for format, refused := range map[string]bool{FormatV3: false, "openbindings.core-tool-scenarios@2": true} {
		dir := writeCorpus(t, map[string]string{"scenarios/5.1-names.json": `{"format": "` + format + `", "section": "5.1", "description": "d", "scenarios": [` + resolveScenario + `]}`})
		_, err := Load(dir)
		if got := err != nil && strings.Contains(err.Error(), "unknown format"); got != refused {
			t.Errorf("%s: refused %v, want %v (%v)", format, got, refused, err)
		}
	}
}

// Load reads fixtures from document/ alone, each file citing a rule or a
// section, and scenarios whose action a module executes; an action the
// corpus retired is refused.
func TestLoad_FixturesAndActions(t *testing.T) {
	fixture := `{"tests": [{"description": "d", "document": {"openbindings": "0.2.0", "operations": {}}, "valid": true}]}`
	dir := writeCorpus(t, map[string]string{
		"document/OBI-02.json":     `{"rule": "OBI-02", "section": "10", "description": "d", ` + fixture[1:],
		"document/section-12.json": `{"section": "12", "description": "d", ` + fixture[1:],
		"scenarios/5.1-names.json": `{"format": "` + FormatV3 + `", "section": "5.1", "description": "d", "scenarios": [` + resolveScenario + `]}`,
	})
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, cs := range c.Cases {
		got = append(got, cs.ID+" "+cs.Rule+" "+cs.Section+" "+cs.Action)
	}
	want := []string{"document/OBI-02.json#/tests/0 OBI-02 10 validity", "document/section-12.json#/tests/0  12 validity", "NAMES-01  5.1 resolve-operation"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cases %q, want %q", got, want)
	}

	if _, err := Load(writeCorpus(t, map[string]string{"document/x.json": fixture})); err == nil || !strings.Contains(err.Error(), "cites neither") {
		t.Errorf("a fixture file citing neither a rule nor a section: %v", err)
	}
	for _, action := range []string{"conclude-conformance", "derive-form", ActionValidity} {
		retired := strings.Replace(resolveScenario, "resolve-operation", action, 1)
		dir := writeCorpus(t, map[string]string{"scenarios/10-conformance.json": `{"format": "` + FormatV3 + `", "section": "10", "description": "d", "scenarios": [` + retired + `]}`})
		if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "unknown action") {
			t.Errorf("%s: %v", action, err)
		}
	}
}
