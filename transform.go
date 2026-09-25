package openbindings

import (
	"context"
	"errors"
)

// TransformParser is a parser for the transform language the specification
// pins (§5.5: JSONata 2.1 for OpenBindings 0.2): it decides whether an
// expression is in the language, which is all OBI-D-18 asks and all
// validation takes (§10.2). The SDK carries none.
type TransformParser interface {
	// Parse reports whether expression is in the pinned language: nil when
	// it parses, an error wrapping ErrTransformUndecided when the parser
	// could not decide, and otherwise why it does not parse.
	Parse(expression string) error
}

// TransformEvaluator evaluates transforms under the contract of §5.5
// (OBI-T-10). The SDK carries none. An implementation of the pinned language
// usually provides both this and TransformParser, and an application gives
// the same one to every layer that parses or evaluates transforms, so the
// expression validation accepts is the expression that runs.
type TransformEvaluator interface {
	// Evaluate runs expression against input, the evaluation context ($ in
	// JSONata). bindings are the context bindings the governing binding
	// specification defines for the expression's position (§5.5 clause 5,
	// OBI-B-02 item 4), keyed by name without the language's prefix
	// ("threshold" for JSONata's $threshold), or nil for none. The
	// environment is closed: the language's standard library and those
	// bindings, nothing else.
	//
	// Success is exactly one JSON value (§5.5 clause 3), in the form
	// CompiledSchema.Validate accepts: nil, a bool, a string, a number (a
	// json.Number, a finite float, or an integer type), a []any, or a
	// map[string]any. Every other outcome is a transform-evaluation failure
	// (clause 4): an expression that yields no result returns an error
	// wrapping ErrTransformNoResult; a syntax error, a dynamic error, or a
	// result that is not a JSON value returns any other error.
	Evaluate(ctx context.Context, expression string, input any, bindings map[string]any) (any, error)
}

// ErrTransformUndecided is wrapped by the error a TransformParser or
// TransformEvaluator returns when it could not decide: its own limits (size,
// depth, time) or a capability it lacks stopped it, not the expression. From
// Parse it leaves OBI-D-18 inconclusive rather than violated, since a limit
// met is no evidence either way (§10.5). From Evaluate it is still a
// transform-evaluation failure (§5.5 clause 4), which a caller can tell apart
// from one the expression caused.
var ErrTransformUndecided = errors.New("openbindings: the transform could not be decided")

// ErrTransformNoResult is wrapped by the error a TransformEvaluator returns
// when an expression yields no result (JSONata's undefined): a
// transform-evaluation failure, distinct from a null result (§5.5 clause 4).
var ErrTransformNoResult = errors.New("openbindings: transform yields no result")
