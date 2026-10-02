// Package corpus reads the specification's conformance corpus for this SDK's
// corpus adapters, and judges what can be judged without the SDK: version
// gates against a support declaration, value-result expectations against
// observed verdicts and a declared profile, and the count of cases each
// module executed or omitted. It imports nothing from the SDK, so the core
// module's own tests and the schemaeval module's tests can both use it
// without an import cycle: the core's tests execute the actions that need no
// schema evaluator, and schemaeval's tests execute the value actions under
// the project's ECMA-262 evaluator.
//
// It reads the validity fixtures and the scenarios, in format
// openbindings.core-tool-scenarios@2, the format of the corpus of the text
// this SDK applies; a scenario file in any other format is refused.
package corpus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Formats of a case.
const (
	FormatFixture = "fixture"
	FormatV2      = "openbindings.core-tool-scenarios@2"
)

// ActionValidity is the action of a validity fixture.
const ActionValidity = "validity"

// Modules that execute cases.
const (
	ModuleRoot       = "root"
	ModuleSchemaeval = "schemaeval"
	ModuleNone       = "none"
)

// Case is one corpus case: a validity fixture test or a scenario.
type Case struct {
	// ID is the scenario ID, or for a fixture test its file and position
	// (document/OBI-D-12.json#/tests/40).
	ID          string
	File        string
	Rule        string
	Format      string
	Action      string
	Description string
	Raw         json.RawMessage
	Gates       Gates
}

// Gates are a case's version gates.
type Gates struct {
	RequiresSupports     string `json:"requiresSupports"`
	RequiresUnsupported  string `json:"requiresUnsupported"`
	RequiresMinSupported string `json:"requiresMinSupported"`
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
	Rule  string            `json:"rule"`
	Tests []json.RawMessage `json:"tests"`
}

type scenarioFile struct {
	Format    string            `json:"format"`
	Rule      string            `json:"rule"`
	Scenarios []json.RawMessage `json:"scenarios"`
}

type caseHeader struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Gates
}

