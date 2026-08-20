package asyncapi

import (
	"context"
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// InspectSource returns all bindable targets (operation IDs) from an AsyncAPI document.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*synthesize.SourceInspection, error) {
	// Authoring convenience: a bare filesystem path loads as its file://
	// spelling (the strict loader refuses bare paths, ASYNC-D-02).
	loadLocation, err := absolutizeArtifactLocation(source.Location)
	if err != nil {
		return nil, err
	}
	doc, err := loadDocument(ctx, c.httpClient, loadLocation, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load AsyncAPI document: %w", err)
	}

	var targets []synthesize.BindableTarget

	bindingSpec := source.BindingSpec
	if bindingSpec != BindingSpec {
		bindingSpec = BindingSpec
	}
	opIDs := bindableOperationIDs(doc, bindingSpec)

	// Suggest the same operation key SynthesizeInterface assigns (synthesize.go: same
	// sorted iteration and SanitizeKey + UniqueKey de-duplication), so an
	// inspection previews exactly what synthesis names.
	usedKeys := map[string]bool{}
	for _, opID := range opIDs {
		asyncOp := doc.Operations[opID]
		ref := operationRef(opID)
		opKey := synthesize.UniqueKey(synthesize.SanitizeKey(opID), usedKeys)
		usedKeys[opKey] = true
		desc := operationDescription(asyncOp)

		targets = append(targets, bindableTarget(ref, opKey, desc))
	}

	return &synthesize.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func bindableTarget(ref, operationKey, description string) synthesize.BindableTarget {
	target := synthesize.BindableTarget{Ref: ref, OperationKey: operationKey}
	if description != "" {
		target.Operation = &openbindings.Operation{Description: description}
	}
	return target
}
