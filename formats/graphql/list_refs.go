package graphql

import (
	"context"
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource introspects a GraphQL endpoint and returns all bindable
// refs (query/mutation/subscription fields).
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	endpoint := source.Location
	if err := validateHTTPLocation(endpoint); err != nil {
		return nil, err
	}

	var schema *introspectionSchema
	var err error
	if source.ContentPresent() {
		schema, err = parseIntrospectionContent(source.Content)
	} else {
		var disc *discovery
		disc, err = discover(ctx, newDefaultHTTPClient(), endpoint, nil)
		if disc != nil {
			schema = disc.schema
		}
	}
	if err != nil {
		return nil, fmt.Errorf("GraphQL introspection: %w", err)
	}

	var targets []openbindings.BindableTarget
	tm := schema.typeMap()

	rootTypes := []struct {
		label    string
		typeName string
	}{
		{"query", schema.rootTypeName("query")},
		{"mutation", schema.rootTypeName("mutation")},
		{"subscription", schema.rootTypeName("subscription")},
	}

	// Suggest the same operation key SynthesizeInterface assigns (synthesize.go: a
	// SanitizeKey'd field name with collision resolution against the root type),
	// so an inspection previews exactly what synthesis names.
	usedKeys := map[string]string{}
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
			ref := rt.label + "/" + f.Name
			opKey := openbindings.ResolveKeyCollision(openbindings.SanitizeKey(f.Name), strings.ToLower(rt.label), usedKeys)
			usedKeys[opKey] = ref
			targets = append(targets, bindableTarget(ref, opKey, f.Description))
		}
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