// Load reads every fixture and scenario file under dir, and checks the count
// of cases in each against the manifest: every case the manifest counts is
// read, and no other.
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
	for _, sub := range []string{"document", "tool"} {
		names, err := jsonFiles(filepath.Join(dir, sub))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			rel := sub + "/" + name
			data, err := os.ReadFile(filepath.Join(dir, sub, name))
			if err != nil {
				return nil, err
			}
			var f fixtureFile
			if err := json.Unmarshal(data, &f); err != nil {
				return nil, fmt.Errorf("parsing %s: %w", rel, err)
			}
			for i, raw := range f.Tests {
				var h caseHeader
				if err := json.Unmarshal(raw, &h); err != nil {
					return nil, fmt.Errorf("parsing %s test %d: %w", rel, i, err)
				}
				c.Cases = append(c.Cases, Case{ID: fmt.Sprintf("%s#/tests/%d", rel, i), File: rel, Rule: f.Rule, Format: FormatFixture,
					Action: ActionValidity, Description: h.Description, Raw: raw, Gates: h.Gates})
			}
			read[rel] = len(f.Tests)
		}
	}
	names, err := jsonFiles(filepath.Join(dir, "scenarios"))
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
		if f.Format != FormatV2 {
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
			if _, known := designations[h.Action]; !known {
				return nil, fmt.Errorf("%s: scenario %s has unknown action %q", rel, h.ID, h.Action)
			}
			c.Cases = append(c.Cases, Case{ID: h.ID, File: rel, Rule: f.Rule, Format: f.Format, Action: h.Action,
				Description: h.Description, Raw: raw, Gates: h.Gates})
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
// reason a module that does not execute it gives.
var designations = map[string]struct{ module, reason string }{
	ActionValidity:              {ModuleRoot, ""},
	"validate-document":         {ModuleRoot, ""},
	"resolve-operation":         {ModuleRoot, ""},
	"conclude-conformance":      {ModuleRoot, ""},
	"check-dependency-kind":     {ModuleRoot, ""},
	"validate-operation-values": {ModuleSchemaeval, "value validation runs in the schemaeval module, under the project's ECMA-262 evaluator"},
	"check-examples":            {ModuleSchemaeval, "example checking composes value validation, which runs in the schemaeval module"},
	"derive-form":               {ModuleNone, "this SDK derives no forms from a schema (OBI-T-05 has no executor here)"},
}

// Designate names the module that executes a case, and the reason the other
// modules omit it.
func Designate(c Case) (module, reason string) {
	d := designations[c.Action]
	return d.module, d.reason
}

// Declaration is a tool's support declaration (§8.1): the release lines it
// supports and the prereleases it includes. Gates are judged against it,
// never against the tool's acceptance or refusal code.
type Declaration struct {
	Lines       []string // major.minor, e.g. "0.2" or "1.0"
	Prereleases []string // "0.2.0-rc.1"
}

var semverRE = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// Supports reports whether the declaration includes version v: a release
// whose major.minor line it declares, build metadata ignored, or a
// prerelease it declares by its full version.
func (d Declaration) Supports(v string) bool {
	m := semverRE.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	if m[4] != "" {
		return slices.Contains(d.Prereleases, m[1]+"."+m[2]+"."+m[3]+"-"+m[4])
	}
	return slices.Contains(d.Lines, m[1]+"."+m[2])
}

// Lowest is the first release of the lowest declared line.
func (d Declaration) Lowest() string {
	lowest := ""
	for _, line := range d.Lines {
		v := line + ".0"
		if lowest == "" || compareRelease(v, lowest) < 0 {
			lowest = v
		}
	}
	return lowest
}

// compareRelease orders major.minor.patch as digit strings, never machine
// integers, so versions of any length compare exactly.
func compareRelease(a, b string) int {
	pa, pb := semverRE.FindStringSubmatch(a), semverRE.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return strings.Compare(a, b)
	}
	for i := 1; i <= 3; i++ {
		if c := len(pa[i]) - len(pb[i]); c != 0 {
			return c
		}
		if c := strings.Compare(pa[i], pb[i]); c != 0 {
			return c
		}
	}
	return 0
}

// Gate reports whether a case's gates exclude it for a tool with declaration
// d, and why.
func Gate(g Gates, d Declaration) (reason string, skip bool) {
	if v := g.RequiresSupports; v != "" && !d.Supports(v) {
		return "gate: requires a tool declaring support for " + v, true
	}
	if v := g.RequiresUnsupported; v != "" && d.Supports(v) {
		return "gate: requires a tool not declaring support for " + v, true
	}
	if v := g.RequiresMinSupported; v != "" && compareRelease(d.Lowest(), v) < 0 {
		return "gate: requires a lowest supported version of at least " + v, true
	}
	return "", false
}

// Profile is the capability profile an adapter declares: every feature the
// corpus's cases depend on, supported or not.
type Profile struct {
	Features map[string]bool
}

// Run categories.
const (
	Pass       = "pass"
	Fail       = "FAIL"
	Shortfall  = "SHORTFALL"
	Omitted    = "OMITTED"
	Advisory   = "ADVISORY"
	Unverified = "UNVERIFIED"
)

// Judgment is a case's run category and its detail.
type Judgment struct {
	Category string
	Detail   string
}

// Observed is one value's observed answer: its verdict (valid,
// instance-mismatch, or no-verdict) and, for a no-verdict, the reason the
// adapter reads from the answer (no-contract, undefined-result, or "").
type Observed struct {
	Verdict, Reason string
}

