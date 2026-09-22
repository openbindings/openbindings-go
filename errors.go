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

// ErrOperationNotFound is returned when the requested operation does not exist in the OBI.
var ErrOperationNotFound = errors.New("openbindings: operation not found")

// ErrDependencyNotFound is returned when a named dependency is absent or its
// local operation reference cannot be resolved.
var ErrDependencyNotFound = errors.New("openbindings: dependency not found or invalid")
