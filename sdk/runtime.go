// Package sdk provides an optional protocol-neutral facade over the
// independently usable OpenBindings core, invoke, and synthesize packages.
package sdk

import (
	"context"
	"fmt"
	"net/http"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

// BindingProvider is one cohesive binding implementation registered for
// invocation, synthesis, and source inspection. Each embedded contract remains
// independently usable; this interface adds no binding semantics.
type BindingProvider interface {
	invoke.BindingInvoker
	synthesize.InterfaceSynthesizer
	synthesize.SourceInspector
}

// RuntimeOptions configures an instance-scoped SDK runtime. No binding package
// or global default registry is installed implicitly.
type RuntimeOptions struct {
	Providers            []BindingProvider
	HTTPClient           *http.Client
	BindingSelector      invoke.BindingSelector
	TransformEvaluator   invoke.TransformEvaluator
	ContextResolver      invoke.ContextResolver
	OutputDecoder        invoke.OutputDecoder
	ResultClassifier     invoke.ResultClassifier
	FieldRouter          invoke.FieldRouter
	MaxDeliveryUnitBytes int64
}

// Runtime composes OpenBindings' protocol-neutral contracts without owning
// any artifact, protocol, or binding-specific behavior.
type Runtime struct {
	operationInvoker *invoke.OperationInvoker
	providers        []BindingProvider
	synthesizer      synthesize.InterfaceSynthesizer
	inspector        synthesize.SourceInspector
	httpClient       *http.Client
}

// New constructs a runtime and rejects ambiguous duplicate exact identifiers
// listed by its providers. CheckBindingSpecs remains authoritative for dynamic
// support that a provider does not advertise.
func New(options RuntimeOptions) (*Runtime, error) {
	if err := rejectDuplicateRegistrations(options.Providers); err != nil {
		return nil, err
	}
	invokers := make([]invoke.BindingInvoker, len(options.Providers))
	synthesizers := make([]synthesize.InterfaceSynthesizer, len(options.Providers))
	for index, provider := range options.Providers {
		invokers[index] = provider
		synthesizers[index] = provider
	}
	op := invoke.NewOperationInvoker(invokers...)
	op.BindingSelector = options.BindingSelector
	op.TransformEvaluator = options.TransformEvaluator
	op.ContextResolver = options.ContextResolver
	op.OutputDecoder = options.OutputDecoder
	op.ResultClassifier = options.ResultClassifier
	op.FieldRouter = options.FieldRouter
	op.MaxDeliveryUnitBytes = options.MaxDeliveryUnitBytes
	combined := synthesize.CombineSynthesizers(synthesizers...)
	inspector, ok := combined.(synthesize.SourceInspector)
	if !ok {
		return nil, fmt.Errorf("combined synthesizer does not implement source inspection")
	}
	return &Runtime{
		operationInvoker: op,
		providers:        append([]BindingProvider(nil), options.Providers...),
		synthesizer:      combined,
		inspector:        inspector,
		httpClient:       options.HTTPClient,
	}, nil
}

// OperationInvoker exposes the lower-level operation contract for typed calls
// and advanced composition without creating another registry.
func (r *Runtime) OperationInvoker() *invoke.OperationInvoker {
	return r.operationInvoker
}

// BindingSpecs returns the exact binding specifications registered by this
// runtime's providers.
func (r *Runtime) BindingSpecs() []openbindings.BindingSpecInfo {
	return r.operationInvoker.BindingSpecs()
}

// CheckBindingSpecs authoritatively checks exact binding-specification
// identifiers against the registered providers.
func (r *Runtime) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return r.operationInvoker.CheckBindingSpecs(bindingSpecs)
}

// Resolve obtains an OBI directly, through well-known discovery, or through a
// registered provider's synthesizer.
func (r *Runtime) Resolve(ctx context.Context, target string) (*synthesize.FetchedInterface, error) {
	options := []synthesize.FetchOption{synthesize.WithSynthesizers(r.providersAsSynthesizers()...)}
	if r.httpClient != nil {
		options = append(options, synthesize.WithFetchHTTPClient(r.httpClient))
	}
	return synthesize.FetchInterface(ctx, target, options...)
}

// InspectSource lists bindable targets through the provider selected by the
// source's exact binding-specification identifier.
func (r *Runtime) InspectSource(ctx context.Context, source *openbindings.Source) (*synthesize.SourceInspection, error) {
	return r.inspector.InspectSource(ctx, source)
}

// SynthesizeInterface projects one or more binding sources into an OBI.
func (r *Runtime) SynthesizeInterface(ctx context.Context, input *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	return r.synthesizer.SynthesizeInterface(ctx, input)
}

// SynthesizeInterfaceWithCoverage projects sources and returns the provider's
// durable, exhaustiveness-qualified coverage ledger.
func (r *Runtime) SynthesizeInterfaceWithCoverage(ctx context.Context, input *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	coverage, ok := r.synthesizer.(synthesize.CoverageSynthesizer)
	if !ok {
		return nil, synthesize.ErrSynthesisCoverageUnsupported
	}
	return coverage.SynthesizeInterfaceWithCoverage(ctx, input)
}

// PrepareOperation performs side-effect-free context preflight for one
// operation through the same provider registry used by Invoke.
func (r *Runtime) PrepareOperation(ctx context.Context, iface *openbindings.Interface, operation string, options ...invoke.InvokeOption) (*invoke.ContextRequiredDetails, error) {
	return r.operationInvoker.PrepareOperation(ctx, iface, operation, options...)
}

// Invoke performs a dynamic operation call. Typed callers use invoke.Invoke
// with OperationInvoker() and a generated OperationSignature.
func (r *Runtime) Invoke(ctx context.Context, iface *openbindings.Interface, operation string, options ...invoke.InvokeOption) *invoke.TypedInvocation[any, any] {
	return invoke.Invoke(ctx, r.operationInvoker, iface, invoke.NewOperationSignature[any, any](operation), options...)
}

func (r *Runtime) providersAsSynthesizers() []synthesize.InterfaceSynthesizer {
	result := make([]synthesize.InterfaceSynthesizer, len(r.providers))
	for index, provider := range r.providers {
		result[index] = provider
	}
	return result
}

func rejectDuplicateRegistrations(providers []BindingProvider) error {
	owners := map[string]int{}
	for index, provider := range providers {
		if provider == nil {
			return fmt.Errorf("binding provider %d is nil", index)
		}
		for _, info := range provider.BindingSpecs() {
			if previous, exists := owners[info.BindingSpec]; exists {
				return fmt.Errorf("binding specification %q is registered by providers %d and %d", info.BindingSpec, previous, index)
			}
			owners[info.BindingSpec] = index
		}
	}
	return nil
}
