package mcp

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource connects to an MCP server and returns all bindable targets
// (tools, resources, resource templates, and prompts).
func (c *Creator) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	if source.Location == "" {
		return nil, fmt.Errorf("MCP source requires a location (server URL)")
	}

	disc, err := discover(ctx, c.clientVersion, source.Location)
	if err != nil {
		return nil, fmt.Errorf("MCP discovery: %w", err)
	}

	var targets []openbindings.BindableTarget

	sort.Slice(disc.Tools, func(i, j int) bool { return disc.Tools[i].Name < disc.Tools[j].Name })
	for _, tool := range disc.Tools {
		desc := tool.Description
		if desc == "" {
			desc = tool.Title
		}
		targets = append(targets, bindableTarget(refPrefixTools+tool.Name, desc))
	}

	sort.Slice(disc.Resources, func(i, j int) bool { return disc.Resources[i].Name < disc.Resources[j].Name })
	for _, resource := range disc.Resources {
		desc := resource.Description
		if desc == "" {
			desc = resource.Title
		}
		targets = append(targets, bindableTarget(refPrefixResources+resource.URI, desc))
	}

	sort.Slice(disc.ResourceTemplates, func(i, j int) bool { return disc.ResourceTemplates[i].Name < disc.ResourceTemplates[j].Name })
	for _, tmpl := range disc.ResourceTemplates {
		desc := tmpl.Description
		if desc == "" {
			desc = tmpl.Title
		}
		targets = append(targets, bindableTarget(refPrefixResources+tmpl.URITemplate, desc))
	}

	sort.Slice(disc.Prompts, func(i, j int) bool { return disc.Prompts[i].Name < disc.Prompts[j].Name })
	for _, prompt := range disc.Prompts {
		desc := prompt.Description
		if desc == "" {
			desc = prompt.Title
		}
		targets = append(targets, bindableTarget(refPrefixPrompts+prompt.Name, desc))
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
