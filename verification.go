package openbindings

import "sort"

// RuleEvidenceStatus records a verifier's evidence for one applicable or
// considered OBI-D rule. The statuses mirror §10.5 of the core specification.
type RuleEvidenceStatus string

const (
	EvidenceSatisfied     RuleEvidenceStatus = "satisfied"
	EvidenceViolated      RuleEvidenceStatus = "violated"
	EvidenceUnverified    RuleEvidenceStatus = "unverified"
	EvidenceNotApplicable RuleEvidenceStatus = "not-applicable"
)

// VerificationConclusion is the portable conclusion a verifier may report
// after applying OBI-T-17 to its collected rule evidence.
type VerificationConclusion string

const (
	ConclusionConformant              VerificationConclusion = "conformant"
	ConclusionNonConformant           VerificationConclusion = "non-conformant"
	ConclusionConformanceUndetermined VerificationConclusion = "conformance-undetermined"
)

// VerificationReport preserves both decisive violations and incomplete
// checks. Rule identifiers are sorted for deterministic SDK output; the core
// specification requires their identity, not this presentation order.
type VerificationReport struct {
	Conclusion VerificationConclusion
	Violated   []string
	Unverified []string
}

// ConcludeVerification applies OBI-T-17's truth conditions to a complete map
// of rule evidence. The caller supplies every rule applicable to the
// verification; absence is not itself an evidence status. A violation is
// decisive even when other rules remain unverified. In the absence of a
// violation, any unverified applicable rule makes the conclusion
// undetermined; otherwise the conclusion is conformant. An unrecognized
// runtime status is treated conservatively as unverified rather than allowing
// malformed evidence to produce a conformant conclusion.
func ConcludeVerification(evidence map[string]RuleEvidenceStatus) VerificationReport {
	report := VerificationReport{}
	for rule, status := range evidence {
		switch status {
		case EvidenceViolated:
			report.Violated = append(report.Violated, rule)
		case EvidenceUnverified:
			report.Unverified = append(report.Unverified, rule)
		case EvidenceSatisfied, EvidenceNotApplicable:
			// Neither contributes to the decisive or incomplete sets.
		default:
			report.Unverified = append(report.Unverified, rule)
		}
	}
	sort.Strings(report.Violated)
	sort.Strings(report.Unverified)
	switch {
	case len(report.Violated) > 0:
		report.Conclusion = ConclusionNonConformant
	case len(report.Unverified) > 0:
		report.Conclusion = ConclusionConformanceUndetermined
	default:
		report.Conclusion = ConclusionConformant
	}
	return report
}
