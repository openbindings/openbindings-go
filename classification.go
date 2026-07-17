package openbindings

// This file carries the InvocationError classification model of the
// openbindings.binding-invoker interface: the normative `category` axis every
// error branches on, and the `effects` marker that gates safe retry. Both are
// derived from the error `code` through the single canonical map below, so the
// classification lives in exactly one place and a test pins that map to the
// interface's documented code→category table (the only thing that can drift
// from the contract).

// Category is the normative classification axis every InvocationError carries.
// It is a fixed, closed set for this interface's major version; a consumer that
// meets an unknown value treats it as CategoryPermanent. An application (or the
// operation invoker) branches on the category without knowing which binding
// family or which specific code produced the failure.
type Category string

const (
	// CategoryContext: required context is missing; the resolve-and-retry
	// hinge (CONTEXT_REQUIRED), not a failure.
	CategoryContext Category = "context"
	// CategoryAuth: supplied credentials were rejected or insufficient.
	// Re-credential before retrying; never retry with the same credentials.
	CategoryAuth Category = "auth"
	// CategoryCancelled: the caller ended the invocation; not a failure of
	// the call itself.
	CategoryCancelled Category = "cancelled"
	// CategoryTransient: a lower-layer condition that may clear on its own
	// (transport dropped, connect failed, timed out). Retry only when Effects
	// permits (see codeEffectsDefault).
	CategoryTransient Category = "transient"
	// CategoryService: the target was reached and returned an error of its
	// own. The application decides from the target's signal (in Details).
	CategoryService Category = "service"
	// CategoryValidation: a value did not match the declared schema, or the
	// schema graph could not be resolved. Fix the value or the document.
	CategoryValidation Category = "validation"
	// CategoryProtocol: the frame sequence or the invocation contract was
	// violated. Fix the caller.
	CategoryProtocol Category = "protocol"
	// CategoryPermanent: a terminal failure that will not clear by retrying
	// the same request. Also the fallback for any unmapped code.
	CategoryPermanent Category = "permanent"
)

// Effects is the invoker's honest report of whether the call's side effects
// may have taken hold — the marker that makes "transient ⇒ retry" safe. It is
// meaningful only where retry is in question (the transient and service
// categories). An error that omits it is treated as EffectsPossible: a
// consumer never auto-retries it and never infers EffectsNone from silence.
type Effects string

const (
	// EffectsNone: the call's side effects provably did not take hold (the
	// connection failed before send; the server definitively refused before
	// executing; a pre-dispatch challenge). Safe to retry.
	EffectsNone Effects = "none"
	// EffectsPossible: the call may have taken effect — it was dispatched,
	// then the transport dropped or it timed out before a conclusive
	// response. Not safe to auto-retry a non-idempotent operation.
	EffectsPossible Effects = "possible"
	// EffectsDefinite: the call took effect (a success/partial output was
	// seen, or the server signaled it acted). A retry is a re-invocation.
	EffectsDefinite Effects = "definite"
)

