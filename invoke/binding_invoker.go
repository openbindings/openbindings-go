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
// or BindingPreparer; check via type assertion.
type BindingInvoker interface {
	BindingSpecs() []openbindings.BindingSpecInfo
	CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict
	InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any]
}

// BindingPreparer optionally gets ready for a possible invocation and reports
// known unmet context requirements. The binding chooses useful setup, including
// I/O, and owns reusable state under its ordinary resource and cleanup policy.
// Preparation must not execute the requested operation, consume its input, or
// spend an approval for it. Invocation must work without an earlier call.
//
// A nil result with no error reports no known unmet requirement, not readiness.
// Required preparation failures return an error. Optional acceleration uses the
// binding's normal valid fallback: it must not add an invocation prerequisite.
// Ordinary invocation calls this same hook and stops on a returned error before
// execution. Explicit preparation does not run the application's context resolver.
//
// Calls may repeat or overlap with invocation. Implementations observe ctx
// cooperatively, do not mutate or retain caller-owned mutable context, and keep
// reusable resources independent of this call's cancellation lifetime. Supplied
// values stay stable during the call. Context-scoped and non-durable data must
// not leak into later calls through retained state. No preparation handle,
// background task, future-success guarantee or automatic retry is implied.
type BindingPreparer interface {
	PrepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error)
}

// CompiledBindingInvoker is executable behavior captured for one exact
// SDK-selected binding. It owns no route identity; the SDK supplies fresh
// per-call args containing context and hooks. PrepareBinding has the same
// contract as BindingPreparer; implementations without preparation return nil, nil.
type CompiledBindingInvoker interface {
	InvokeBinding(context.Context, *BindingInvocationArgs) Invocation[any, any]
	PrepareBinding(context.Context, *BindingInvocationArgs) (*ContextRequiredDetails, error)
}

// BindingCompiler is the optional deterministic closure seam. Implementers
// may capture an exact handler/artifact target once so retained routes do not
// repeat registry or address lookup on every invocation.
type BindingCompiler interface {
	CompileBinding(*BindingInvocationArgs) (CompiledBindingInvoker, error)
}
