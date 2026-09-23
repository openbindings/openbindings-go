package openbindings

import (
	"context"
	"errors"
)

// TransformEngine is an implementation of the transform language the
// specification pins (§5.5: JSONata 2.1 for OpenBindings 0.2). The SDK
// carries none. An application chooses one and gives the same engine to
// every layer that parses or evaluates transforms, so the expression
// validation accepts is the expression that runs.
type TransformEngine interface {
	// Parse reports whether expression is in the pinned language: nil when
	// it parses, an error wrapping ErrTransformUndecided when the engine
	// could not decide, and otherwise why it does not parse. It decides
	// OBI-D-18, which is syntactic only.
	Parse(expression string) error

	// Evaluate runs expression against input, the evaluation context ($ in
	// JSONata). variables are the named variables the governing binding
	// specification defines for the expression's position, keyed by name
	// without the language's prefix ("threshold" for JSONata's $threshold;
	// JSONata's reference implementation calls them bindings), or nil for
	// none. The environment is closed: the language's standard library and
	// those variables, nothing else (§5.5 clause 5).
	//
	// Success is exactly one JSON value, as generic decoding produces it
	// (§5.5 clause 3). Every other outcome is a transform-evaluation failure
	// (clause 4): an expression that yields no result returns an error
	// wrapping ErrTransformNoResult; a syntax error, a dynamic error, or a
	// result that is not a JSON value returns any other error.
	Evaluate(ctx context.Context, expression string, input any, variables map[string]any) (any, error)
}

// ErrTransformUndecided is wrapped by the error a TransformEngine returns when
// it could not decide: its own limits (size, depth, time) or a capability it
// lacks stopped it, not the expression. From Parse it leaves OBI-D-18
// inconclusive rather than violated, since a limit met is no evidence either
// way (§10.5). From Evaluate it is still a transform-evaluation failure (§5.5
// clause 4), which a caller can tell apart from one the expression caused.
var ErrTransformUndecided = errors.New("openbindings: the transform engine could not decide")

// ErrTransformNoResult is wrapped by the error a TransformEngine returns when
// an expression yields no result (JSONata's undefined): a transform-evaluation
// failure, distinct from a null result (§5.5 clause 4).
var ErrTransformNoResult = errors.New("openbindings: transform yields no result")
