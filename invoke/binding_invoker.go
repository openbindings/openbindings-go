package invoke

import (
	"context"

	openbindings "github.com/openbindings/openbindings-go"
)

// BindingInvoker invokes bindings whose sources are governed by specific
// binding specifications (e.g., openbindings.openapi-3.1@1, openbindings.mcp@1).
//
// InvokeBinding returns the Invocation handle synchronously; creation is
// inert (no I/O during construction). The binding's work is scheduled on its
// own goroutine and MUST raise CONTEXT_REQUIRED (and any other pre-dispatch
// failure carrying a refusal guarantee) before output or observable effects of
// the requested operation. Description retrieval and connection setup may
// precede that boundary. ctx is the invocation's
// lifetime: its cancellation converges with the handle's Cancel().
//
// Wiring failures knowable synchronously (e.g. an unloadable selector) surface as
// an already-errored handle, never as a panic.
//
// A concrete invoker may also implement InterfaceSynthesizer, SourceInspector,
// or BindingPreflighter; check via type assertion.
type BindingInvoker interface {
	BindingSpecs() []openbindings.BindingSpecInfo
	CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict
	InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any]
}

// BindingPreflighter is optional. PreflightBinding tells a binding that an
// invocation of args' selection may follow and lets it report context
// requirements it can already identify from the source and args.Context:
// a ContextRequiredDetails in the same shape its CONTEXT_REQUIRED challenge
// carries, or nil. The result is advisory: it may omit requirements, nil is
// always conformant, and the live challenge remains authoritative.
// Invocation never requires a prior preflight. args.Context is supplied for
// this call alone. Preflight never dispatches the requested operation,
// consumes its input, emits its outputs, or spends an approval for it; the
// boundary of the requested operation is the governing binding
// specification's, and anything else a binding does in response is that
// specification's to require and otherwise this implementation's,
// documented in its README. Requirements are reported only as the result;
// an error means the binding could not answer and carries no prediction.
type BindingPreflighter interface {
	PreflightBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error)
}

// CompiledBindingInvoker is executable behavior captured for one exact
// SDK-selected binding. It owns no route identity; the SDK supplies fresh
// per-call args containing context and hooks. PreflightBinding has the same
// contract as BindingPreflighter; implementations without preflight return nil, nil.
type CompiledBindingInvoker interface {
	InvokeBinding(context.Context, *BindingInvocationArgs) Invocation[any, any]
	PreflightBinding(context.Context, *BindingInvocationArgs) (*ContextRequiredDetails, error)
}

// BindingCompiler is the optional deterministic closure seam. Implementers
// may capture an exact handler/artifact target once so retained routes do not
// repeat registry or address lookup on every invocation.
type BindingCompiler interface {
	CompileBinding(*BindingInvocationArgs) (CompiledBindingInvoker, error)
}
