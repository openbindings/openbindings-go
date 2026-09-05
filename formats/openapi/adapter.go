package openapi

import (
	"context"
	"net/http"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

// AdapterOptions coherently configures the OpenBindings-facing OpenAPI
// capabilities. Invocation and authoring retrieval remain distinct because
// authoring requires a fresh read while invocation maintains a document cache.
type AdapterOptions struct {
	Invoker             InvokerOptions
	SynthesisHTTPClient *http.Client
}

// Adapter is the complete OpenBindings adapter for the OAS binding family.
// One instance can be registered for invocation, synthesis, and source
// inspection. OpenAPI planning and execution remain in openapi-client.
type Adapter struct {
	invoker     *Invoker
	synthesizer *Synthesizer
}

var (
	_ invoke.BindingInvoker           = (*Adapter)(nil)
	_ invoke.BindingPreparer          = (*Adapter)(nil)
	_ invoke.BuiltinHooksProvider     = (*Adapter)(nil)
	_ synthesize.InterfaceSynthesizer = (*Adapter)(nil)
	_ synthesize.CoverageSynthesizer  = (*Adapter)(nil)
	_ synthesize.SourceInspector      = (*Adapter)(nil)
)

// NewAdapter returns one cohesive OpenAPI registration using default clients.
func NewAdapter() *Adapter {
	return NewAdapterWithOptions(AdapterOptions{})
}

// NewAdapterWithOptions returns one cohesively configured OpenAPI registration
// for the high-level SDK.
func NewAdapterWithOptions(configured AdapterOptions) *Adapter {
	return &Adapter{
		invoker:     NewInvokerWithOptions(configured.Invoker),
		synthesizer: NewSynthesizerWithClient(configured.SynthesisHTTPClient),
	}
}

// BindingSpecs returns the four exact OAS-family binding identifiers.
func (a *Adapter) BindingSpecs() []openbindings.BindingSpecInfo {
	return a.invoker.BindingSpecs()
}

// CheckBindingSpecs authoritatively checks exact identifiers against the OAS
// family implemented by this adapter.
func (a *Adapter) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return a.invoker.CheckBindingSpecs(bindingSpecs)
}

// InvokeBinding translates one Core binding invocation to the native client.
func (a *Adapter) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	return a.invoker.InvokeBinding(ctx, args)
}

// PrepareBinding reports context the selected operation requires without
// dispatching a request.
func (a *Adapter) PrepareBinding(ctx context.Context, args *invoke.BindingInvocationArgs) (*invoke.ContextRequiredDetails, error) {
	return a.invoker.PrepareBinding(ctx, args)
}

// BuiltinHooks returns the adapter's protocol-aware output and result hooks.
func (a *Adapter) BuiltinHooks() (invoke.OutputDecoder, invoke.ResultClassifier) {
	return a.invoker.BuiltinHooks()
}

// SynthesizeInterface projects an OAS artifact into an OBI.
func (a *Adapter) SynthesizeInterface(ctx context.Context, input *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	return a.synthesizer.SynthesizeInterface(ctx, input)
}

// SynthesizeInterfaceWithCoverage projects an OAS artifact and returns its
// exhaustive binding-coverage ledger.
func (a *Adapter) SynthesizeInterfaceWithCoverage(ctx context.Context, input *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	return a.synthesizer.SynthesizeInterfaceWithCoverage(ctx, input)
}

// InspectSource lists bindable OAS targets through the native projection.
func (a *Adapter) InspectSource(ctx context.Context, source *openbindings.Source) (*synthesize.SourceInspection, error) {
	return a.synthesizer.InspectSource(ctx, source)
}
