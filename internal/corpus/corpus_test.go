package corpus

import (
	"encoding/json"
	"testing"
)

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
	if got := (Declaration{Lines: []string{"1"}}).Supports("1.7.3"); !got {
		t.Error("a post-1.0 line is its major version")
	}
	if got := d.Lowest(); got != "0.2.0" {
		t.Errorf("Lowest = %s", got)
	}
	for g, skip := range map[Gates]bool{
		{RequiresSupports: "0.2.5"}:                                   false,
		{RequiresSupports: "1.0.0"}:                                   true,
		{RequiresUnsupported: "0.2.0-rc.1"}:                           false,
		{RequiresUnsupported: "0.2.1"}:                                true,
		{RequiresMinSupported: "0.2.0"}:                               false,
		{RequiresMinSupported: "0.3.0"}:                               true,
		{RequiresMinSupported: "0.99999999999999999999.0"}:            true,
		{RequiresSupports: "0.2.0", RequiresUnsupported: "999.0.0"}:   false,
		{RequiresMinSupported: "0.2.0", RequiresUnsupported: "0.1.0"}: false,
		{RequiresSupports: "0.2.0", RequiresMinSupported: "0.2.1"}:    true,
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
		format, expected   string
		refused, exclusive bool
		observed           []Observed
		want               string
	}{
		{FormatV2, `{"results":["valid","instance-mismatch"]}`, false, false, []Observed{v, m}, Pass},
		{FormatV2, `{"results":["valid"]}`, false, false, []Observed{m}, Fail},
		{FormatV2, `{"results":["valid"]}`, false, false, []Observed{n}, Shortfall},
		{FormatV2, `{"results":["no-verdict"]}`, false, false, []Observed{v}, Fail},
		{FormatV2, `{"results":[{"verdict":"valid","orNoVerdict":true}]}`, false, false, []Observed{n}, Pass},
		{FormatV2, `{"results":[{"verdict":"valid","orNoVerdict":true}]}`, false, false, []Observed{m}, Fail},
		{FormatV2, `{"results":["valid"],"dependsOn":["draft-07-dialect"]}`, false, false, []Observed{n}, Pass},
		{FormatV2, `{"results":["valid"],"dependsOn":["draft-07-dialect"]}`, false, false, []Observed{v}, Fail},
		{FormatV2, `{"results":[{"verdict":"valid","dependsOn":[]}],"dependsOn":["draft-07-dialect"]}`, false, false, []Observed{v}, Pass},
		{FormatV2, `{"results":["valid"],"dependsOn":["undeclared"]}`, false, false, []Observed{v}, Fail},
		{FormatV2, `{"results":["no-verdict"],"forbidReasons":["no-contract"]}`, false, false, []Observed{{Verdict: "no-verdict", Reason: "no-contract"}}, Fail},
		{FormatV2, `{"results":["valid"]}`, false, false, []Observed{{Verdict: "no-verdict", Reason: "resource-limit"}}, Omitted},
		{FormatV2, `{"outcome":"version-refusal"}`, true, true, nil, Pass},
		{FormatV2, `{"outcome":"version-refusal"}`, true, false, nil, Fail},
		{FormatV2, `{"results":["valid"]}`, true, true, nil, Fail},
		{FormatV2, `{"outcome":"version-refusal"}`, false, false, []Observed{v}, Fail},
		{FormatV2, `{"results":["valid","valid"]}`, false, false, []Observed{v}, Fail},
		{FormatV1, `{"results":["valid","graph-unavailable"]}`, false, false, []Observed{v, n}, Pass},
		{FormatV1, `{"results":["valid","instance-mismatch"]}`, false, false, []Observed{v, n}, Fail},
		{FormatV1, `{"allowedOutcomes":["valid","resolver-error"]}`, false, false, []Observed{n}, Pass},
		{FormatV1, `{"allowedOutcomes":["valid","resolver-error"]}`, false, false, []Observed{m}, Fail},
	}
	for i, c := range cases {
		if got := JudgeValues(c.format, json.RawMessage(c.expected), c.refused, c.exclusive, c.observed, p); got.Category != c.want {
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
