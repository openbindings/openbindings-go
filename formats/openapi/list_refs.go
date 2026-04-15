package openapi

import (
	"context"
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// ListBindableRefs returns all bindable refs (path+method combinations) from
// an OpenAPI document. Each ref is a JSON Pointer into the paths object.
func (c *Creator) ListBindableRefs(ctx context.Context, source *openbindings.Source) (*openbindings.ListRefsResult, error) {
	doc, err := loadDocument(source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}

	var refs []openbindings.BindableRef

	if doc.Paths == nil {
		return &openbindings.ListRefsResult{Refs: refs, Exhaustive: true}, nil
	}

	pathKeys := make([]string, 0, doc.Paths.Len())
	for path := range doc.Paths.Map() {
		pathKeys = append(pathKeys, path)
	}
	sort.Strings(pathKeys)

	for _, path := range pathKeys {
		pathItem := doc.Paths.Find(path)
		if pathItem == nil {
			continue
		}
		for _, method := range httpMethods {
			op := pathItem.GetOperation(strings.ToUpper(method))
			if op == nil {
				continue
			}

			ref := buildJSONPointerRef(path, method)
			desc := operationDescription(op)

			refs = append(refs, openbindings.BindableRef{
				Ref:         ref,
				Description: desc,
			})
		}
	}

	return &openbindings.ListRefsResult{Refs: refs, Exhaustive: true}, nil
}
