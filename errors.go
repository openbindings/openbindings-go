package openbindings

import (
	"errors"
	"strings"
)

// ValidationError lists, in a deterministic order, the document-rule
// violations validation established. Interface.Validate and ValidateDocument
// return it beside their report, and ParseDocument returns it for violations
// of the document schema. Every problem names the rule it violates.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "non-conformant interface"
	}
	return "non-conformant interface: " + strings.Join(e.Problems, "; ")
}

// ErrOperationNotFound is wrapped by the errors returned for a name that
// resolves to no operation (OBI-T-12).
var ErrOperationNotFound = errors.New("openbindings: operation not found")
