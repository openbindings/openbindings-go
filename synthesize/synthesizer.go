package synthesize

import (
	"context"

	openbindings "github.com/openbindings/openbindings-go"
)

// InterfaceSynthesizer synthesizes OpenBindings interfaces from sources
// governed by its supported binding specifications.
// Independent of BindingInvoker — an implementation may provide one, the other, or both.
// Synthesizers load sources fresh on every call; parsed-artifact caching belongs
// to invokers (authoring wants freshness).
type InterfaceSynthesizer interface {
	BindingSpecs() []openbindings.BindingSpecInfo
	CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict
	SynthesizeInterface(ctx context.Context, in *SynthesizeInput) (*openbindings.Interface, error)
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
	BindingSpecs() []openbindings.BindingSpecInfo
	InspectSource(ctx context.Context, source *openbindings.Source) (*SourceInspection, error)
}