// JudgeValues judges a value case's observed answers against its expected
// object. refused and exclusive say whether the answer was a version refusal
// and whether it came with nothing else.
func JudgeValues(expected json.RawMessage, refused, exclusive bool, observed []Observed, p Profile) Judgment {
	var e struct {
		Outcome       string            `json:"outcome"`
		Results       []json.RawMessage `json:"results"`
		DependsOn     []string          `json:"dependsOn"`
		ForbidReasons []string          `json:"forbidReasons"`
	}
	if err := json.Unmarshal(expected, &e); err != nil {
		return Judgment{Fail, "unreadable expectation: " + err.Error()}
	}
	switch {
	case refused && !exclusive:
		return Judgment{Fail, "a version refusal came with a result"}
	case refused && e.Outcome == "version-refusal":
		return Judgment{Pass, "version-refusal"}
	case refused:
		return Judgment{Fail, "version-refusal; expected value results"}
	case e.Outcome != "":
		return Judgment{Fail, fmt.Sprintf("the document was interpreted; expected %s", e.Outcome)}
	}
	if len(observed) != len(e.Results) {
		return Judgment{Fail, fmt.Sprintf("%d results for %d expected", len(observed), len(e.Results))}
	}
	var got []string
	shortfall, omission := "", ""
	for i, o := range observed {
		got = append(got, o.Verdict)
		if o.Reason != "" && slices.Contains(e.ForbidReasons, o.Reason) {
			return Judgment{Fail, fmt.Sprintf("value %d: reason %s is forbidden here", i, o.Reason)}
		}
		var form struct {
			Verdict     string   `json:"verdict"`
			OrNoVerdict bool     `json:"orNoVerdict"`
			DependsOn   []string `json:"dependsOn"`
		}
		var token string
		if json.Unmarshal(e.Results[i], &token) == nil {
			form.Verdict = token
		} else if err := json.Unmarshal(e.Results[i], &form); err != nil {
			return Judgment{Fail, "unreadable expected result: " + err.Error()}
		}
		depends := e.DependsOn
		if form.DependsOn != nil {
			depends = form.DependsOn
		}
		lacking := ""
		for _, f := range depends {
			supported, declared := p.Features[f]
			if !declared {
				return Judgment{Fail, "the profile does not declare " + f}
			}
			if !supported {
				lacking = f
			}
		}
		switch {
		case form.Verdict == "no-verdict" || (lacking != "" && !form.OrNoVerdict):
			if o.Verdict != "no-verdict" {
				why := "no verdict is required"
				if lacking != "" {
					why = "the profile declares " + lacking + " unsupported, so no verdict is required"
				}
				return Judgment{Fail, fmt.Sprintf("value %d: %s; got %s", i, why, o.Verdict)}
			}
		case o.Verdict == form.Verdict:
		case o.Verdict == "no-verdict" && form.OrNoVerdict:
		case o.Verdict == "no-verdict" && o.Reason == "resource-limit":
			omission = fmt.Sprintf("value %d: no verdict at a reported resource limit", i)
		case o.Verdict == "no-verdict":
			shortfall = fmt.Sprintf("value %d: no verdict where the profile supports every feature the case depends on", i)
		default:
			return Judgment{Fail, fmt.Sprintf("value %d: got %s; expected %s", i, o.Verdict, string(e.Results[i]))}
		}
	}
	switch {
	case shortfall != "":
		return Judgment{Shortfall, fmt.Sprintf("%s %v", shortfall, got)}
	case omission != "":
		return Judgment{Omitted, fmt.Sprintf("%s %v", omission, got)}
	}
	return Judgment{Pass, fmt.Sprint(got)}
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
// designated to it, and for module root also every case no module executes,
// and nothing else. It returns the problems and a per-action count summary.
func (c *Corpus) Reconcile(module string, l *Ledger) (problems []string, summary string) {
	counts := map[string]map[string]int{}
	expected := map[string]bool{}
	for _, cs := range c.Cases {
		m, _ := Designate(cs)
		if m != module && !(module == ModuleRoot && m == ModuleNone) {
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
		lines = append(lines, fmt.Sprintf("%s: %d cases (pass %d, FAIL %d, OMITTED %d, SHORTFALL %d, ADVISORY %d, UNVERIFIED %d)",
			a, n["cases"], n[Pass], n[Fail], n[Omitted], n[Shortfall], n[Advisory], n[Unverified]))
	}
	return problems, fmt.Sprintf("module %s: %d of the corpus's %d cases\n%s", module, total, len(c.Cases), strings.Join(lines, "\n"))
}
