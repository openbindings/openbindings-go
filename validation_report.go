package openbindings

import (
	"fmt"
	"slices"
)

// RuleEvidenceStatus records a validator's evidence for one applicable or
// considered OBI-D rule. The statuses mirror §10.4 of the core specification.
type RuleEvidenceStatus string

const (
	EvidenceSatisfied     RuleEvidenceStatus = "satisfied"
	EvidenceViolated      RuleEvidenceStatus = "violated"
	EvidenceInconclusive  RuleEvidenceStatus = "inconclusive"
	EvidenceNotApplicable RuleEvidenceStatus = "not-applicable"
)

// ConformanceConclusion is the portable conclusion a validator may report
// after applying OBI-T-08 to its collected rule evidence.
type ConformanceConclusion string

const (
	ConclusionConformant              ConformanceConclusion = "conformant"
	ConclusionNonConformant           ConformanceConclusion = "non-conformant"
	ConclusionConformanceUndetermined ConformanceConclusion = "conformance-undetermined"
)

// documentRules is every document rule the core specification defines
// (§10.2), in identifier order.
var documentRules = []string{
	"OBI-01", "OBI-02", "OBI-03", "OBI-04", "OBI-05", "OBI-06",
	"OBI-07", "OBI-08", "OBI-09", "OBI-10", "OBI-11", "OBI-12",
	"OBI-13",
}

// DocumentRules returns the identifiers of every document rule the core
// specification defines, in identifier order. Every ValidationReport
// Document.Validate or ValidateDocument returns carries evidence for each of
// them. A version refusal returns no report, nor does a host object the
// caller made unencodable (a NaN, invalid UTF-8 in a Go string, a
// marshaler's error); a host object beyond this SDK's own limits gets a
// report, deciding at most OBI-03.
func DocumentRules() []string {
	return append([]string(nil), documentRules...)
}

// Finding is one located piece of rule evidence: a violation established at a
// document position, or a check this validator could not decide there.
type Finding struct {
	// Rule is the stable rule identifier, such as "OBI-06".
	Rule string
	// Status is EvidenceViolated or EvidenceInconclusive.
	Status RuleEvidenceStatus
	// Path locates the finding in the document as an RFC 6901 JSON Pointer,
	// such as "/bindings/createTask/operation". A finding about a missing
	// member is located at the object that lacks it. The empty pointer is
	// the whole document.
	Path string
	// Message states what was established, or why it could not be decided.
	Message string
	// Position is where the finding lies in the input bytes, from
	// ValidateDocument and ParseDocument; the zero Position otherwise.
	Position Position
}

// ValidationReport is a validator's account of one document under the core
// specification's §10.4 vocabulary.
//
// Conclusion is conformant only when every document rule was decided and none
// was violated; any established violation makes it non-conformant; otherwise
// any inconclusive rule makes it conformance-undetermined. Absence of a
// violation is therefore not conformance: a caller reporting a result must use
// Conclusion, and must not present an undetermined result as conformant.
type ValidationReport struct {
	// Release is the release of the specification whose text the report
	// applies, and to which its rule identifiers belong (§10, OBI-T-08):
	// 0.2.0, as its working draft until that version is released, whatever
	// release of the 0.2 line the document declares, since the patch number
	// a document declares carries no meaning (§8.1). A report
	// ConcludeConformance builds from evidence alone carries no Release.
	Release string
	// Revision is the source-control revision of the specification text the
	// report applies, a commit of github.com/openbindings/spec, while Release
	// names a working draft rather than a published release (OBI-T-08); it
	// is empty when Release names a published release, and in a report
	// ConcludeConformance builds from evidence alone.
	Revision   string
	Conclusion ConformanceConclusion
	// Evidence holds one status per document rule (DocumentRules), the
	// evidence the conclusion was reached from. Reports from
	// Document.Validate, ValidateDocument, and ConcludeConformance carry
	// exactly the document rules. In a validator's report, a rule with
	// nothing to govern in the document is vacuously satisfied, and on a
	// text violating OBI-01 every other rule is not applicable (§10). A
	// version refusal, or a host object the caller made unencodable,
	// returns no report, whose Evidence is nil; a host object beyond this
	// SDK's own limits gets a report whose Evidence decides at most
	// OBI-03.
	Evidence map[string]RuleEvidenceStatus
	// Violated and Inconclusive identify rules by their identifiers in
	// Release, in identifier order. These lists are SDK report fields.
	Violated     []string
	Inconclusive []string
	// Findings locate every established violation and every undecided check,
	// in the order the validator records them, which is the same every time
	// for the same document: its checks run in a fixed sequence, and the
	// document schema's findings (OBI-02), which its library reports in no
	// fixed order, are ordered by where the failing keyword applies (the
	// object, for a member the schema does not allow; the member, for a
	// member name it refuses; the value otherwise), comparing reference
	// tokens one by one as strings, so /a/10 comes before /a/2, then by
	// message. A report is as large as what it reports: each finding's Path
	// is as long as its location is deep, so a deeply nested document with a
	// finding at every level makes a report that grows with the square of
	// its depth. Findings are not capped.
	Findings []Finding
}

