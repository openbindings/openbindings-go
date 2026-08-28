package openapi

import (
	"context"
	"errors"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// InspectSource returns all bindable targets (path+method combinations) from
// an OpenAPI document. Each selector is a JSON Pointer into the paths object.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*synthesize.SourceInspection, error) {
	if source != nil && source.BindingSpec == BindingSpecOpenAPI20 {
		return c.inspectSwagger20Source(ctx, source)
	}
	// Authoring convenience: a bare filesystem path loads as its file://
	// spelling (the strict loader refuses bare paths, OAPI-D-02).
	loadLocation, err := absolutizeArtifactLocation(source.Location)
	if err != nil {
		return nil, err
	}
	doc, schemaOverlays, entryBytes, err := loadDocumentForSynthesis(ctx, c.resolverClient(), loadLocation, source.Content)
	// Inspection shares synthesis's acceptance floor: a ladder-invalid target
	// is not advertised as bindable, and a whole-source refusal (§3 part 2)
	// refuses inspection the same way it refuses synthesis -- including when
	// the load failed, for the reason stated at the same check in `invoker.go`.
	floor := computeAcceptanceFloorFromBytes(entryBytes)
	if floor != nil && floor.Refusal != "" {
		return nil, errors.New(floor.Refusal)
	}
	if floor != nil && floor.SourceExclusion != "" {
		return nil, errors.New(floor.SourceExclusion)
	}
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	schemaOverlays.setExternalComponents(internalizeExternalRefs(ctx, doc))

	// Inspection and synthesis share the same realizability filter: an OAS
	// operation whose synthesizable operation boundary cannot be represented is
	// not advertised as bindable merely because it appears under paths — it
	// is filtered per operation (tolerant mode), never a reason to refuse
	// inspecting the rest of the document.
	bindingSpec := source.BindingSpec
	if err := checkAcceptedOpenAPIVersionForBindingSpec(doc, bindingSpec); err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	iface, err := convertDocToInterfaceWithOverlay(doc, source.Location, bindingSpec, nil, func(unrealizableTarget) {}, schemaOverlays, floor)
	if err != nil {
		return nil, err
	}
	var targets []synthesize.BindableTarget
	for _, binding := range iface.Bindings {
		op := iface.Operations[binding.Operation]
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
