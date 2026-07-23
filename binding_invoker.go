package openbindings

import "context"

// BindingInvoker invokes bindings whose sources are governed by specific
// binding specifications (e.g., openbindings.openapi@1, openbindings.mcp@1).
//
// InvokeBinding returns the Invocation handle synchronously; creation is
// inert (no I/O during construction). The binding's work is scheduled on its
// own goroutine and MUST raise CONTEXT_REQUIRED (and any other pre-dispatch
// failure) before any observable side effect. ctx is the invocation's
// lifetime: its cancellation converges with the handle's Cancel().
//
// Wiring failures knowable synchronously (e.g. an unloadable ref) surface as
// an already-errored handle, never as a panic.
//
// A concrete invoker may also implement InterfaceSynthesizer, SourceInspector,
// or BindingPreparer; check via type assertion.
type BindingInvoker interface {
	BindingSpecs() []BindingSpecInfo
	InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any]
}

// BindingPreparer is the optional side-effect-free preflight capability (the
// prepareBinding operation of the openbindings.binding-invoker interface): it
// reports the context the binding would require for this invocation, or nil
// when the binding can proceed. Lets the operation layer resolve context
// BEFORE the caller streams input, collapsing knowable-upfront challenges
// into the clean no-input-consumed case. Implementations MUST NOT perform
// network I/O or any other side effect.
type BindingPreparer interface {
	PrepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error)
}

// InterfaceSynthesizer synthesizes OpenBindings interfaces from sources
// governed by its supported binding specifications.
// Independent of BindingInvoker — an implementation may provide one, the other, or both.
// Synthesizers load sources fresh on every call; parsed-artifact caching belongs
// to invokers (authoring wants freshness).
type InterfaceSynthesizer interface {
	BindingSpecs() []BindingSpecInfo
	SynthesizeInterface(ctx context.Context, in *SynthesizeInput) (*Interface, error)
}

// CoverageSynthesizer extends InterfaceSynthesizer with durable accounting of
// every source interaction unit observed by the same synthesis call. It is a
// separate capability so existing third-party synthesizers remain source
// compatible while reference families adopt the 0.2 coverage contract.
type CoverageSynthesizer interface {
	InterfaceSynthesizer
	SynthesizeInterfaceWithCoverage(ctx context.Context, in *SynthesizeInput) (*SynthesizeResult, error)
}

// SourceInspector inspects sources and returns bindable targets that
// tooling can frame as OpenBindings operations.
type SourceInspector interface {
	BindingSpecs() []BindingSpecInfo
	InspectSource(ctx context.Context, source *Source) (*SourceInspection, error)
}
