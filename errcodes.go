package openbindings

// Standard error codes for binding executor results. These enable
// protocol-agnostic error handling by the operation executor and
// application code. Binding executors SHOULD use these codes in
// ExecuteError.Code.
//
// These are SDK conventions, not spec requirements. Third-party
// executors MAY use different codes.
const (
	// ErrCodeAuthRequired indicates authentication is needed
	// (e.g., HTTP 401, gRPC Unauthenticated). Retryable with credentials.
	ErrCodeAuthRequired = "auth_required"

	// ErrCodePermissionDenied indicates the caller is authenticated
	// but not authorized (e.g., HTTP 403).
	ErrCodePermissionDenied = "permission_denied"

	// ErrCodeInvalidRef indicates the ref is malformed or can't be parsed.
	ErrCodeInvalidRef = "invalid_ref"

	// ErrCodeRefNotFound indicates the ref is syntactically valid but
	// doesn't resolve to anything in the source.
	ErrCodeRefNotFound = "ref_not_found"

	// ErrCodeInvalidInput indicates the input doesn't match the expected schema.
	ErrCodeInvalidInput = "invalid_input"

	// ErrCodeSourceLoadFailed indicates the binding source couldn't be loaded or parsed.
	ErrCodeSourceLoadFailed = "source_load_failed"

	// ErrCodeSourceConfigError indicates the source loaded but is missing
	// required configuration (e.g., no server URL, no binary name).
	ErrCodeSourceConfigError = "source_config_error"

	// ErrCodeConnectFailed indicates a connection to the service couldn't be established.
	ErrCodeConnectFailed = "connect_failed"

	// ErrCodeExecutionFailed indicates the call was made but the service returned an error.
	ErrCodeExecutionFailed = "execution_failed"

	// ErrCodeResponseError indicates a response was received but couldn't be processed
	// (e.g., marshal failure, response too large).
	ErrCodeResponseError = "response_error"

	// ErrCodeStreamError indicates an error during streaming after the initial connection.
	ErrCodeStreamError = "stream_error"

	// ErrCodeTimeout indicates the operation timed out.
	ErrCodeTimeout = "timeout"

	// ErrCodeCancelled indicates the operation was cancelled by the caller.
	ErrCodeCancelled = "cancelled"

	// ErrCodeBindingNotFound indicates the requested binding is not defined on the interface.
	ErrCodeBindingNotFound = "binding_not_found"

	// ErrCodeTransformError indicates a transform evaluation failed.
	ErrCodeTransformError = "transform_error"

	// ErrCodeValidationFailed indicates that graph or input validation failed
	// before execution began.
	ErrCodeValidationFailed = "validation_failed"

	// ErrCodeEventLimitExceeded indicates that the operation graph exceeded
	// the maximum number of events permitted per execution.
	ErrCodeEventLimitExceeded = "event_limit_exceeded"

	// ErrCodeOperationGraphExit indicates that an exit node terminated the
	// graph execution with an error.
	ErrCodeOperationGraphExit = "operation_graph_exit"

	// ErrCodeMapNotArray indicates that a map node's transform did not
	// produce an array value as required.
	ErrCodeMapNotArray = "map_not_array"
)
