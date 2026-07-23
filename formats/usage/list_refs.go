package usage

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets from a bare usage source:
// command-path refs (the format's own grammar), one per bindable command,
// exactly as synthesis would bind them.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	location, err := absolutizeArtifactLocation(source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load usage source: %w", err)
	}
	text, err := artifactText(ctx, location, source.Content, c.AuthorizeExec)
	if err != nil {
		return nil, fmt.Errorf("load usage source: %w", err)
	}
	spec, err := ParseKDL([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("parse usage content: %w", err)
	}
	if err := validateAcceptedUsageArtifact(spec); err != nil {
		return nil, err
	}
	meta := spec.Meta()
	if meta.Bin == "" {
		return &openbindings.SourceInspection{Exhaustive: true}, nil
	}

	var targets []openbindings.BindableTarget
	if rc := rootCommand(spec); rc != nil {
		targets = append(targets, bindableTarget("", meta.Bin, meta.About))
	}
	walkWithGlobals(spec, func(path []string, cmd Command, _ []Flag) {
		if len(path) == 0 || cmd.SubcommandRequired {
			return
		}
		targets = append(targets, bindableTarget(commandRef(path), operationName(path), cmd.Help))
	})

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
