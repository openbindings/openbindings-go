package synthesize

import (
	"context"
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
)

// CombineSynthesizers returns a single InterfaceSynthesizer that routes to
// the appropriate inner synthesizer by the source's binding-specification
// identifier (exact match). The returned value also implements
// SourceInspector (assert to use), routing InspectSource to inner
// synthesizers that inspect — the counterpart of the TS SDK's separate
// combineSourceInspectors.
func CombineSynthesizers(synthesizers ...InterfaceSynthesizer) InterfaceSynthesizer {
	c := &combinedSynthesizer{}
	for _, cr := range synthesizers {
		c.synthesizers = append(c.synthesizers, cr)
		for _, info := range cr.BindingSpecs() {
			if _, taken := c.bySpec[info.BindingSpec]; taken {
				continue
			}
			if c.bySpec == nil {
				c.bySpec = map[string]InterfaceSynthesizer{}
			}
			c.bySpec[info.BindingSpec] = cr
			c.specs = append(c.specs, info)
		}
	}
	return c
}

var _ SourceInspector = (*combinedSynthesizer)(nil)
var _ CoverageSynthesizer = (*combinedSynthesizer)(nil)

type combinedSynthesizer struct {
	synthesizers []InterfaceSynthesizer
	bySpec       map[string]InterfaceSynthesizer // exact listed identifier -> synthesizer
	specs        []openbindings.BindingSpecInfo
}

func (c *combinedSynthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	cp := make([]openbindings.BindingSpecInfo, len(c.specs))
	copy(cp, c.specs)
	return cp
}

func (c *combinedSynthesizer) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	verdicts := openbindings.CheckBindingSpecs(bindingSpecs, nil)
	unique := make([]string, len(verdicts))
	index := make(map[string]int, len(verdicts))
	for i, verdict := range verdicts {
		unique[i] = verdict.BindingSpec
		index[verdict.BindingSpec] = i
	}
	for _, synthesizer := range c.synthesizers {
		for _, verdict := range synthesizer.CheckBindingSpecs(unique) {
			i, requested := index[verdict.BindingSpec]
			if requested && verdict.Supported {
				verdicts[i].Supported = true
			}
		}
	}
	return verdicts
}

func (c *combinedSynthesizer) SynthesizeInterface(ctx context.Context, in *SynthesizeInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		skeleton, err := SynthesisSkeleton(in)
		return &skeleton, err
	}
	cr := c.findSynthesizer(in.Sources[0].BindingSpec)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSynthesizer, in.Sources[0].BindingSpec)
	}
	return cr.SynthesizeInterface(ctx, in)
}

// SynthesizeInterfaceWithCoverage routes to the selected synthesizer's
// durable coverage surface. It never fabricates exhaustive evidence from a
// strict synthesis result.
func (c *combinedSynthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *SynthesizeInput) (*SynthesizeResult, error) {
	if len(in.Sources) == 0 {
		skeleton, err := SynthesisSkeleton(in)
		if err != nil {
			return nil, err
		}
		return NewSynthesisResult(&skeleton, nil, true)
	}
	cr := c.findSynthesizer(in.Sources[0].BindingSpec)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSynthesizer, in.Sources[0].BindingSpec)
	}
	coverage, ok := cr.(CoverageSynthesizer)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSynthesisCoverageUnsupported, in.Sources[0].BindingSpec)
	}
	return coverage.SynthesizeInterfaceWithCoverage(ctx, in)
}

// InspectSource implements SourceInspector by routing to the underlying
// synthesizer matching the source's binding specification, when it
// implements SourceInspector.
func (c *combinedSynthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*SourceInspection, error) {
	if source == nil {
		return nil, ErrNoSources
	}
	cr := c.findSynthesizer(source.BindingSpec)
	if cr == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSynthesizer, source.BindingSpec)
	}
	inspector, ok := cr.(SourceInspector)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSourceInspectionUnsupported, source.BindingSpec)
	}
	inspection, err := inspector.InspectSource(ctx, source)
	if inspection != nil && inspection.Targets == nil {
		// A source with nothing bindable is a real, honest answer — but the
		// contract declares targets as an array, and a nil slice marshals to
		// JSON null, failing output validation at every wire consumer.
		inspection.Targets = []BindableTarget{}
	}
	return inspection, err
}

func (c *combinedSynthesizer) findSynthesizer(bindingSpec string) InterfaceSynthesizer {
	for _, synthesizer := range c.synthesizers {
		for _, verdict := range synthesizer.CheckBindingSpecs([]string{bindingSpec}) {
			if verdict.BindingSpec == bindingSpec && verdict.Supported {
				return synthesizer
			}
		}
	}
	return nil
}
