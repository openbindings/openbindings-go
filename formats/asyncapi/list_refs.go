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

	// Suggest the same operation key CreateInterface assigns (create.go: same
	// sorted iteration and SanitizeKey + UniqueKey de-duplication), so an
	// inspection previews exactly what create names.
	usedKeys := map[string]bool{}
	for _, opID := range opIDs {
		asyncOp := doc.Operations[opID]
		ref := "#/operations/" + opID
		opKey := openbindings.UniqueKey(openbindings.SanitizeKey(opID), usedKeys)
		usedKeys[opKey] = true
		desc := operationDescription(asyncOp)

		targets = append(targets, bindableTarget(ref, opKey, desc))
	}

	return &openbindings.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func bindableTarget(ref, operationKey, description string) openbindings.BindableTarget {
	target := openbindings.BindableTarget{Ref: ref, OperationKey: operationKey}
	if description != "" {
		target.Operation = &openbindings.Operation{Description: description}
	}
	return target
}
