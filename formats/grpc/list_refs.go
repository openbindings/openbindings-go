package grpc

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets (package.Service/Method) from a
// gRPC source. Supports both proto file parsing and live server reflection.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	var disc *discovery
	var err error

	if source.Content != nil || isProtoFile(source.Location) {
		disc, err = discoverFromProto(ctx, source.Location, source.Content)
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
		return string(disc.services[i].FullName()) < string(disc.services[j].FullName())
	})

	// Suggest the same operation key SynthesizeInterface assigns (create.go: same
	// SanitizeKey + collision resolution against the service name), so an
	// inspection previews exactly what create names.
	usedKeys := map[string]string{}
	for _, svc := range disc.services {
		methods := serviceMethodsSorted(svc)
		for _, method := range methods {
			if method.IsStreamingClient() {
				continue
			}
			fqn := string(svc.FullName()) + "/" + string(method.Name())
			opKey := openbindings.ResolveKeyCollision(openbindings.SanitizeKey(string(method.Name())), string(svc.Name()), usedKeys)
			usedKeys[opKey] = fqn
			desc := commentToDescription(method)
			targets = append(targets, bindableTarget(fqn, opKey, desc))
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
