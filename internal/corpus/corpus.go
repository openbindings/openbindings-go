// Package corpus reads the specification's core conformance corpus for this
// SDK's corpus adapters, and judges what can be judged without the SDK: an
// answer against what a case says the document means, under the capability
// profile the adapter declares, as the corpus's Judging table states; and
// the count of cases each module executed or omitted. It imports nothing
// from the SDK, so the core module's own tests and the schemaeval module's
// tests can both use it without an import cycle: the core's tests execute
// the actions that need no schema evaluator, and schemaeval's tests execute
// the value actions under the project's ECMA-262 evaluator.
//
// It reads the validity fixtures in document/ and the scenarios in
// scenarios/, in format openbindings.core-scenarios@3, the format of the
// corpus of the text this SDK applies; a scenario file in any other format
// is refused.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Formats of a case.
const (
	FormatFixture = "fixture"
	FormatV3      = "openbindings.core-scenarios@3"
)

// ActionValidity is the action of a validity fixture.
const ActionValidity = "validity"

// Modules that execute cases.
const (
	ModuleRoot       = "root"
	ModuleSchemaeval = "schemaeval"
)

// Case is one corpus case: a validity fixture test or a scenario.
type Case struct {
	// ID is the scenario ID, or for a fixture test its file and position
	// (document/OBI-12.json#/tests/40).
	ID   string
	File string
	// Rule is the rule a fixture file covers, and Section the section its
	// file cites; a scenario file cites a section alone.
	Rule        string
	Section     string
	Format      string
	Action      string
	Description string
	Raw         json.RawMessage
}

// Corpus is a loaded corpus.
type Corpus struct {
	Dir   string
	Cases []Case
	// Counts holds the manifest's count for each file it lists.
	Counts map[string]int
}

