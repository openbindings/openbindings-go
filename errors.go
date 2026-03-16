package openbindings

import "errors"

var (
	// ErrNoProvider is returned when no provider matches the requested format.
	ErrNoProvider = errors.New("openbindings: no provider for format")

	// ErrOperationNotFound is returned when the requested operation does not exist in the OBI.
	ErrOperationNotFound = errors.New("openbindings: operation not found")

	// ErrBindingNotFound is returned when no binding is available for the requested operation.
	ErrBindingNotFound = errors.New("openbindings: no binding for operation")

	// ErrNilInterface is returned when a nil interface is passed to an operation that requires one.
	ErrNilInterface = errors.New("openbindings: nil interface")

	// ErrUnknownSource is returned when a binding references a source not present in the interface.
	ErrUnknownSource = errors.New("openbindings: unknown source")

	// ErrNoTransformEvaluator is returned when a binding has a transform but no evaluator is configured.
	ErrNoTransformEvaluator = errors.New("openbindings: transform evaluator required but not configured")

	// ErrNoSources is returned when an operation requires sources but none were provided.
	ErrNoSources = errors.New("openbindings: no sources provided")

	// ErrTransformRefNotFound is returned when a transform reference cannot be resolved.
	ErrTransformRefNotFound = errors.New("openbindings: transform reference not found")

	// ErrEmptyTransformExpression is returned when a transform has no expression to evaluate.
	ErrEmptyTransformExpression = errors.New("openbindings: transform expression is empty")
)
