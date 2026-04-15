package usage

import (
	"context"
	"fmt"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// ListBindableRefs returns all bindable refs (space-separated command paths)
// from a usage spec.
func (c *Creator) ListBindableRefs(ctx context.Context, source *openbindings.Source) (*openbindings.ListRefsResult, error) {
	spec, err := loadSpec(ctx, source.Location, source.Content)
	if err != nil {
		return nil, fmt.Errorf("load usage spec: %w", err)
	}

	var refs []openbindings.BindableRef

	meta := spec.Meta()

	// Root command (single-command CLIs like grep, curl).
	if rootCmd := rootCommand(spec); rootCmd != nil {
		bin := meta.Bin
		if bin == "" {
			bin = meta.Name
		}
		if bin != "" {
			refs = append(refs, openbindings.BindableRef{
				Ref:         bin,
				Description: meta.About,
			})
		}
	}

	// Subcommands.
	walkWithGlobals(spec, func(path []string, cmd Command, _ []Flag) {
		if len(path) == 0 {
			return
		}
		if cmd.SubcommandRequired {
			return
		}
		refs = append(refs, openbindings.BindableRef{
			Ref:         strings.Join(path, " "),
			Description: cmd.Help,
		})
	})

	sort.Slice(refs, func(i, j int) bool { return refs[i].Ref < refs[j].Ref })
	return &openbindings.ListRefsResult{Refs: refs, Exhaustive: true}, nil
}
