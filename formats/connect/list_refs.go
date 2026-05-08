package connect

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets (package.Service/Method) from a
// Connect source by parsing the proto definition.
func (c *Creator) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	if source.Location == "" && source.Content == nil {
		return nil, fmt.Errorf("Connect source requires a location or content")
	}

	disc, err := discoverFromProto(ctx, source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("Connect proto parse: %w", err)
	}

	var targets []openbindings.BindableTarget

	sort.Slice(disc.services, func(i, j int) bool {
		return string(disc.services[i].FullName()) < string(disc.services[j].FullName())
	})

	for _, svc := range disc.services {
		methods := serviceMethodsSorted(svc)
		for _, method := range methods {
			if method.IsStreamingClient() {
				continue
			}
			fqn := string(svc.FullName()) + "/" + string(method.Name())
			desc := commentToDescription(method)
			targets = append(targets, bindableTarget(fqn, desc))
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
