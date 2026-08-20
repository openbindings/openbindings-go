package usage

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// InspectSource returns all bindable targets from a bare usage source:
// command-path refs (the format's own grammar), one per bindable command,
// exactly as synthesis would bind them.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*synthesize.SourceInspection, error) {
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
		return &synthesize.SourceInspection{Exhaustive: true}, nil
	}

	var targets []synthesize.BindableTarget
	usedOperationKeys := map[string]bool{}
	rootKey := synthesize.UniqueKey(synthesize.SanitizeKey(meta.Bin), usedOperationKeys)
	usedOperationKeys[rootKey] = true
	rc := rootCommand(spec)
	if rc == nil {
		rc = &Command{}
	}
	if _, err := generateInputSchema(*rc, nil); err == nil {
		targets = append(targets, bindableTarget("", rootKey, meta.About))
	}
	walkWithGlobals(spec, func(path []string, cmd Command, inherited []Flag) {
		if len(path) == 0 {
			return
		}
		opKey := synthesize.UniqueKey(synthesize.SanitizeKey(operationName(path)), usedOperationKeys)
		usedOperationKeys[opKey] = true
		if cmd.SubcommandRequired {
			return
		}
		if _, err := generateInputSchema(cmd, inherited); err != nil {
			return
		}
		for _, ref := range uniquelyResolvableCommandRefs(spec, path) {
			targets = append(targets, bindableTarget(ref, opKey, cmd.Help))
		}
	})

	sort.Slice(targets, func(i, j int) bool { return targets[i].Ref < targets[j].Ref })
	return &synthesize.SourceInspection{Targets: targets, Exhaustive: true}, nil
}

func bindableTarget(ref, operationKey, description string) synthesize.BindableTarget {
	target := synthesize.BindableTarget{Ref: ref, OperationKey: operationKey}
	if description != "" {
		target.Operation = &openbindings.Operation{Description: description}
	}
	return target
}
