package openbindings

import "errors"

var (
	// ErrNoExecutor is returned when no executor matches the requested format.
	ErrNoExecutor = errors.New("openbindings: no executor for format")

	// ErrNoCreator is returned when no creator matches the requested format.
	ErrNoCreator = errors.New("openbindings: no creator for format")

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

	// ErrContextInsufficient is returned when an executor cannot proceed
	// because required context (credentials, configuration) is missing.
	ErrContextInsufficient = errors.New("openbindings: context insufficient for this binding")

	// ErrResolutionUnavailable is returned when context is insufficient and
	// no platform callbacks are available to resolve it interactively.
	ErrResolutionUnavailable = errors.New("openbindings: interactive context resolution not available")

	// ErrRefListingUnsupported is returned when a creator does not implement RefLister.
	ErrRefListingUnsupported = errors.New("openbindings: creator does not support ref listing")
)
