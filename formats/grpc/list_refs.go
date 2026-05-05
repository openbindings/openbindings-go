package grpc

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets (package.Service/Method) from a
// gRPC source. Supports both proto file parsing and live server reflection.
func (c *Creator) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	var disc *discovery
	var err error

	if source.Content != nil || isProtoFile(source.Location) {
		disc, err = discoverFromProto(source.Location, source.Content)
		if err != nil {
			return nil, fmt.Errorf("gRPC proto parse: %w", err)
		}
	} else {
		addr := source.Location
		if addr == "" {
			return nil, fmt.Errorf("gRPC source requires a location or content")
		}
		disc, err = discover(ctx, addr)
		if err != nil {
			return nil, fmt.Errorf("gRPC reflection: %w", err)
		}
	}

	var targets []openbindings.BindableTarget

	sort.Slice(disc.services, func(i, j int) bool {
		return disc.services[i].GetFullyQualifiedName() < disc.services[j].GetFullyQualifiedName()
	})

	for _, svc := range disc.services {
		methods := svc.GetMethods()
		sort.Slice(methods, func(i, j int) bool {
			return methods[i].GetName() < methods[j].GetName()
		})
		for _, method := range methods {
			if method.IsClientStreaming() {
				continue
			}
			fqn := svc.GetFullyQualifiedName() + "/" + method.GetName()
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
