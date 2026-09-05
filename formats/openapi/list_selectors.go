package openapi

import (
	"context"
	"errors"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// InspectSource returns all bindable targets (path+method combinations) from
// an OpenAPI document. Each selector is a JSON Pointer into the paths object.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*synthesize.SourceInspection, error) {
	if source == nil {
		return nil, errors.New("OpenAPI source is required")
	}
	if source.BindingSpec == BindingSpecOpenAPI20 {
		return c.inspectSwagger20Source(ctx, source)
	}
	observed, err := c.synthesizeProviderProjection(ctx, &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{
		BindingSpec: source.BindingSpec,
		Location:    source.Location,
		Content:     source.Content,
		Description: source.Description,
	}}}, true)
	if err != nil {
		return nil, err
	}
	if failure := firstProviderFailure(observed.failures); failure != nil && failure.SourceRef == "#" {
		return nil, errors.New(failure.Message)
	}
	var targets []synthesize.BindableTarget
	for _, binding := range observed.iface.Bindings {
		op := observed.iface.Operations[binding.Operation]
		targets = append(targets, synthesize.BindableTarget{Selector: binding.Selector, OperationKey: binding.Operation, Operation: &op})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Selector < targets[j].Selector })

	return &synthesize.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func (c *Synthesizer) inspectSwagger20Source(ctx context.Context, source *openbindings.Source) (*synthesize.SourceInspection, error) {
	in := &synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{
		BindingSpec: source.BindingSpec,
		Location:    source.Location,
		Content:     source.Content,
	}}}
	iface, _, _, err := c.synthesizeSwagger20(ctx, in, true)
	if err != nil {
		return nil, err
	}
	var targets []synthesize.BindableTarget
	for _, binding := range iface.Bindings {
		operation := iface.Operations[binding.Operation]
		targets = append(targets, synthesize.BindableTarget{
			Selector: binding.Selector, OperationKey: binding.Operation, Operation: &operation,
		})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Selector < targets[j].Selector })
	return &synthesize.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func bindableTarget(selector, operationKey, description string) synthesize.BindableTarget {
	target := synthesize.BindableTarget{Selector: selector, OperationKey: operationKey}
	if description != "" {
		target.Operation = &openbindings.Operation{Description: description}
	}
	return target
}
