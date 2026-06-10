package openbindings

// Canonical invocation error codes. Wire values are SCREAMING_SNAKE with an
// ERR_ prefix, plus the un-prefixed negotiation signal CONTEXT_REQUIRED,
// matching the openbindings.binding-invoker role. The TypeScript SDK emits
// the same values; consumers switching on Code are portable across both.
//
// The lifecycle codes are produced by the SDK's invocation machinery; the
// operational codes are SDK conventions for format invokers. Third-party
// invokers MAY use additional codes.
const (
	// -----------------------------------------------------------------------
	// Lifecycle and protocol codes (produced by the invocation machinery)
	// -----------------------------------------------------------------------

	// ErrCodeCancelled indicates the invocation was cancelled (caller Cancel(),
	// an abandoned output stream, or upstream context cancellation).
	ErrCodeCancelled = "ERR_CANCELLED"

	// ErrCodeAlreadyConsumed indicates the output sequence was acquired a
	// second time (single-consumer, acquire-once), or a second concurrent
	// input reader appeared.
	ErrCodeAlreadyConsumed = "ERR_ALREADY_CONSUMED"

	// ErrCodeExpectedSingle indicates Single observed zero outputs, or
	// short-circuited on a second output.
	ErrCodeExpectedSingle = "ERR_EXPECTED_SINGLE"

	// ErrCodeInputClosed indicates a write after the input side closed
	// (caller Close() or binding CloseInput()). Non-terminal.
	ErrCodeInputClosed = "ERR_INPUT_CLOSED"

	// ErrCodeInvocationClosed indicates the invocation already terminated
	// (closed, errored, or cancelled).
	ErrCodeInvocationClosed = "ERR_INVOCATION_CLOSED"

	// ErrCodeTooManyInputs indicates a binding that accepts a bounded number
	// of inputs received more. Terminal.
	ErrCodeTooManyInputs = "ERR_TOO_MANY_INPUTS"

	// ErrCodeMissingInput indicates a required input message never arrived
	// before the input side closed.
	ErrCodeMissingInput = "ERR_MISSING_INPUT"

	// ErrCodeProtocol indicates a frame-protocol violation (binding-invoker
	// role wire protocol).
	ErrCodeProtocol = "ERR_PROTOCOL"

	// ErrCodeTransportClosed indicates the transport closed without a
	// terminal frame (binding-invoker role wire protocol).
	ErrCodeTransportClosed = "ERR_TRANSPORT_CLOSED"

	// ErrCodeContextRequired indicates missing runtime context (credentials,
	// configuration). Raised by a binding BEFORE any observable side effect;
	// Details carries a ContextRequiredDetails. Un-prefixed: it is a
	// negotiation signal, not a failure of the operation.
	ErrCodeContextRequired = "CONTEXT_REQUIRED"

	// -----------------------------------------------------------------------
	// Local wiring codes (operation-layer resolution failures; never cross
	// the wire from a binding)
	// -----------------------------------------------------------------------

	// ErrCodeOperationNotFound indicates the requested operation matches no
	// key or alias on the interface.
	ErrCodeOperationNotFound = "ERR_OPERATION_NOT_FOUND"

	// ErrCodeBindingNotFound indicates the requested binding is not defined
	// on the interface, no binding matches the operation, or no invoker
	// handles the selected source format.
	ErrCodeBindingNotFound = "ERR_BINDING_NOT_FOUND"

	// ErrCodeUnknownSource indicates a binding references a source not
	// present in the interface.
	ErrCodeUnknownSource = "ERR_UNKNOWN_SOURCE"

	// -----------------------------------------------------------------------
	// Operational codes (format-invoker conventions)
	// -----------------------------------------------------------------------

	// ErrCodeAuthRequired indicates the service rejected the provided
	// credentials (e.g., HTTP 401, gRPC Unauthenticated).
	ErrCodeAuthRequired = "ERR_AUTH_REQUIRED"

	// ErrCodePermissionDenied indicates the caller is authenticated
	// but not authorized (e.g., HTTP 403).
	ErrCodePermissionDenied = "ERR_PERMISSION_DENIED"

	// ErrCodeInvalidRef indicates the ref is malformed or can't be parsed.
	ErrCodeInvalidRef = "ERR_INVALID_REF"

	// ErrCodeRefNotFound indicates the ref is syntactically valid but
	// doesn't resolve to anything in the source.
	ErrCodeRefNotFound = "ERR_REF_NOT_FOUND"

	// ErrCodeSourceLoadFailed indicates the binding source couldn't be loaded or parsed.
	ErrCodeSourceLoadFailed = "ERR_SOURCE_LOAD_FAILED"

	// ErrCodeSourceConfigError indicates the source loaded but is missing
	// required configuration (e.g., no server URL, no binary name).
	ErrCodeSourceConfigError = "ERR_SOURCE_CONFIG_ERROR"

	// ErrCodeConnectFailed indicates a connection to the service couldn't be established.
	ErrCodeConnectFailed = "ERR_CONNECT_FAILED"

	// ErrCodeExecutionFailed indicates the call was made but the service returned an error.
	ErrCodeExecutionFailed = "ERR_EXECUTION_FAILED"

	// ErrCodeResponseError indicates a response was received but couldn't be processed
	// (e.g., marshal failure, response too large).
	ErrCodeResponseError = "ERR_RESPONSE_ERROR"

	// ErrCodeStreamError indicates an error during streaming after the initial connection.
	ErrCodeStreamError = "ERR_STREAM_ERROR"

	// ErrCodeTimeout indicates the operation timed out.
	ErrCodeTimeout = "ERR_TIMEOUT"

	// ErrCodeTransformError indicates a transform evaluation failed.
	ErrCodeTransformError = "ERR_TRANSFORM_ERROR"

	// ErrCodeValidationFailed indicates input (OBI-T-07) or output (OBI-T-08)
	// schema validation failed, or graph validation failed before execution.
	ErrCodeValidationFailed = "ERR_VALIDATION_FAILED"

	// ErrCodeRuntime indicates a generic runtime failure inside a binding
	// implementation.
	ErrCodeRuntime = "ERR_RUNTIME"

	// -----------------------------------------------------------------------
	// Operation-graph codes
	// -----------------------------------------------------------------------

	// ErrCodeEventLimitExceeded indicates that the operation graph exceeded
	// the maximum number of events permitted per execution.
	ErrCodeEventLimitExceeded = "ERR_EVENT_LIMIT_EXCEEDED"

	// ErrCodeOperationGraphExit indicates that an exit node terminated the
	// graph execution with an error.
	ErrCodeOperationGraphExit = "ERR_OPERATION_GRAPH_EXIT"

	// ErrCodeMapNotArray indicates that a map node's transform did not
	// produce an array value as required.
	ErrCodeMapNotArray = "ERR_MAP_NOT_ARRAY"
)