// Violations returns the findings that established a violation.
func (r ValidationReport) Violations() []Finding {
	return r.findingsWith(EvidenceViolated)
}

// InconclusiveChecks returns the findings this validator could not decide.
func (r ValidationReport) InconclusiveChecks() []Finding {
	return r.findingsWith(EvidenceInconclusive)
}

func (r ValidationReport) findingsWith(status RuleEvidenceStatus) []Finding {
	var out []Finding
	for _, finding := range r.Findings {
		if finding.Status == status {
			out = append(out, finding)
		}
	}
	return out
}

// ConcludeConformance applies OBI-T-08's truth conditions to rule evidence
// and returns the report they conclude. It concludes from the document rules
// alone (DocumentRules), since §10.4 defines each conclusion by the document
// rules: evidence under any other identifier is dropped and decides nothing.
// Each document rule applies to every text that holds OBI-01; on a text
// violating it, OBI-02 through OBI-13 are not applicable and the
// OBI-01 violation alone establishes non-conformance (§10). Absence is no
// evidence either way, so a rule missing from the evidence is inconclusive,
// and an empty map concludes conformance undetermined. A mistyped
// identifier, such as "OBI-1", is therefore dropped and leaves its rule
// missing, so it never makes a conclusion conformant or non-conformant.
//
// A violation is decisive even when other rules remain inconclusive. In the
// absence of a violation, any inconclusive rule makes the conclusion
// undetermined; otherwise the conclusion is conformant. A status other than
// the four this package defines is treated as inconclusive, so malformed
// evidence never concludes conformant.
//
// The returned report's Evidence holds exactly the document rules, the
// evidence it concluded from: each rule's status as given, or inconclusive
// where the evidence omits the rule. It has the shape of the Evidence
// Document.Validate and ValidateDocument return. The report carries no
// findings, and no Release or Revision, since evidence alone names no
// specification text. The caller's map is not changed.
//
// A caller holding evidence this SDK cannot produce, such as its own
// decision of a rule a report left inconclusive, amends that report under
// these invariants:
//   - The decision settles the whole rule: satisfied, violated with the
//     findings that establish the violation, or not applicable. Another
//     inconclusive answer amends nothing.
//   - The rule's earlier findings go, and the decision's findings take their
//     place; every other rule's evidence and findings stay.
//   - The Conclusion and the Violated and Inconclusive lists are recomputed
//     by concluding again from the amended Evidence with ConcludeConformance,
//     never edited.
//   - Release and Revision are copied from the report: the amending caller
//     applied the same specification text (OBI-T-08). A caller that applied
//     other text does not amend this report.
func ConcludeConformance(evidence map[string]RuleEvidenceStatus) ValidationReport {
	report := ValidationReport{Evidence: make(map[string]RuleEvidenceStatus, len(documentRules))}
	// documentRules is in identifier order, so the lists built here are too.
	for _, rule := range documentRules {
		status, given := evidence[rule]
		if !given {
			status = EvidenceInconclusive
		}
		report.Evidence[rule] = status
		switch status {
		case EvidenceViolated:
			report.Violated = append(report.Violated, rule)
		case EvidenceSatisfied, EvidenceNotApplicable:
			// Neither contributes to the decisive or incomplete sets.
		default:
			// Inconclusive, or a status this package does not define.
			report.Inconclusive = append(report.Inconclusive, rule)
		}
	}
	switch {
	case len(report.Violated) > 0:
		report.Conclusion = ConclusionNonConformant
	case len(report.Inconclusive) > 0:
		report.Conclusion = ConclusionConformanceUndetermined
	default:
		report.Conclusion = ConclusionConformant
	}
	return report
}

