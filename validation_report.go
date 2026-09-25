package openbindings

import (
	"fmt"
	"sort"
)

// RuleEvidenceStatus records a validator's evidence for one applicable or
// considered OBI-D rule. The statuses mirror §10.5 of the core specification.
type RuleEvidenceStatus string

const (
	EvidenceSatisfied     RuleEvidenceStatus = "satisfied"
	EvidenceViolated      RuleEvidenceStatus = "violated"
	EvidenceInconclusive  RuleEvidenceStatus = "inconclusive"
	EvidenceNotApplicable RuleEvidenceStatus = "not-applicable"
)

// ConformanceConclusion is the portable conclusion a validator may report
// after applying OBI-T-17 to its collected rule evidence.
type ConformanceConclusion string

const (
	ConclusionConformant              ConformanceConclusion = "conformant"
	ConclusionNonConformant           ConformanceConclusion = "non-conformant"
	ConclusionConformanceUndetermined ConformanceConclusion = "conformance-undetermined"
)

// documentRules is every document rule the core specification defines
// (§10.2), in identifier order. OBI-D-13, OBI-D-14, and OBI-D-15 are retired
// (§10.6).
var documentRules = []string{
	"OBI-D-01", "OBI-D-02", "OBI-D-03", "OBI-D-04", "OBI-D-05", "OBI-D-06",
	"OBI-D-07", "OBI-D-08", "OBI-D-09", "OBI-D-10", "OBI-D-11", "OBI-D-12",
	"OBI-D-16", "OBI-D-17", "OBI-D-18", "OBI-D-19",
}

// DocumentRules returns the identifiers of every document rule the core
// specification defines, in identifier order. Every ValidationReport
// Interface.Validate or ValidateDocument returns carries evidence for each of
// them; a version refusal, or a host object that cannot be encoded, returns
// no report.
func DocumentRules() []string {
	return append([]string(nil), documentRules...)
}

// Finding is one located piece of rule evidence: a violation established at a
// document position, or a check this validator could not decide there.
type Finding struct {
	// Rule is the stable rule identifier, such as "OBI-D-08".
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
}

// Diagnostic is advice a rule asks tools to surface without failing the
// document, such as OBI-T-02's notice of an unknown field. A diagnostic never
// affects a ValidationReport's evidence or conclusion.
type Diagnostic struct {
	// Rule is the rule that asks for the diagnostic, such as "OBI-T-02".
	Rule string
	// Path locates the diagnostic as an RFC 6901 JSON Pointer, as
	// Finding.Path does.
	Path    string
	Message string
}

// ValidationReport is a validator's account of one document under the core
// specification's §10.5 vocabulary.
//
// Conclusion is conformant only when every document rule was decided and none
// was violated; any established violation makes it non-conformant; otherwise
// any inconclusive rule makes it conformance-undetermined. Absence of a
// violation is therefore not conformance: a caller reporting a result must use
// Conclusion, and must not present an undetermined result as conformant.
type ValidationReport struct {
	Conclusion ConformanceConclusion
	// Evidence holds one status per rule considered. Reports from
	// Interface.Validate and ValidateDocument carry every document rule; a
	// rule with nothing to govern in the document is vacuously satisfied. A
	// version refusal, or a host object that cannot be encoded, returns no
	// report, whose Evidence is nil.
	Evidence map[string]RuleEvidenceStatus
	// Violated and Inconclusive identify rules by their stable identifiers,
	// in identifier order, as OBI-T-17 requires.
	Violated     []string
	Inconclusive []string
	// Findings locate every established violation and every undecided check,
	// in the order the validator encountered them. A report is as large as
	// what it reports: each finding's Path is as long as its location is
	// deep, so a deeply nested document with a finding at every level makes a
	// report that grows with the square of its depth. Findings are not capped.
	Findings []Finding
	// Diagnostics are advisory and never affect Conclusion.
	Diagnostics []Diagnostic
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

// ConcludeConformance applies OBI-T-17's truth conditions to a complete map of
// rule evidence. The caller supplies every rule applicable to the validation;
// absence is not itself an evidence status. It concludes from exactly the
// evidence given, as the core conformance corpus's OBI-T-17 scenarios do, so
// an empty map concludes conformant: a report from Interface.Validate or
// ValidateDocument always carries every document rule. A violation is decisive even when
// other rules remain inconclusive. In the absence of a violation, any
// inconclusive applicable rule makes the conclusion undetermined; otherwise
// the conclusion is conformant. An unrecognized runtime status is treated
// conservatively as inconclusive rather than allowing malformed evidence to
// produce a conformant conclusion.
//
// A caller holding evidence this SDK cannot produce can amend a report's
// Evidence and conclude again. The returned report carries
// the evidence it concluded from and no findings.
func ConcludeConformance(evidence map[string]RuleEvidenceStatus) ValidationReport {
	report := ValidationReport{Evidence: make(map[string]RuleEvidenceStatus, len(evidence))}
	for rule, status := range evidence {
		report.Evidence[rule] = status
		switch status {
		case EvidenceViolated:
			report.Violated = append(report.Violated, rule)
		case EvidenceInconclusive:
			report.Inconclusive = append(report.Inconclusive, rule)
		case EvidenceSatisfied, EvidenceNotApplicable:
			// Neither contributes to the decisive or incomplete sets.
		default:
			report.Inconclusive = append(report.Inconclusive, rule)
		}
	}
	sort.Strings(report.Violated)
	sort.Strings(report.Inconclusive)
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

// VersionRefusalError reports OBI-T-04's version refusal: the document
// declares a well-formed version outside this SDK's supported set, so it is
// not interpreted under this version's semantics at all. A refusal is not a
// conformance conclusion; validation returns it instead of a report.
type VersionRefusalError struct {
	// Version is the document's declared openbindings value.
	Version string
	Reason  string
}

func (e *VersionRefusalError) Error() string {
	return fmt.Sprintf("openbindings: %s (OBI-T-04)", e.Reason)
}

// ruleChecks collects located evidence while a validator runs. Rules that
// record no finding are satisfied.
type ruleChecks struct {
	findings    []Finding
	diagnostics []Diagnostic
}

func (c *ruleChecks) violated(rule, path, message string) {
	c.findings = append(c.findings, Finding{Rule: rule, Status: EvidenceViolated, Path: path, Message: message})
}

func (c *ruleChecks) inconclusive(rule, path, message string) {
	c.findings = append(c.findings, Finding{Rule: rule, Status: EvidenceInconclusive, Path: path, Message: message})
}

func (c *ruleChecks) diagnose(rule, path, message string) {
	c.diagnostics = append(c.diagnostics, Diagnostic{Rule: rule, Path: path, Message: message})
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

// report concludes over every document rule: violated when any violation was
// recorded for it, inconclusive when only undecided checks were, satisfied
// otherwise.
func (c *ruleChecks) report() ValidationReport {
	evidence := make(map[string]RuleEvidenceStatus, len(documentRules))
	for _, rule := range documentRules {
		evidence[rule] = EvidenceSatisfied
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
	report.Findings = append([]Finding(nil), c.findings...)
	report.Diagnostics = append([]Diagnostic(nil), c.diagnostics...)
	return report
}

// conclude returns the report together with the error validation returns: a
// *ValidationError listing every established violation, or nil when none was
// established. Inconclusive checks and diagnostics are not violations and do
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
