package openapi

import (
	"context"
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets (path+method combinations) from
// an OpenAPI document. Each ref is a JSON Pointer into the paths object.
func (c *Creator) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	doc, err := loadDocument(source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}

	var targets []openbindings.BindableTarget

	if doc.Paths == nil {
		return &openbindings.SourceInspection{Targets: targets, Exhaustive: true}, nil
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

			targets = append(targets, bindableTarget(ref, desc))
		}
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
