package openbindings

import (
	"errors"
	"strings"
)

// ValidationError lists the document-rule violations validation established,
// in the order the report's Findings hold them (ValidationReport.Findings),
// each a Finding that names its rule and locates it. Document.Validate and ValidateDocument return it beside their
// report, and ParseDocument returns it for violations of OBI-01 and the
// document schema.
type ValidationError struct {
	Findings []Finding
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Findings) == 0 {
		return "openbindings: non-conformant document"
	}
	lines := make([]string, len(e.Findings))
	for i, finding := range e.Findings {
		lines[i] = formatFinding(finding.Path, finding.Message, finding.Rule)
	}
	return "openbindings: non-conformant document: " + strings.Join(lines, "; ")
}

var (
	// ErrInconclusive marks a call that decided nothing: this SDK could not
	// read or interpret its input in full, so the call answers neither way.
	// It is not a conformance conclusion (§10.4): a document ParseDocument
	// reads no further may conform or not, and ValidateDocument reports the
	// rules it can decide about it. Nor is it a value verdict: a value
	// contract's no-verdicts are a *NoVerdictError (ErrNoVerdict), which no
	// error matching ErrInconclusive is.
	//
	// It is wrapped by ParseDocument's errors other than a
	// *VersionRefusalError and a *ValidationError; by
	// ValueContractCompiler.Resolve's and Document.References' errors for a
	// document beyond this SDK's own limits, nesting deeper than encoding/json
	// reads or holding a string escaping a lone UTF-16 surrogate, which the
	// model does not write (Document.Validate states which documents those
	// are); and by Document.References' error for an index it could not
	// complete. A document declaring no valid version is not one of these:
	// its OBI-03 violation is established, so those calls return it as a
	// *ValidationError. Nor is value input that is not one JSON value, which
	// leaves nothing to decide.
	ErrInconclusive = errors.New("openbindings: inconclusive")

	// ErrOperationNotFound is wrapped by the errors returned for a name that
	// resolves to no one operation (OBI-T-06): no operation carries it, or,
	// in a document violating OBI-05, several do.
	ErrOperationNotFound = errors.New("openbindings: no one operation is named")
)
