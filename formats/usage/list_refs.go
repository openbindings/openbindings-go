package usage

import (
	"context"
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource returns all bindable targets (space-separated command paths)
// from a usage spec.
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	spec, err := loadSpec(ctx, source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load usage spec: %w", err)
	}

	var targets []openbindings.BindableTarget

	meta := spec.Meta()

	// Root command (single-command CLIs like grep, curl). create keys the root
	// operation by the binary name; mirror that here.
	if rootCmd := rootCommand(spec); rootCmd != nil {
		bin := meta.Bin
		if bin == "" {
			bin = meta.Name
		}
		if bin != "" {
			targets = append(targets, bindableTarget(bin, bin, meta.About))
		}
	}

	// Subcommands. Suggest the same operation key SynthesizeInterface assigns
	// (synthesize_interface.go: the dotted command path, or an explicit opKey prop).
	walkWithGlobals(spec, func(path []string, cmd Command, _ []Flag) {
		if len(path) == 0 {
			return
		}
		if cmd.SubcommandRequired {
			return
		}
		opKey := strings.Join(path, ".")
		if override, ok := cmd.Node.Props["opKey"]; ok {
			if s := override.String(); s != "" {
				opKey = s
			}
		}
		targets = append(targets, bindableTarget(strings.Join(path, " "), opKey, cmd.Help))
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
