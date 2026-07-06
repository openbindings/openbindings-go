package usage

import (
	"context"
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets from a usage source: unit refs
// (#/units/<name>) for a wrapper document, or the units a bare kdl would
// gain on wrapping (§11 naming) so inspection shows targets as they will be
// bound.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	w, _, _, err := wrapperForSource(ctx, source.Format, source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load usage source: %w", err)
	}
	spec, err := w.loadArtifact()
	if err != nil {
		return nil, err
	}

	var targets []openbindings.BindableTarget
	for name, u := range w.Units {
		description := u.Description
		if description == "" {
			if strings.TrimSpace(u.Command) == "" {
				description = spec.Meta().About
			} else if found, ferr := findCommand(spec, u.Command); ferr == nil {
				description = found.cmd.Help
			}
		}
		targets = append(targets, bindableTarget(UnitRef(name), name, description))
	}

	sort.Slice(targets, func(i, j int) bool { return targets[i].Ref < targets[j].Ref })
	return &openbindings.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func bindableTarget(ref, operationKey, description string) openbindings.BindableTarget {
	target := openbindings.BindableTarget{Ref: ref, OperationKey: operationKey}
	if description != "" {
		target.Operation = &openbindings.Operation{Description: description}
	}
	return target
}
