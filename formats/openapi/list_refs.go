package openapi

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets (path+method combinations) from
// an OpenAPI document. Each ref is a JSON Pointer into the paths object.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	// Authoring convenience: a bare filesystem path loads as its file://
	// spelling (the strict loader refuses bare paths, OAPI-D-02).
	loadLocation, err := absolutizeArtifactLocation(source.Location)
	if err != nil {
		return nil, err
	}
	doc, err := loadDocument(loadLocation, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}

	// Inspection and synthesis share the same realizability filter: an OAS
	// operation whose revision-1 flattened boundary cannot be represented is
	// not advertised as bindable merely because it appears under paths — it
	// is filtered per operation (tolerant mode), never a reason to refuse
	// inspecting the rest of the document.
	iface, err := convertDocToInterface(doc, source.Location, nil, func(unrealizableTarget) {})
	if err != nil {
		return nil, err
	}
	var targets []openbindings.BindableTarget
	for _, binding := range iface.Bindings {
		op := iface.Operations[binding.Operation]
		targets = append(targets, openbindings.BindableTarget{Ref: binding.Ref, OperationKey: binding.Operation, Operation: &op})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Ref < targets[j].Ref })

	return &openbindings.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func bindableTarget(ref, operationKey, description string) openbindings.BindableTarget {
	target := openbindings.BindableTarget{Ref: ref, OperationKey: operationKey}
	if description != "" {
		target.Operation = &openbindings.Operation{Description: description}
	}
	return target
}
