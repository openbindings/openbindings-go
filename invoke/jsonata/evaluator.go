// Package jsonata adapts the independently usable JSONata runtime to the
// OpenBindings invocation interfaces. Import it explicitly when composing an
// invoker; the generic SDK neither imports it nor selects an evaluator.
package jsonata

import (
	"context"
	"errors"
	"fmt"

	runtime "github.com/openbindings/jsonata/go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

// Options are host work budgets, not expression bindings or precision settings.
// Runtime defaults and sufficient-budget value semantics apply unchanged.
type Options = runtime.Options

// Evaluator translates OpenBindings values/errors; the runtime owns execution,
// cancellation, compilation caching and all resource budgets.
type Evaluator struct{ executor *runtime.Executor }

var _ invoke.TransformEvaluator = (*Evaluator)(nil)
var _ invoke.TransformEvaluatorWithBindings = (*Evaluator)(nil)

func New(options Options) (*Evaluator, error) {
	executor, err := runtime.New(options)
	if err != nil {
		return nil, err
	}
	return &Evaluator{executor: executor}, nil
}

func (e *Evaluator) Evaluate(ctx context.Context, expression string, data any) (any, error) {
	return e.EvaluateWithBindings(ctx, expression, data, nil)
}

// EvaluateWithBindings admits JSON values only. The invoker owns which variable
// names a binding specification permits at each transform position.
func (e *Evaluator) EvaluateWithBindings(ctx context.Context, expression string, data any, bindings map[string]any) (any, error) {
	if e == nil || e.executor == nil {
		return nil, fmt.Errorf("construct JSONata evaluator with New")
	}
	if ctx == nil {
		return nil, fmt.Errorf("JSONata evaluation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	input, err := jsonvalue.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("JSONata input: %w", err)
	}
	var bound []byte
	if len(bindings) > 0 {
		bound, err = jsonvalue.Marshal(bindings)
		if err != nil {
			return nil, fmt.Errorf("JSONata bindings: %w", err)
		}
	}
	output, err := e.executor.Evaluate(ctx, expression, input, bound)
	if errors.Is(err, runtime.ErrUndefined) {
		return nil, invoke.ErrTransformUndefined
	}
	if err != nil {
		return nil, err
	}
	var value any
	if err := jsonvalue.Unmarshal(output, &value); err != nil {
		return nil, fmt.Errorf("JSONata result: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return value, nil
}
