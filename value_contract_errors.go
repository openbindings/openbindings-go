package openbindings

import (
	"errors"
	"strings"
)

var (
	// ErrMismatch marks an instance mismatch: the value fails its value
	// contract (§5.2).
	ErrMismatch = errors.New("openbindings: the value does not satisfy its value contract")
	// ErrNoVerdict marks a validation that reached no verdict: not a
	// rejection of the value.
	ErrNoVerdict = errors.New("openbindings: no verdict")
	// ErrNoValueContract marks a value checked where the operation states no
	// value contract, which a value neither satisfies nor fails (§5.1,
	// §5.2).
	ErrNoValueContract = errors.New("openbindings: the operation states no value contract there")
	// ErrUndefined marks a no-verdict on an undefined result (§5.2): one
	// JSON Schema leaves undefined, one §7.4 names, or one that depends on a
	// keyword value §5.2's provisions make invalid. Whether the value
	// satisfies its contract is then undefined, whoever evaluates it, so no
	// evaluator lifts it; a missing resource or capability, which withholds a
	// verdict only where it is missing, and core's conservative refusals do
	// not carry it.
	ErrUndefined = errors.New("openbindings: the result is undefined")
)

// MismatchError is an established instance mismatch. errors.Is matches it to
// ErrMismatch. An evaluator returns one for a mismatch; core returns its own,
// holding the evaluator's error as Cause.
type MismatchError struct {
	// Problems: at least one, sorted by InstanceLocation then Message, in a
	// MismatchError core returns.
	Problems []SchemaProblem
	// Cause is the evaluator's error, for diagnostics.
	Cause error
	// hidden keeps Cause out of the chain: core sets it when Cause breaks the
	// evaluator's contract in a way core can see (see keepsVocabulary).
	hidden bool
}

// SchemaProblem is one failed constraint of a mismatch.
type SchemaProblem struct {
	// InstanceLocation is an RFC 6901 JSON Pointer into the value, JSON
	// Schema's instance location, that resolves in the value; "" is the whole
	// value.
	InstanceLocation string
	// Message is advisory.
	Message string
}

func (e *MismatchError) Error() string {
	if e == nil || len(e.Problems) == 0 {
		return ErrMismatch.Error()
	}
	lines := make([]string, len(e.Problems))
	for i, problem := range e.Problems {
		if problem.InstanceLocation == "" {
			lines[i] = problem.Message
		} else {
			lines[i] = problem.InstanceLocation + ": " + problem.Message
		}
	}
	return ErrMismatch.Error() + ": " + strings.Join(lines, "; ")
}

// Is reports whether target is ErrMismatch.
func (e *MismatchError) Is(target error) bool { return target == ErrMismatch }

// Unwrap returns Cause, or nil when core keeps Cause out of the chain.
func (e *MismatchError) Unwrap() error {
	if e == nil || e.hidden {
		return nil
	}
	return e.Cause
}

// NoVerdictError reports that no verdict was reached. errors.Is matches it to
// ErrNoVerdict.
type NoVerdictError struct {
	// Location is where a refusal of core's lies, as a URI-reference:
	// "#/schemas/Task" in the OBI document, or a Resource's URI plus a
	// fragment; for an operation with no schema, the position that is absent
	// ("#/operations/tasks.create/input"). It is "" for the evaluator's
	// refusals and for a value core cannot read.
	Location string
	// Cause says why: core's own reason, or the evaluator's error, unchanged.
	Cause error
	// hidden keeps Cause out of the chain, as for MismatchError.
	hidden bool
}

func (e *NoVerdictError) Error() string {
	message := ErrNoVerdict.Error()
	if e == nil {
		return message
	}
	if e.Location != "" {
		message += " at " + e.Location
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

// Is reports whether target is ErrNoVerdict.
func (e *NoVerdictError) Is(target error) bool { return target == ErrNoVerdict }

// coreReason is the cause of one of core's own no-verdicts that a sentinel
// names, such as ErrUndefined. It matches the sentinel, and reads as the
// sentinel's text without the package prefix, which the NoVerdictError
// holding it already writes, followed by the detail, if any.
type coreReason struct {
	sentinel error
	detail   string
}

func (r *coreReason) Error() string {
	text := strings.TrimPrefix(r.sentinel.Error(), "openbindings: ")
	if r.detail != "" {
		text += ": " + r.detail
	}
	return text
}

func (r *coreReason) Unwrap() error { return r.sentinel }

// Unwrap returns Cause, or nil when core keeps Cause out of the chain.
func (e *NoVerdictError) Unwrap() error {
	if e == nil || e.hidden {
		return nil
	}
	return e.Cause
}
