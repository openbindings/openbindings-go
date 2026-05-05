package asyncapi

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets (operation IDs) from an AsyncAPI document.
func (c *Creator) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	doc, err := loadDocument(ctx, c.httpClient, source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load AsyncAPI document: %w", err)
	}

	var targets []openbindings.BindableTarget

	opIDs := make([]string, 0, len(doc.Operations))
	for opID := range doc.Operations {
		opIDs = append(opIDs, opID)
	}
	sort.Strings(opIDs)

	for _, opID := range opIDs {
		asyncOp := doc.Operations[opID]
		ref := "#/operations/" + opID
		desc := operationDescription(asyncOp)

		targets = append(targets, bindableTarget(ref, desc))
	}

	return &openbindings.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func bindableTarget(ref, description string) openbindings.BindableTarget {
	target := openbindings.BindableTarget{Ref: ref}
	if description != "" {
		target.Operation = &openbindings.Operation{Description: description}
	}
	return target
}
