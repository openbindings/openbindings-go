package openbindings

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// compileWith calls the evaluator's Compile, reading its answer as core does
// (the SchemaEvaluator contract): a usable CompiledSchema, or the error that
// is the value contract's standing no-verdict. A panic, or (nil, nil), is such
// an error too.
func compileWith(ctx context.Context, e SchemaEvaluator, bundle SchemaBundle) (compiled CompiledSchema, err error) {
	defer func() {
		if r := recover(); r != nil {
			compiled, err = nil, fmt.Errorf("the evaluator panicked while compiling: %v", r)
		}
	}()
	compiled, err = e.Compile(ctx, bundle)
	if err == nil && compiled == nil {
		err = errors.New("the evaluator returned no compiled schema and no error")
	}
	if err != nil {
		compiled = nil
	}
	return compiled, err
}

// validateWith calls a compiled schema's Validate and reads its answer, in
// order: a panic, a context error anywhere in the chain, and an error matching
// one of core's refusal sentinels or holding a *NoVerdictError (which the
// contract forbids) give no verdict; an error holding a non-nil
// *MismatchError is a mismatch; nil is valid; anything else gives no verdict.
// value is the JSON value validated, against which a mismatch's problems are
// repaired.
func validateWith(ctx context.Context, compiled CompiledSchema, value any) error {
	answer, panicked := callValidate(ctx, compiled, value)
	if panicked != nil {
		return &NoVerdictError{Cause: panicked}
	}
	if answer == nil {
		return nil
	}
	reading, readPanic := readAnswer(answer)
	switch {
	case readPanic != nil:
		return &NoVerdictError{Cause: readPanic}
	case reading.mismatch == nil:
		return &NoVerdictError{Cause: answer, hidden: !keepsVocabulary(ctx, answer, false)}
	}
	return &MismatchError{
		Problems: repairProblems(value, reading.mismatch.Problems),
		Cause:    answer,
		hidden:   !keepsVocabulary(ctx, answer, true),
	}
}

func callValidate(ctx context.Context, compiled CompiledSchema, value any) (answer, panicked error) {
	defer func() {
		if r := recover(); r != nil {
			answer, panicked = nil, fmt.Errorf("the evaluator panicked while validating: %v", r)
		}
	}()
	return compiled.Validate(ctx, value), nil
}

// answerReading is what core reads from an evaluator's non-nil answer: the
// mismatch it holds, when it is one.
type answerReading struct {
	mismatch *MismatchError
}

// readAnswer classifies an evaluator's error. The error's own Is, As, and
// Unwrap methods run here, so a panic in them gives no verdict.
func readAnswer(answer error) (reading answerReading, panicked error) {
	defer func() {
		if r := recover(); r != nil {
			reading, panicked = answerReading{}, fmt.Errorf("the evaluator's error panicked while core read it: %v", r)
		}
	}()
	if errors.Is(answer, context.Canceled) || errors.Is(answer, context.DeadlineExceeded) {
		return answerReading{}, nil
	}
	if matchesRefusal(answer) {
		return answerReading{}, nil
	}
	var mismatch *MismatchError
	if errors.As(answer, &mismatch) && mismatch != nil {
		return answerReading{mismatch: mismatch}, nil
	}
	return answerReading{}, nil
}

// matchesRefusal reports whether an evaluator's error speaks core's refusal
// vocabulary, which the contract forbids it.
func matchesRefusal(err error) bool {
	for _, sentinel := range []error{ErrNoVerdict, ErrUndefined, ErrNoValueContract, ErrOperationNotFound, ErrInconclusive} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	var refusal *NoVerdictError
	return errors.As(err, &refusal)
}

// keepsVocabulary reports whether a result core returns may unwrap to the
// evaluator's error: whether the error keeps the evaluator's contract as far
// as core can see. It must not speak core's refusal vocabulary, match
// ErrMismatch outside a mismatch, or match a context error other than the
// caller's own, once the caller's ctx is done. Otherwise the error stays in
// Cause alone, so every promise core makes about a returned error's chain
// holds whatever the evaluator does.
func keepsVocabulary(ctx context.Context, err error, mismatch bool) (keeps bool) {
	defer func() {
		if recover() != nil {
			keeps = false
		}
	}()
	if matchesRefusal(err) {
		return false
	}
	if !mismatch && errors.Is(err, ErrMismatch) {
		return false
	}
	done := ctx.Err()
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, contextErr) && (done == nil || !errors.Is(done, contextErr)) {
			return false
		}
	}
	return true
}

// repairProblems copies an evaluator's problems, locating each where it
// resolves in the value (a location that does not resolve becomes its longest
// prefix that does), adding a whole-value problem when there is none, and
// sorting them by location, then message.
func repairProblems(value any, problems []SchemaProblem) []SchemaProblem {
	out := make([]SchemaProblem, 0, max(len(problems), 1))
	for _, problem := range problems {
		out = append(out, SchemaProblem{
			InstanceLocation: jsonpointer.Longest(value, problem.InstanceLocation),
			Message:          problem.Message,
		})
	}
	if len(out) == 0 {
		out = append(out, SchemaProblem{Message: "the value does not satisfy its value contract"})
	}
	slices.SortStableFunc(out, func(a, b SchemaProblem) int {
		return cmp.Or(cmp.Compare(a.InstanceLocation, b.InstanceLocation), cmp.Compare(a.Message, b.Message))
	})
	return out
}