// VersionRefusalError reports this SDK's version refusal: the document
// declares a well-formed version (§8.1) outside SupportedVersions, whose text
// this SDK does not apply, so it does not interpret the document at all.
// Refusing is this SDK's policy; the specification leaves it to each tool
// (§10.3). A refusal is not a conformance conclusion; validation returns it
// instead of a report.
type VersionRefusalError struct {
	// Version is the document's declared openbindings value.
	Version string
	// Reason says why the version is refused, such as which way it misses
	// the supported line. It is advisory text for a person; its wording is
	// not part of the API.
	Reason string
}

func (e *VersionRefusalError) Error() string {
	return "openbindings: " + e.Reason
}

// ruleChecks collects located evidence while a validator runs. Rules that
// record no finding are satisfied, or not applicable where notApplicable
// says so.
type ruleChecks struct {
	release, revision string
	findings          []Finding
	// notApplicable holds the rules that impose nothing on the document,
	// which record no finding: every rule but OBI-01 for a text violating
	// OBI-01 (§10).
	notApplicable map[string]bool
}

func (c *ruleChecks) violated(rule, path, message string) {
	c.findings = append(c.findings, Finding{Rule: rule, Status: EvidenceViolated, Path: path, Message: message})
}

func (c *ruleChecks) inconclusive(rule, path, message string) {
	c.findings = append(c.findings, Finding{Rule: rule, Status: EvidenceInconclusive, Path: path, Message: message})
}

// inconclusiveExcept leaves every document rule but the decided ones
// inconclusive for one reason, when validation cannot proceed past them.
func (c *ruleChecks) inconclusiveExcept(reason string, decided ...string) {
	skip := map[string]bool{}
	for _, rule := range decided {
		skip[rule] = true
	}
	for _, rule := range documentRules {
		if !skip[rule] {
			c.inconclusive(rule, "", reason)
		}
	}
}

// notApplicableExcept records every document rule but the decided ones as not
// applicable, when what was decided leaves the others nothing to require.
func (c *ruleChecks) notApplicableExcept(decided ...string) {
	c.notApplicable = map[string]bool{}
	for _, rule := range documentRules {
		if !slices.Contains(decided, rule) {
			c.notApplicable[rule] = true
		}
	}
}

// report concludes over every document rule: violated when any violation was
// recorded for it, inconclusive when only undecided checks were, not
// applicable when recorded so, and satisfied otherwise.
func (c *ruleChecks) report() ValidationReport {
	evidence := make(map[string]RuleEvidenceStatus, len(documentRules))
	for _, rule := range documentRules {
		evidence[rule] = EvidenceSatisfied
		if c.notApplicable[rule] {
			evidence[rule] = EvidenceNotApplicable
		}
	}
	for _, finding := range c.findings {
		switch finding.Status {
		case EvidenceViolated:
			evidence[finding.Rule] = EvidenceViolated
		case EvidenceInconclusive:
			if evidence[finding.Rule] != EvidenceViolated {
				evidence[finding.Rule] = EvidenceInconclusive
			}
		}
	}
	report := ConcludeConformance(evidence)
	report.Release, report.Revision = c.release, c.revision
	report.Findings = append([]Finding(nil), c.findings...)
	return report
}

// conclude returns the report together with the error validation returns: a
// *ValidationError listing every established violation, or nil when none was
// established. Inconclusive checks are not violations and do
// not appear in the error.
func (c *ruleChecks) conclude() (ValidationReport, error) {
	return c.report(), c.violationError()
}

func (c *ruleChecks) violationError() error {
	var violations []Finding
	for _, finding := range c.findings {
		if finding.Status == EvidenceViolated {
			violations = append(violations, finding)
		}
	}
	if len(violations) == 0 {
		return nil
	}
	return &ValidationError{Findings: violations}
}

func formatFinding(path, message, rule string) string {
	if path == "" {
		return fmt.Sprintf("%s (%s)", message, rule)
	}
	return fmt.Sprintf("%s: %s (%s)", path, message, rule)
}
