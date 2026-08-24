package invoke

// Invocation error-code constants used by the SDK. Wire values are SCREAMING_SNAKE with an
// ERR_ prefix, plus the un-prefixed negotiation signal CONTEXT_REQUIRED,
// matching the openbindings.binding-invoker interface where that interface
// defines them. Only interface-owned lifecycle and negotiation codes have
// cross-implementation portability by definition. The remaining codes are
// open SDK conventions, not an exhaustive cross-protocol failure vocabulary;
// binding specifications and third-party implementations may define others.
// (One idiom split: local wiring failures — unknown operation/binding/source
// — surface as pre-errored handles in Go but throw typed errors in TypeScript;
// the values exist in both SDKs for documentation.)
//
// The lifecycle codes are produced by the SDK's invocation machinery. Other
// constants document implementation outcomes used by this SDK; exporting them
// does not give them cross-binding semantics or make them a portable failure
// taxonomy. Third-party invokers MAY use additional codes.
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

	// ErrCodeFrameProtocol indicates an OpenBindings invocation frame-protocol violation.
	ErrCodeFrameProtocol = "ERR_FRAME_PROTOCOL"

	// ErrCodeProtocol is available to binding specifications or implementations
	// that choose this native-protocol identifier; it is not the frame code.
	ErrCodeProtocol = "ERR_PROTOCOL"

	// ErrCodeTransportClosed indicates the transport closed without a
	// terminal frame (the binding-invoker contract's wire protocol).
	ErrCodeTransportClosed = "ERR_TRANSPORT_CLOSED"

	// ErrCodeContextRequired indicates missing runtime context (credentials,
	// configuration). Raised by a binding BEFORE any observable side effect;
	// Details carries a ContextRequiredDetails. Un-prefixed: it is a
	// negotiation signal, not a failure of the operation.
	ErrCodeContextRequired = "CONTEXT_REQUIRED"
	// ErrCodeRefused carries the binding-invoker contract's never-dispatched
	// guarantee: the invocation was refused before dispatch and no
	// observable interaction side effect occurred.
	ErrCodeRefused = "ERR_REFUSED"

	// ErrCodeTypeMismatch indicates a typed-invoker boundary failure: a value
	// could not be decoded into (or encoded from) the generated concrete type.
	// Produced only by TypedInvocation (the codegen boundary).
	ErrCodeTypeMismatch = "ERR_TYPE_MISMATCH"

	// -----------------------------------------------------------------------
	// Operation-invoker-owned resolution codes. They are portable when
	// produced by that interface and do not originate from a binding merely
	// as protocol evidence.
	// -----------------------------------------------------------------------

	// ErrCodeOperationNotFound indicates the requested operation matches no
	// key or alias on the interface.
	ErrCodeOperationNotFound = "ERR_OPERATION_NOT_FOUND"

	// ErrCodeBindingNotFound indicates the requested binding is not defined
	// on the interface, no binding matches the operation, or no invoker
	// handles the selected source format.
	ErrCodeBindingNotFound = "ERR_BINDING_NOT_FOUND"

	// ErrCodeBindingSelectionRequired indicates that several invocable
	// bindings remain and the caller must choose one explicitly.
	ErrCodeBindingSelectionRequired = "ERR_BINDING_SELECTION_REQUIRED"

	// ErrCodeUnknownSource indicates a binding references a source not
	// present in the interface.
	ErrCodeUnknownSource = "ERR_UNKNOWN_SOURCE"

	// -----------------------------------------------------------------------
	// SDK implementation codes (not a cross-binding taxonomy)
	// -----------------------------------------------------------------------

	// ErrCodeInvalidSelector indicates the selector is malformed or can't
	// be parsed.
	ErrCodeInvalidSelector = "ERR_INVALID_SELECTOR"

	// ErrCodeSelectorNotFound indicates the selector is syntactically valid
	// but doesn't resolve to anything in the source.
	ErrCodeSelectorNotFound = "ERR_SELECTOR_NOT_FOUND"

	// ErrCodeSourceLoadFailed indicates the binding source couldn't be loaded or parsed.
	ErrCodeSourceLoadFailed = "ERR_SOURCE_LOAD_FAILED"

	// ErrCodeSourceConfigError indicates the source loaded but is missing
	// required configuration (e.g., no server URL, no binary name).
	ErrCodeSourceConfigError = "ERR_SOURCE_CONFIG_ERROR"

	// ErrCodeConnectFailed indicates a connection to the service couldn't be established.
	ErrCodeConnectFailed = "ERR_CONNECT_FAILED"

	// ErrCodeExecutionFailed indicates only that the governed interaction
	// completed unsuccessfully. It carries no protocol category, blame,
	// retryability, side-effect, authentication, authorization, or availability
	// semantics.
	ErrCodeExecutionFailed = "ERR_EXECUTION_FAILED"

	// ErrCodeResponseError indicates a response was received but couldn't be processed
	// (e.g., marshal failure, response too large).
	ErrCodeResponseError = "ERR_RESPONSE_ERROR"

	// ErrCodeStreamError indicates an error during streaming after the initial connection.
	ErrCodeStreamError = "ERR_STREAM_ERROR"

	// ErrCodeTimeout is an open extension identifier retained for binding- or
	// runtime-specific timeout rules. Caller-owned invocation deadlines use
	// ErrCodeCancelled at the abstract interface boundary.
	ErrCodeTimeout = "ERR_TIMEOUT"

	// -----------------------------------------------------------------------
	// Remaining operation-invoker-owned mechanics
	// -----------------------------------------------------------------------

	// ErrCodeTransformError indicates an operation input or output transform failed.
	ErrCodeTransformError = "ERR_TRANSFORM_ERROR"

	// ErrCodeOperationValidationFailed indicates operation-layer validation
	// failed, including a complete governing OBI schema claim.
	ErrCodeOperationValidationFailed = "ERR_OPERATION_VALIDATION_FAILED"

	// ErrCodeValidationFailed is an open extension identifier retained for
	// binding-specific validation rules.
	ErrCodeValidationFailed = "ERR_VALIDATION_FAILED"

	// ErrCodeSchemaUnresolved indicates the governing schema graph could not
	// be established (an unresolvable $ref, or a schema that will not
	// compile), so the validation claim could not be EVALUATED at all —
	// reported distinctly from a value mismatch and never papered over with
	// partial validation (OBI-T-16; the openbindings.operation-invoker
	// contract owns this code).
	ErrCodeSchemaUnresolved = "ERR_SCHEMA_UNRESOLVED"

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
	// graph execution with an error. Details carries the event that reached
	// the exit node (the spec's error detail).
	ErrCodeOperationGraphExit = "ERR_OPERATION_GRAPH_EXIT"

	// ErrCodeUnsupportedFormatVersion indicates a binding source declares a
	// format version the invoker refuses (e.g. the operation-graph OG-T-02
	// rule mirroring OBI-T-04: higher major, or higher minor while pre-1.0).
	ErrCodeUnsupportedFormatVersion = "ERR_UNSUPPORTED_FORMAT_VERSION"

	// Per-node failure identifiers inside an operation graph (TIMEOUT_EXCEEDED,
	// WRITE_REJECTED, MAP_NOT_ARRAY, TRANSFORM_UNDEFINED) are defined by the
	// operation-graph format specification and live in the
	// formats/operationgraph package, not here: they are format error
	// identifiers, not invoker codes.
)
