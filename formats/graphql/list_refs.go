package graphql

import (
	"context"
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// ListBindableRefs introspects a GraphQL endpoint and returns all bindable
// refs (Query/Mutation/Subscription fields).
func (c *Creator) ListBindableRefs(ctx context.Context, source *openbindings.Source) (*openbindings.ListRefsResult, error) {
	endpoint := source.Location
	if endpoint == "" {
		return nil, fmt.Errorf("GraphQL source requires a location (endpoint URL)")
	}

	disc, err := discover(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("GraphQL introspection: %w", err)
	}

	var refs []openbindings.BindableRef
	tm := disc.schema.typeMap()

	rootTypes := []struct {
		label    string
		typeName string
	}{
		{"Query", disc.schema.rootTypeName("Query")},
		{"Mutation", disc.schema.rootTypeName("Mutation")},
		{"Subscription", disc.schema.rootTypeName("Subscription")},
	}

	for _, rt := range rootTypes {
		if rt.typeName == "" {
			continue
		}
		t, ok := tm[rt.typeName]
		if !ok {
			continue
		}

		fields := make([]field, len(t.Fields))
		copy(fields, t.Fields)
		sort.Slice(fields, func(i, j int) bool {
			return fields[i].Name < fields[j].Name
		})

		for _, f := range fields {
			if strings.HasPrefix(f.Name, "__") {
				continue
			}
			refs = append(refs, openbindings.BindableRef{
				Ref:         rt.label + "/" + f.Name,
				Description: f.Description,
			})
		}
	}

	return &openbindings.ListRefsResult{Refs: refs, Exhaustive: true}, nil
}