// Locate returns the corpus directory: OB_SPEC_CORPUS when set, else a
// sibling spec checkout found from one of dirs, or "" when there is none.
func Locate(dirs ...string) string {
	var candidates []string
	if env := os.Getenv("OB_SPEC_CORPUS"); env != "" {
		candidates = append(candidates, env)
	}
	for _, d := range dirs {
		candidates = append(candidates, filepath.Join(d, "..", "spec", "conformance"), filepath.Join(d, "spec", "conformance"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

// Required reports whether a missing corpus, or a check that could not be
// verified, must fail rather than skip (OB_CORPUS_REQUIRED, set in CI).
func Required() bool { return os.Getenv("OB_CORPUS_REQUIRED") != "" }

type fixtureFile struct {
	Rule    string            `json:"rule"`
	Section string            `json:"section"`
	Tests   []json.RawMessage `json:"tests"`
}

type scenarioFile struct {
	Format    string            `json:"format"`
	Section   string            `json:"section"`
	Scenarios []json.RawMessage `json:"scenarios"`
}

type caseHeader struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Action      string `json:"action"`
}

// Load reads every fixture file in document/ and every scenario file in
// scenarios/ under dir, and checks the count of cases in each against the
// manifest: every case the manifest counts is read, and no other. A fixture
// file cites a rule or a section; a scenario file is in format @3, and each
// scenario names an action some module executes.
func Load(dir string) (*Corpus, error) {
	c := &Corpus{Dir: dir, Counts: map[string]int{}}
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("reading the manifest: %w", err)
	}
	var m struct {
		Files []struct {
			Path  string `json:"path"`
			Tests int    `json:"tests"`
		} `json:"files"`
		ScenarioFiles []struct {
			Path      string `json:"path"`
			Scenarios int    `json:"scenarios"`
		} `json:"scenarioFiles"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		return nil, fmt.Errorf("parsing the manifest: %w", err)
	}
	for _, f := range m.Files {
		c.Counts[f.Path] = f.Tests
	}
	for _, f := range m.ScenarioFiles {
		c.Counts[f.Path] = f.Scenarios
	}
	read := map[string]int{}
	names, err := jsonFiles(filepath.Join(dir, "document"))
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		rel := "document/" + name
		data, err := os.ReadFile(filepath.Join(dir, "document", name))
		if err != nil {
			return nil, err
		}
		var f fixtureFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", rel, err)
		}
		if f.Rule == "" && f.Section == "" {
			return nil, fmt.Errorf("%s: a fixture file cites a rule or a section, and this one cites neither", rel)
		}
		for i, raw := range f.Tests {
			var h caseHeader
			if err := json.Unmarshal(raw, &h); err != nil {
				return nil, fmt.Errorf("parsing %s test %d: %w", rel, i, err)
			}
			c.Cases = append(c.Cases, Case{ID: fmt.Sprintf("%s#/tests/%d", rel, i), File: rel, Rule: f.Rule, Section: f.Section,
				Format: FormatFixture, Action: ActionValidity, Description: h.Description, Raw: raw})
		}
		read[rel] = len(f.Tests)
	}
	names, err = jsonFiles(filepath.Join(dir, "scenarios"))
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for _, name := range names {
		rel := "scenarios/" + name
		data, err := os.ReadFile(filepath.Join(dir, "scenarios", name))
		if err != nil {
			return nil, err
		}
		var f scenarioFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", rel, err)
		}
		if f.Format != FormatV3 {
			return nil, fmt.Errorf("%s: unknown format %q", rel, f.Format)
		}
		for i, raw := range f.Scenarios {
			var h caseHeader
			if err := json.Unmarshal(raw, &h); err != nil {
				return nil, fmt.Errorf("parsing %s scenario %d: %w", rel, i, err)
			}
			if prior, dup := seen[h.ID]; dup {
				return nil, fmt.Errorf("%s: scenario %s repeats one in %s", rel, h.ID, prior)
			}
			seen[h.ID] = rel
			if _, known := designations[h.Action]; !known || h.Action == ActionValidity {
				return nil, fmt.Errorf("%s: scenario %s has unknown action %q", rel, h.ID, h.Action)
			}
			c.Cases = append(c.Cases, Case{ID: h.ID, File: rel, Section: f.Section, Format: f.Format, Action: h.Action,
				Description: h.Description, Raw: raw})
		}
		read[rel] = len(f.Scenarios)
	}
	var problems []string
	for path, n := range c.Counts {
		if read[path] != n {
			problems = append(problems, fmt.Sprintf("%s: the manifest counts %d cases, the file holds %d", path, n, read[path]))
		}
	}
	for path, n := range read {
		if _, listed := c.Counts[path]; !listed {
			problems = append(problems, fmt.Sprintf("%s: %d cases the manifest does not list", path, n))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("the corpus does not match its manifest: %s", strings.Join(problems, "; "))
	}
	return c, nil
}

func jsonFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// designations maps each action to the module that executes it, with the
// reason the other module gives for not executing it.
var designations = map[string]struct{ module, reason string }{
	ActionValidity:              {ModuleRoot, ""},
	"validate-document":         {ModuleRoot, ""},
	"resolve-operation":         {ModuleRoot, ""},
	"check-dependency-kind":     {ModuleRoot, ""},
	"validate-operation-values": {ModuleSchemaeval, "value validation runs in the schemaeval module, under the project's ECMA-262 evaluator"},
	"check-examples":            {ModuleSchemaeval, "example checking composes value validation, which runs in the schemaeval module"},
}

// Designate names the module that executes a case, and the reason the other
// module omits it.
func Designate(c Case) (module, reason string) {
	d := designations[c.Action]
	return d.module, d.reason
}

// Profile is the capability profile an adapter declares: each feature the
// cases it executes depend on, supported or not.
type Profile struct {
	Features map[string]bool
}

// lacking returns a feature of dependsOn the profile declares unsupported,
// or "", and a feature it does not declare at all, or "".
func (p Profile) lacking(dependsOn []string) (unsupported, undeclared string) {
	for _, f := range dependsOn {
		supported, declared := p.Features[f]
		switch {
		case !declared:
			return "", f
		case !supported && unsupported == "":
			unsupported = f
		}
	}
	return unsupported, ""
}

// Run categories, as the corpus defines them.
const (
	Pass      = "pass"
	Fail      = "FAIL"
	Shortfall = "SHORTFALL"
	Omitted   = "OMITTED"
)

// Judgment is a case's run category and its detail.
type Judgment struct {
	Category string
	Detail   string
}

// Answers software gives: a value's result or an example's, and a text's
// conclusion. Decline is no answer either way.
const (
	Satisfies     = "satisfies"
	Fails         = "fails"
	Conformant    = "conformant"
	NonConformant = "non-conformant"
	Decline       = "decline"
)

// expectedResult is one value's expected result in any of its forms.
type expectedResult struct {
	Result      string   `json:"result"`
	OrNoVerdict bool     `json:"orNoVerdict"`
	DependsOn   []string `json:"dependsOn"`
}

func readResult(raw json.RawMessage) (expectedResult, error) {
	var token string
	if json.Unmarshal(raw, &token) == nil {
		return expectedResult{Result: token}, nil
	}
	var r expectedResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	if r.Result != Satisfies && r.Result != Fails {
		return r, fmt.Errorf("an object result holds %q, not satisfies or fails", r.Result)
	}
	return r, nil
}

// judgeResult judges one answer (Satisfies, Fails, or Decline) against one
// expected result, under the case's dependsOn unless the result replaces it:
//   - satisfies or fails passes with the same result, or with a decline
//     where the result carries orNoVerdict or a feature it depends on is
//     declared unsupported; any other decline is a SHORTFALL, and the other
//     result FAILs;
//   - undefined, external, and no-contract pass with any decline, and a
//     result FAILs.
func judgeResult(raw json.RawMessage, caseDependsOn []string, answer string, p Profile) Judgment {
	r, err := readResult(raw)
	if err != nil {
		return Judgment{Fail, "unreadable expected result: " + err.Error()}
	}
	depends := caseDependsOn
	if r.DependsOn != nil {
		depends = r.DependsOn
	}
	unsupported, undeclared := p.lacking(depends)
	if undeclared != "" {
		return Judgment{Fail, "the profile does not declare " + undeclared}
	}
	if answer != Satisfies && answer != Fails && answer != Decline {
		return Judgment{Fail, fmt.Sprintf("an answer %q that is no result and no decline", answer)}
	}
	switch r.Result {
	case "undefined", "external", "no-contract":
		if answer != Decline {
			return Judgment{Fail, fmt.Sprintf("%s where the expected result is %s, which no result answers", answer, r.Result)}
		}
		return Judgment{Pass, ""}
	case Satisfies, Fails:
		switch {
		case answer == r.Result:
			return Judgment{Pass, ""}
		case answer != Decline:
			return Judgment{Fail, fmt.Sprintf("%s; expected %s", answer, r.Result)}
		case r.OrNoVerdict:
			return Judgment{Pass, "declined where the schema holds a part the result does not depend on"}
		case unsupported != "":
			return Judgment{Pass, "declined under " + unsupported + ", declared unsupported"}
		}
		return Judgment{Shortfall, fmt.Sprintf("declined where the profile supports every feature the result depends on; expected %s", r.Result)}
	}
	return Judgment{Fail, fmt.Sprintf("unknown expected result %q", r.Result)}
}

// worst combines the judgments of a case's parts, keyed by what each
// judges: any FAIL fails the case, then any SHORTFALL, and otherwise it
// passes.
func worst(parts map[string]Judgment) Judgment {
	var fails, shortfalls []string
	keys := make([]string, 0, len(parts))
	for k := range parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch j := parts[k]; j.Category {
		case Fail:
			fails = append(fails, k+": "+j.Detail)
		case Shortfall:
			shortfalls = append(shortfalls, k+": "+j.Detail)
		}
	}
	switch {
	case len(fails) > 0:
		return Judgment{Fail, strings.Join(fails, "; ")}
	case len(shortfalls) > 0:
		return Judgment{Shortfall, strings.Join(shortfalls, "; ")}
	}
	return Judgment{Pass, ""}
}

// JudgeValues judges a validate-operation-values case: answers holds the
// software's answer for each value, in order, Satisfies, Fails, or Decline.
func JudgeValues(expected json.RawMessage, answers []string, p Profile) Judgment {
	var e struct {
		Results   []json.RawMessage `json:"results"`
		DependsOn []string          `json:"dependsOn"`
	}
	if err := json.Unmarshal(expected, &e); err != nil {
		return Judgment{Fail, "unreadable expectation: " + err.Error()}
	}
	if len(answers) != len(e.Results) {
		return Judgment{Fail, fmt.Sprintf("%d answers for %d expected results", len(answers), len(e.Results))}
	}
	parts := map[string]Judgment{}
	for i, answer := range answers {
		parts[fmt.Sprintf("value %02d", i)] = judgeResult(e.Results[i], e.DependsOn, answer, p)
	}
	j := worst(parts)
	if j.Category == Pass {
		j.Detail = fmt.Sprint(answers)
	}
	return j
}

// exampleTokens reads an example's expected claim as the value result it
// states: a true claim is a value that satisfies its contract, a false one
// a value that fails it, and no claim a value where no contract is stated.
var exampleTokens = map[string]string{
	"true":      Satisfies,
	"false":     Fails,
	"undefined": "undefined",
	"external":  "external",
	"no-claim":  "no-contract",
}

// JudgeExamples judges a check-examples case: answers holds, per example
// and per side the example supplies, the software's answer for that value,
// Satisfies, Fails, or Decline. A claim's truth is judged as the value's
// result is: true as satisfies, false as fails, undefined and external as
// themselves, and no claim as no contract.
func JudgeExamples(expected json.RawMessage, answers map[string]map[string]string, p Profile) Judgment {
	var e struct {
		Examples map[string]map[string]string `json:"examples"`
	}
	if err := json.Unmarshal(expected, &e); err != nil {
		return Judgment{Fail, "unreadable expectation: " + err.Error()}
	}
	parts := map[string]Judgment{}
	for name, sides := range e.Examples {
		for side, claim := range sides {
			key := name + " " + side
			token, known := exampleTokens[claim]
			if !known {
				parts[key] = Judgment{Fail, "unknown expected claim " + claim}
				continue
			}
			answer, given := answers[name][side]
			if !given {
				parts[key] = Judgment{Fail, "no answer for a value the example supplies"}
				continue
			}
			raw, _ := json.Marshal(token)
			parts[key] = judgeResult(raw, nil, answer, p)
		}
	}
	for name, sides := range answers {
		for side := range sides {
			if _, expected := e.Examples[name][side]; !expected {
				parts[name+" "+side] = Judgment{Fail, "an answer for a value the case does not list"}
			}
		}
	}
	return worst(parts)
}

// DocumentAnswer is software's answer about a text: Conformant,
// NonConformant, or Decline (neither established), and the rules it reports
// violated.
type DocumentAnswer struct {
	Conclusion string
	Violated   []string
}

// judgeConformance judges a DocumentAnswer against whether the text
// conforms: a conforming text passes when concluded conformant, or declined
// under a dependsOn feature declared unsupported, and is a SHORTFALL when
// declined otherwise; a non-conforming text passes when concluded
// non-conformant with every rule of violates violated and none of
// notViolated. Every other answer FAILs.
func judgeConformance(conforms bool, violates, notViolated, dependsOn []string, a DocumentAnswer, p Profile) Judgment {
	unsupported, undeclared := p.lacking(dependsOn)
	if undeclared != "" {
		return Judgment{Fail, "the profile does not declare " + undeclared}
	}
	if conforms {
		switch a.Conclusion {
		case Conformant:
			return Judgment{Pass, Conformant}
		case Decline:
			if unsupported != "" {
				return Judgment{Pass, "declined under " + unsupported + ", declared unsupported"}
			}
			return Judgment{Shortfall, "declined on a conforming text where the profile supports every feature the case depends on"}
		}
		return Judgment{Fail, fmt.Sprintf("concluded %s for a conforming text (violated: %v)", a.Conclusion, a.Violated)}
	}
	if a.Conclusion != NonConformant {
		return Judgment{Fail, fmt.Sprintf("concluded %s for a text that does not conform", a.Conclusion)}
	}
	var problems []string
	for _, rule := range violates {
		if !slices.Contains(a.Violated, rule) {
			problems = append(problems, fmt.Sprintf("expected %s violated", rule))
		}
	}
	for _, rule := range notViolated {
		if slices.Contains(a.Violated, rule) {
			problems = append(problems, fmt.Sprintf("%s reported violated, which the case lists as not violated", rule))
		}
	}
	if len(problems) > 0 {
		return Judgment{Fail, fmt.Sprintf("%s (violated: %v)", strings.Join(problems, "; "), a.Violated)}
	}
	return Judgment{Pass, fmt.Sprintf("%s %v", NonConformant, a.Violated)}
}

// JudgeFixture judges a validity fixture test, raw, against the answer.
// Fixtures carry no dependsOn, so a decline on a conforming text is a
// SHORTFALL.
func JudgeFixture(raw json.RawMessage, a DocumentAnswer, p Profile) Judgment {
	var t struct {
		Valid       *bool    `json:"valid"`
		Violates    []string `json:"violates"`
		NotViolated []string `json:"notViolated"`
	}
	if err := json.Unmarshal(raw, &t); err != nil || t.Valid == nil {
		return Judgment{Fail, fmt.Sprintf("unreadable fixture test: %v", err)}
	}
	return judgeConformance(*t.Valid, t.Violates, t.NotViolated, nil, a, p)
}

// JudgeDocument judges a validate-document scenario's expected object
// against the answer.
func JudgeDocument(expected json.RawMessage, a DocumentAnswer, p Profile) Judgment {
	var e struct {
		Outcome   string   `json:"outcome"`
		Violates  []string `json:"violates"`
		DependsOn []string `json:"dependsOn"`
	}
	if err := json.Unmarshal(expected, &e); err != nil {
		return Judgment{Fail, "unreadable expectation: " + err.Error()}
	}
	if e.Outcome != Conformant && e.Outcome != NonConformant {
		return Judgment{Fail, fmt.Sprintf("unknown expected outcome %q", e.Outcome)}
	}
	return judgeConformance(e.Outcome == Conformant, e.Violates, nil, e.DependsOn, a, p)
}

// Ledger records each case's run category in one module's run.
type Ledger struct {
	mu       sync.Mutex
	outcomes map[string]Judgment
}

// Record records a case's judgment once.
func (l *Ledger) Record(id string, j Judgment) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.outcomes == nil {
		l.outcomes = map[string]Judgment{}
	}
	if prior, twice := l.outcomes[id]; twice {
		l.outcomes[id] = Judgment{Fail, fmt.Sprintf("recorded twice (%s, then %s)", prior.Category, j.Category)}
		return
	}
	l.outcomes[id] = j
}

// Outcome returns a case's recorded judgment.
func (l *Ledger) Outcome(id string) (Judgment, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	j, ok := l.outcomes[id]
	return j, ok
}

// Reconcile checks that module recorded exactly one outcome for every case
// designated to it, and nothing else. It returns the problems and a
// per-action count summary.
func (c *Corpus) Reconcile(module string, l *Ledger) (problems []string, summary string) {
	counts := map[string]map[string]int{}
	expected := map[string]bool{}
	for _, cs := range c.Cases {
		if m, _ := Designate(cs); m != module {
			continue
		}
		expected[cs.ID] = true
		j, ok := l.Outcome(cs.ID)
		if !ok {
			problems = append(problems, cs.ID+": neither executed nor omitted")
			continue
		}
		if counts[cs.Action] == nil {
			counts[cs.Action] = map[string]int{}
		}
		counts[cs.Action][j.Category]++
		counts[cs.Action]["cases"]++
	}
	l.mu.Lock()
	for id := range l.outcomes {
		if !expected[id] {
			problems = append(problems, id+": recorded, but not a case this module executes")
		}
	}
	l.mu.Unlock()
	sort.Strings(problems)
	var lines []string
	actions := make([]string, 0, len(counts))
	for a := range counts {
		actions = append(actions, a)
	}
	sort.Strings(actions)
	total := 0
	for _, a := range actions {
		n := counts[a]
		total += n["cases"]
		lines = append(lines, fmt.Sprintf("%s: %d cases (pass %d, FAIL %d, SHORTFALL %d, OMITTED %d)",
			a, n["cases"], n[Pass], n[Fail], n[Shortfall], n[Omitted]))
	}
	return problems, fmt.Sprintf("module %s: %d of the corpus's %d cases\n%s", module, total, len(c.Cases), strings.Join(lines, "\n"))
}
