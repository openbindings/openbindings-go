package openbindings

import (
	"maps"
	"slices"
	"testing"
)

// allRules returns evidence holding status for every document rule.
func allRules(status RuleEvidenceStatus) map[string]RuleEvidenceStatus {
	evidence := map[string]RuleEvidenceStatus{}
	for _, rule := range DocumentRules() {
		evidence[rule] = status
	}
	return evidence
}

// A conformant conclusion needs every document rule established with no
// violation (Reports and Verdicts), so a document rule missing from the
// evidence is inconclusive, and an empty map concludes undetermined.
func TestConcludeConformance(t *testing.T) {
	without := func(evidence map[string]RuleEvidenceStatus, rules ...string) map[string]RuleEvidenceStatus {
		evidence = maps.Clone(evidence)
		for _, rule := range rules {
			delete(evidence, rule)
		}
		return evidence
	}
	with := func(evidence map[string]RuleEvidenceStatus, rule string, status RuleEvidenceStatus) map[string]RuleEvidenceStatus {
		evidence = maps.Clone(evidence)
		evidence[rule] = status
		return evidence
	}
	satisfied := allRules(EvidenceSatisfied)
	for _, tc := range []struct {
		name                   string
		evidence               map[string]RuleEvidenceStatus
		conclusion             ConformanceConclusion
		violated, inconclusive []string
	}{
		{"every rule satisfied", satisfied, ConclusionConformant, nil, nil},
		{"a rule not applicable", with(satisfied, "OBI-08", EvidenceNotApplicable), ConclusionConformant, nil, nil},
		{"an empty map", map[string]RuleEvidenceStatus{}, ConclusionConformanceUndetermined, nil, DocumentRules()},
		{"a nil map", nil, ConclusionConformanceUndetermined, nil, DocumentRules()},
		{"two rules missing", without(satisfied, "OBI-12", "OBI-13"), ConclusionConformanceUndetermined, nil, []string{"OBI-12", "OBI-13"}},
		{"a rule inconclusive", with(satisfied, "OBI-10", EvidenceInconclusive), ConclusionConformanceUndetermined, nil, []string{"OBI-10"}},
		{"a violation among missing rules", map[string]RuleEvidenceStatus{"OBI-11": EvidenceViolated}, ConclusionNonConformant, []string{"OBI-11"}, slices.DeleteFunc(DocumentRules(), func(rule string) bool { return rule == "OBI-11" })},
		{"an unknown status", with(satisfied, "OBI-04", "maybe"), ConclusionConformanceUndetermined, nil, []string{"OBI-04"}},
		// Evidence under an identifier that is not a document rule decides
		// nothing: a conclusion is reached from the document rules alone.
		// OBI-00 and OBI-14 are shaped like rule identifiers, but this text
		// defines neither.
		{"complete evidence and a foreign violation", with(satisfied, "X-01", EvidenceViolated), ConclusionConformant, nil, nil},
		{"complete evidence and a foreign inconclusive", with(satisfied, "OBI-14", EvidenceInconclusive), ConclusionConformant, nil, nil},
		{"incomplete evidence and a foreign satisfied", with(without(satisfied, "OBI-06"), "X-01", EvidenceSatisfied), ConclusionConformanceUndetermined, nil, []string{"OBI-06"}},
		{"incomplete evidence and a foreign violation", with(without(satisfied, "OBI-06"), "X-01", EvidenceViolated), ConclusionConformanceUndetermined, nil, []string{"OBI-06"}},
		// A typo leaves its rule missing, so inconclusive: never a false
		// conformant, and never a false non-conformant.
		{"a satisfied typo", with(without(satisfied, "OBI-01"), "OBI-1", EvidenceSatisfied), ConclusionConformanceUndetermined, nil, []string{"OBI-01"}},
		{"a violated typo", with(without(satisfied, "OBI-01"), "OBI-1", EvidenceViolated), ConclusionConformanceUndetermined, nil, []string{"OBI-01"}},
		{"only foreign evidence", map[string]RuleEvidenceStatus{"X-01": EvidenceViolated, "OBI-00": EvidenceSatisfied}, ConclusionConformanceUndetermined, nil, DocumentRules()},
	} {
		report := ConcludeConformance(tc.evidence)
		if report.Conclusion != tc.conclusion || !slices.Equal(report.Violated, tc.violated) || !slices.Equal(report.Inconclusive, tc.inconclusive) {
			t.Errorf("%s: %s, violated %v, inconclusive %v; want %s, %v, %v", tc.name, report.Conclusion, report.Violated, report.Inconclusive, tc.conclusion, tc.violated, tc.inconclusive)
		}
		// The report carries the evidence it concluded from: exactly the
		// document rules, a missing one as inconclusive.
		if keys := slices.Sorted(maps.Keys(report.Evidence)); !slices.Equal(keys, DocumentRules()) {
			t.Errorf("%s: Evidence holds %v, want exactly the document rules", tc.name, keys)
		}
		for _, rule := range DocumentRules() {
			want, given := tc.evidence[rule]
			if !given {
				want = EvidenceInconclusive
			}
			if report.Evidence[rule] != want {
				t.Errorf("%s: %s is %q in the report, want %q", tc.name, rule, report.Evidence[rule], want)
			}
		}
		if report.Release != "" || report.Revision != "" || report.Findings != nil {
			t.Errorf("%s: a report from evidence alone carries %q, %q, %v", tc.name, report.Release, report.Revision, report.Findings)
		}
	}
	// The caller's map is not changed, its foreign entries included.
	evidence := map[string]RuleEvidenceStatus{"OBI-01": EvidenceSatisfied, "X-01": EvidenceViolated}
	kept := maps.Clone(evidence)
	report := ConcludeConformance(evidence)
	if !maps.Equal(evidence, kept) {
		t.Errorf("ConcludeConformance changed its argument: %v", evidence)
	}
	report.Evidence["OBI-02"] = EvidenceViolated
	if !maps.Equal(evidence, kept) {
		t.Errorf("the report's Evidence shares the caller's map: %v", evidence)
	}
}