// codeCategory is the ONE canonical code→category map. It mirrors the
// binding-invoker README "Standard error codes" registry table exactly; a
// table test (classification_test.go) pins every registry code to its
// documented category so this map cannot silently drift from the contract.
//
// A code absent here is classified CategoryPermanent by categoryForCode (the
// README's unknown-category fallback direction), but every code THIS SDK
// emits appears here — including the one format-specific promotion,
// TIMEOUT_EXCEEDED, which the operation-graph engine raises as a terminal
// invocation error for a node that blows its budget.
var codeCategory = map[string]Category{
	// Normative codes (named by the contract).
	ErrCodeContextRequired:  CategoryContext,
	ErrCodeProtocol:         CategoryProtocol,
	ErrCodeTransportClosed:  CategoryTransient,
	ErrCodeCancelled:        CategoryCancelled,
	ErrCodeValidationFailed: CategoryValidation,
	ErrCodeBindingNotFound:  CategoryPermanent,

	// Convention codes (recommended spellings for a category member).
	ErrCodeAuthRequired:             CategoryAuth,
	ErrCodePermissionDenied:         CategoryAuth,
	ErrCodeInvalidRef:               CategoryPermanent,
	ErrCodeRefNotFound:              CategoryPermanent,
	ErrCodeSchemaUnresolved:         CategoryValidation,
	ErrCodeSourceLoadFailed:         CategoryPermanent,
	ErrCodeSourceConfigError:        CategoryPermanent,
	ErrCodeConnectFailed:            CategoryTransient,
	ErrCodeExecutionFailed:          CategoryService,
	ErrCodeResponseError:            CategoryService,
	ErrCodeStreamError:              CategoryTransient,
	ErrCodeTimeout:                  CategoryTransient,
	ErrCodeOperationNotFound:        CategoryPermanent,
	ErrCodeUnknownSource:            CategoryPermanent,
	ErrCodeTransformError:           CategoryValidation,
	ErrCodeInputClosed:              CategoryProtocol,
	ErrCodeInvocationClosed:         CategoryProtocol,
	ErrCodeTooManyInputs:            CategoryProtocol,
	ErrCodeMissingInput:             CategoryProtocol,
	ErrCodeAlreadyConsumed:          CategoryProtocol,
	ErrCodeExpectedSingle:           CategoryProtocol,
	ErrCodeTypeMismatch:             CategoryValidation,
	ErrCodeEventLimitExceeded:       CategoryPermanent,
	ErrCodeOperationGraphExit:       CategoryService,
	ErrCodeUnsupportedFormatVersion: CategoryPermanent,
	ErrCodeRuntime:                  CategoryPermanent,

	// Format-specific promotion: the operation-graph per-node budget timeout
	// (operationgraph.TimeoutExceeded) can surface as a terminal invocation
	// error. A timeout is transient, never permanent.
	"TIMEOUT_EXCEEDED": CategoryTransient,
}

// categoryForCode returns the canonical category for a code, defaulting to
// CategoryPermanent for any code not in the map (the interface's
// unknown-category fallback: a consumer that cannot place a code treats it as
// terminal).
func categoryForCode(code string) Category {
	if c, ok := codeCategory[code]; ok {
		return c
	}
	return CategoryPermanent
}

// codeEffectsDefault is the honest side-effect report for the transport-level
// transient codes this SDK emits, consulted only when an emission site did not
// set Effects itself. A connection that failed before send provably did not
// take effect; a timeout, a mid-stream error, or a transport drop all happened
// after dispatch, so the call may have taken hold. This is why format packages
// need no per-site effects wiring for these codes: classify() fills them at the
// one terminal chokepoint. Sites with information the code alone lacks (an HTTP
// 429/503 whose response proves non-execution) set Effects explicitly and
// classify() leaves that in place.
var codeEffectsDefault = map[string]Effects{
	ErrCodeConnectFailed:   EffectsNone,     // never dispatched
	ErrCodeTimeout:         EffectsPossible, // dispatched, no conclusive response
	ErrCodeStreamError:     EffectsPossible, // failed after the stream opened
	ErrCodeTransportClosed: EffectsPossible, // transport dropped after send
	"TIMEOUT_EXCEEDED":     EffectsPossible, // node dispatched, then blew its budget
}

// classify fills Category (from the canonical map) and Effects (from the
// transport-code defaults) when an emission site left them unset. It is
// idempotent — an explicit Category or Effects is never overwritten — so it is
// safe to call at every terminal chokepoint.
func (e *InvocationError) classify() {
	if e == nil {
		return
	}
	if e.Category == "" {
		e.Category = categoryForCode(e.Code)
	}
	if e.Effects == "" {
		if d, ok := codeEffectsDefault[e.Code]; ok {
			e.Effects = d
		}
	}
}

// classifiedError constructs an *InvocationError with Category and Effects
// already resolved from its code — the constructor for the SDK's own
// (non-fired) returned errors, so a caller inspecting .Category on a value
// that never passed through FireError still sees the classification.
func classifiedError(code, message string) *InvocationError {
	e := &InvocationError{Code: code, Message: message}
	e.classify()
	return e
}
