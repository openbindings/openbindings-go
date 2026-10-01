package openbindings

import (
	"errors"
	"strings"
)

// ValidationError lists, in a deterministic order, the document-rule
// violations validation established, each a Finding that names its rule and
// locates it. Document.Validate and ValidateDocument return it beside their
// report, and ParseDocument returns it for violations of OBI-D-01 and the
// document schema.
type ValidationError struct {
	Findings []Finding
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Findings) == 0 {
		return "non-conformant document"
	}
	lines := make([]string, len(e.Findings))
	for i, finding := range e.Findings {
		lines[i] = formatFinding(finding.Path, finding.Message, finding.Rule)
	}
	return "non-conformant document: " + strings.Join(lines, "; ")
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
	// ValueContractCompiler.Resolve's error for a document declaring no valid
	// version, which it does not interpret; and by ValueContract.Validate's
	// and ValidateJSON's errors for input that is not one JSON value.
	ErrInconclusive = errors.New("openbindings: inconclusive")

	// ErrOperationNotFound is wrapped by the errors returned for a name that
	// resolves to no one operation (OBI-T-07): no operation carries it, or,
	// in a document violating OBI-D-04, several do.
	ErrOperationNotFound = errors.New("openbindings: no one operation is named")
)
