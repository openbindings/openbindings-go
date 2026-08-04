package openbindings

import (
	"context"
	"fmt"
)

// CombineInvokers returns a single BindingInvoker that routes to the
// appropriate inner invoker by the source's binding-specification
// identifier. Identifiers are exact and opaque (core §6): matching is
// string equality, never version-range interpretation. First registration
// wins for a given identifier; order matters.
func CombineInvokers(invokers ...BindingInvoker) BindingInvoker {
	c := &combinedInvoker{}
	for _, iv := range invokers {
		c.add(iv)
	}
	return c
}

// CombineSynthesizers returns a single InterfaceSynthesizer that routes to
// the appropriate inner synthesizer by the source's binding-specification
// identifier (exact match). The returned value also implements
// SourceInspector (assert to use), routing InspectSource to inner
// synthesizers that inspect — the counterpart of the TS SDK's separate
// combineSourceInspectors.
func CombineSynthesizers(synthesizers ...InterfaceSynthesizer) InterfaceSynthesizer {
	c := &combinedSynthesizer{}
	for _, cr := range synthesizers {
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

// ---------------------------------------------------------------------------
// combinedInvoker
// ---------------------------------------------------------------------------

type combinedInvoker struct {
	bySpec map[string]BindingInvoker // exact identifier -> invoker
	specs  []BindingSpecInfo
}

func (c *combinedInvoker) add(iv BindingInvoker) {
	for _, info := range iv.BindingSpecs() {
		if _, taken := c.bySpec[info.BindingSpec]; taken {
			continue // first registration wins
		}
		if c.bySpec == nil {
			c.bySpec = map[string]BindingInvoker{}
		}
		c.bySpec[info.BindingSpec] = iv
		c.specs = append(c.specs, info)
	}
}

func (c *combinedInvoker) BindingSpecs() []BindingSpecInfo {
	cp := make([]BindingSpecInfo, len(c.specs))
	copy(cp, c.specs)
	return cp
}

func (c *combinedInvoker) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	invoker := c.findInvoker(args.Source.BindingSpec)
	if invoker == nil {
		return NewErroredInvocation[any, any](&InvocationError{
			Code:    ErrCodeBindingNotFound,
			Message: fmt.Sprintf("%v: %s", ErrNoInvoker, args.Source.BindingSpec),
		})
	}
	return invoker.InvokeBinding(ctx, args)
}

// prepareBinding routes the side-effect-free preflight to the matching inner
// invoker. An invoker without BindingPreparer simply reports no requirement.
func (c *combinedInvoker) prepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	invoker := c.findInvoker(args.Source.BindingSpec)
	if invoker == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoInvoker, args.Source.BindingSpec)
	}
	if p, ok := invoker.(BindingPreparer); ok {
		return p.PrepareBinding(ctx, args)
	}
	return nil, nil
}

func (c *combinedInvoker) findInvoker(bindingSpec string) BindingInvoker {
	return c.bySpec[bindingSpec]
}

// ---------------------------------------------------------------------------
// combinedSynthesizer
// ---------------------------------------------------------------------------

var _ SourceInspector = (*combinedSynthesizer)(nil)
var _ CoverageSynthesizer = (*combinedSynthesizer)(nil)

type combinedSynthesizer struct {
	bySpec map[string]InterfaceSynthesizer // exact identifier -> synthesizer
	specs  []BindingSpecInfo
}

func (c *combinedSynthesizer) BindingSpecs() []BindingSpecInfo {
	cp := make([]BindingSpecInfo, len(c.specs))
	copy(cp, c.specs)
	return cp
}

func (c *combinedSynthesizer) SynthesizeInterface(ctx context.Context, in *SynthesizeInput) (*Interface, error) {
	if len(in.Sources) == 0 {
		skeleton, err := SynthesisSkeleton(in)
		return &skeleton, err
	}
	cr := c.bySpec[in.Sources[0].BindingSpec]
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
	cr := c.bySpec[in.Sources[0].BindingSpec]
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
func (c *combinedSynthesizer) InspectSource(ctx context.Context, source *Source) (*SourceInspection, error) {
	if source == nil {
		return nil, ErrNoSources
	}
	cr := c.bySpec[source.BindingSpec]
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
