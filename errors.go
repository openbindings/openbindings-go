package openbindings

import (
	"errors"
	"strings"
)

// ValidationError is a deterministic, multi-problem validation error.
// Returned by Interface.Validate and by ParseDocument for shape-level
// violations.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return "invalid interface"
	}
	return "invalid interface: " + strings.Join(e.Problems, "; ")
}

// ErrOperationNotFound is returned when the requested operation does not exist in the OBI.
var ErrOperationNotFound = errors.New("openbindings: operation not found")
