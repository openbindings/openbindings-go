package openbindings

import (
	"errors"
	"strings"
)

// ValidationError lists, in a deterministic order, the document-rule
// violations validation established, each a Finding that names its rule and
// locates it. Interface.Validate and ValidateDocument return it beside their
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

// ErrOperationNotFound is wrapped by the errors returned for a name that
// resolves to no one operation (OBI-T-07): no operation carries it, or, in a
// document violating OBI-D-04, several do.
var ErrOperationNotFound = errors.New("openbindings: no one operation is named")
