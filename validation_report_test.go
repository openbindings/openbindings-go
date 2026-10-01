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

// OBI-T-09: conformance is claimed only when every document rule has been
// established with no violation, so a document rule missing from the
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
		{"a rule not applicable", with(satisfied, "OBI-D-11", EvidenceNotApplicable), ConclusionConformant, nil, nil},
		{"an empty map", map[string]RuleEvidenceStatus{}, ConclusionConformanceUndetermined, nil, DocumentRules()},
		{"a nil map", nil, ConclusionConformanceUndetermined, nil, DocumentRules()},
		{"two rules missing", without(satisfied, "OBI-D-12", "OBI-D-13"), ConclusionConformanceUndetermined, nil, []string{"OBI-D-12", "OBI-D-13"}},
		{"a rule inconclusive", with(satisfied, "OBI-D-10", EvidenceInconclusive), ConclusionConformanceUndetermined, nil, []string{"OBI-D-10"}},
		{"a violation among missing rules", map[string]RuleEvidenceStatus{"OBI-D-05": EvidenceViolated}, ConclusionNonConformant, []string{"OBI-D-05"}, slices.DeleteFunc(DocumentRules(), func(rule string) bool { return rule == "OBI-D-05" })},
		{"an unknown status", with(satisfied, "OBI-D-03", "maybe"), ConclusionConformanceUndetermined, nil, []string{"OBI-D-03"}},
		{"a rule outside the document rules", with(satisfied, "X-01", EvidenceViolated), ConclusionNonConformant, []string{"X-01"}, nil},
	} {
		report := ConcludeConformance(tc.evidence)
		if report.Conclusion != tc.conclusion || !slices.Equal(report.Violated, tc.violated) || !slices.Equal(report.Inconclusive, tc.inconclusive) {
			t.Errorf("%s: %s, violated %v, inconclusive %v; want %s, %v, %v", tc.name, report.Conclusion, report.Violated, report.Inconclusive, tc.conclusion, tc.violated, tc.inconclusive)
		}
		// The report carries the evidence it concluded from: every document
		// rule, a missing one as inconclusive.
		for _, rule := range DocumentRules() {
			want, given := tc.evidence[rule]
			if !given {
				want = EvidenceInconclusive
			}
			if report.Evidence[rule] != want {
				t.Errorf("%s: %s is %q in the report, want %q", tc.name, rule, report.Evidence[rule], want)
			}
		}
		if report.Version != "" || report.Revision != "" || report.Findings != nil {
			t.Errorf("%s: a report from evidence alone carries %q, %q, %v", tc.name, report.Version, report.Revision, report.Findings)
		}
	}
	// The caller's map is not changed.
	evidence := map[string]RuleEvidenceStatus{"OBI-D-01": EvidenceSatisfied}
	ConcludeConformance(evidence)
	if len(evidence) != 1 {
		t.Errorf("ConcludeConformance changed its argument: %v", evidence)
	}
}
