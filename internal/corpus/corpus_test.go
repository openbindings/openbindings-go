package corpus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeclarationMajorMinorLines(t *testing.T) {
	d := Declaration{Lines: []string{"2.0", "1.10"}, Prereleases: []string{"1.11.0-rc.1"}}
	for version, want := range map[string]bool{
		"1.10.0": true, "1.10.999999999999999999999": true, "1.10.3+build": true, "2.0.0": true,
		"1.9.99": false, "1.11.0": false, "2.1.0": false, "1.10.0-rc.1": false,
		"1.11.0-rc.1+build": true, "1.11.0-rc.2": false,
	} {
		if got := d.Supports(version); got != want {
			t.Errorf("Supports(%q) = %v, want %v", version, got, want)
		}
		if _, skipped := Gate(Gates{RequiresSupports: version}, d); skipped == want {
			t.Errorf("support gate for %q skipped %v, want %v", version, skipped, !want)
		}
	}
}

func TestDeclarationAndGates(t *testing.T) {
	d := Declaration{Lines: []string{"0.2"}, Prereleases: []string{"0.3.0-rc.1"}}
	for v, want := range map[string]bool{
		"0.2.0": true, "0.2.99": true, "0.2.0+build.1": true, "0.2.0-rc.1": false,
		"0.3.0-rc.1": true, "0.3.0": false, "0.1.9": false, "1.2.0": false, "0.2": false,
	} {
		if got := d.Supports(v); got != want {
			t.Errorf("Supports(%s) = %v, want %v", v, got, want)
		}
	}
	if got := (Declaration{Lines: []string{"1"}}).Supports("1.7.3"); got {
		t.Error("a major version alone does not declare a release line")
	}
	for g, skip := range map[Gates]bool{
		{RequiresSupports: "0.2.5"}:      false,
		{RequiresSupports: "1.0.0"}:      true,
		{RequiresSupports: "0.3.0-rc.1"}: false,
		{RequiresSupports: "0.2.0-rc.1"}: true,
	} {
		if _, got := Gate(g, d); got != skip {
			t.Errorf("Gate(%+v) skip = %v, want %v", g, got, skip)
		}
	}
}

func TestJudgeValues(t *testing.T) {
	p := Profile{Features: map[string]bool{"exact-numbers": true, "draft-07-dialect": false}}
	v, m, n := Observed{Verdict: "valid"}, Observed{Verdict: "instance-mismatch"}, Observed{Verdict: "no-verdict"}
	cases := []struct {
		expected string
		refused  bool
		observed []Observed
		want     string
	}{
		{`{"results":["valid","instance-mismatch"]}`, false, []Observed{v, m}, Pass},
		{`{"results":["valid"]}`, false, []Observed{m}, Fail},
		{`{"results":["valid"]}`, false, []Observed{n}, Shortfall},
		{`{"results":["no-verdict"]}`, false, []Observed{v}, Fail},
		{`{"results":[{"verdict":"valid","orNoVerdict":true}]}`, false, []Observed{n}, Pass},
		{`{"results":[{"verdict":"valid","orNoVerdict":true}]}`, false, []Observed{m}, Fail},
		{`{"results":["valid"],"dependsOn":["draft-07-dialect"]}`, false, []Observed{n}, Pass},
		{`{"results":["valid"],"dependsOn":["draft-07-dialect"]}`, false, []Observed{v}, Fail},
		{`{"results":[{"verdict":"valid","dependsOn":[]}],"dependsOn":["draft-07-dialect"]}`, false, []Observed{v}, Pass},
		{`{"results":["valid"],"dependsOn":["undeclared"]}`, false, []Observed{v}, Fail},
		{`{"results":["no-verdict"],"forbidReasons":["no-contract"]}`, false, []Observed{{Verdict: "no-verdict", Reason: "no-contract"}}, Fail},
		{`{"results":["valid"]}`, false, []Observed{{Verdict: "no-verdict", Reason: "resource-limit"}}, Omitted},
		{`{"results":["valid"]}`, true, nil, Fail},
		{`{"results":["valid","valid"]}`, false, []Observed{v}, Fail},
	}
	for i, c := range cases {
		if got := JudgeValues(json.RawMessage(c.expected), c.refused, c.observed, p); got.Category != c.want {
			t.Errorf("case %d (%s): %s (%s), want %s", i, c.expected, got.Category, got.Detail, c.want)
		}
	}
}

func TestReconcile(t *testing.T) {
	c := &Corpus{Cases: []Case{{ID: "a", Action: ActionValidity}, {ID: "b", Action: "derive-form"}, {ID: "c", Action: "validate-operation-values"}}}
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

// Load reads scenario format @2 alone: a scenario file in format @1, the
// format the corpus used before the revised text, is refused.
func TestLoad_ReadsFormatV2Alone(t *testing.T) {
	for format, refused := range map[string]bool{FormatV2: false, "openbindings.core-tool-scenarios@1": true} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "scenarios"), 0o755); err != nil {
			t.Fatal(err)
		}
		scenarios := `{"format": "` + format + `", "rule": "OBI-T-08", "scenarios": [{"id": "T08-S-01", "action": "conclude-conformance", "given": {"evidence": {}}, "expected": {"conclusion": "conformance-undetermined"}}]}`
		manifest := `{"files": [], "scenarioFiles": [{"path": "scenarios/OBI-T-08.json", "scenarios": 1}]}`
		for name, content := range map[string]string{"scenarios/OBI-T-08.json": scenarios, "manifest.json": manifest} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Load(dir)
		if got := err != nil && strings.Contains(err.Error(), "unknown format"); got != refused {
			t.Errorf("%s: refused %v, want %v (%v)", format, got, refused, err)
		}
	}
}
