package asyncapi

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// ListBindableRefs returns all bindable refs (operation IDs) from an AsyncAPI document.
func (c *Creator) ListBindableRefs(ctx context.Context, source *openbindings.Source) (*openbindings.ListRefsResult, error) {
	doc, err := loadDocument(ctx, c.httpClient, source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load AsyncAPI document: %w", err)
	}

	var refs []openbindings.BindableRef

	opIDs := make([]string, 0, len(doc.Operations))
	for opID := range doc.Operations {
		opIDs = append(opIDs, opID)
	}
	sort.Strings(opIDs)

	for _, opID := range opIDs {
		asyncOp := doc.Operations[opID]
		ref := "#/operations/" + opID
		desc := operationDescription(asyncOp)

		refs = append(refs, openbindings.BindableRef{
			Ref:         ref,
			Description: desc,
		})
	}

	return &openbindings.ListRefsResult{Refs: refs, Exhaustive: true}, nil
}
