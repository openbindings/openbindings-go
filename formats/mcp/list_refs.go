package mcp

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// ListBindableRefs connects to an MCP server and returns all bindable refs
// (tools, resources, resource templates, and prompts).
func (c *Creator) ListBindableRefs(ctx context.Context, source *openbindings.Source) (*openbindings.ListRefsResult, error) {
	if source.Location == "" {
		return nil, fmt.Errorf("MCP source requires a location (server URL)")
	}

	disc, err := discover(ctx, c.clientVersion, source.Location)
	if err != nil {
		return nil, fmt.Errorf("MCP discovery: %w", err)
	}

	var refs []openbindings.BindableRef

	sort.Slice(disc.Tools, func(i, j int) bool { return disc.Tools[i].Name < disc.Tools[j].Name })
	for _, tool := range disc.Tools {
		desc := tool.Description
		if desc == "" {
			desc = tool.Title
		}
		refs = append(refs, openbindings.BindableRef{
			Ref:         refPrefixTools + tool.Name,
			Description: desc,
		})
	}

	sort.Slice(disc.Resources, func(i, j int) bool { return disc.Resources[i].Name < disc.Resources[j].Name })
	for _, resource := range disc.Resources {
		desc := resource.Description
		if desc == "" {
			desc = resource.Title
		}
		refs = append(refs, openbindings.BindableRef{
			Ref:         refPrefixResources + resource.URI,
			Description: desc,
		})
	}

	sort.Slice(disc.ResourceTemplates, func(i, j int) bool { return disc.ResourceTemplates[i].Name < disc.ResourceTemplates[j].Name })
	for _, tmpl := range disc.ResourceTemplates {
		desc := tmpl.Description
		if desc == "" {
			desc = tmpl.Title
		}
		refs = append(refs, openbindings.BindableRef{
			Ref:         refPrefixResources + tmpl.URITemplate,
			Description: desc,
		})
	}

	sort.Slice(disc.Prompts, func(i, j int) bool { return disc.Prompts[i].Name < disc.Prompts[j].Name })
	for _, prompt := range disc.Prompts {
		desc := prompt.Description
		if desc == "" {
			desc = prompt.Title
		}
		refs = append(refs, openbindings.BindableRef{
			Ref:         refPrefixPrompts + prompt.Name,
			Description: desc,
		})
	}

	return &openbindings.ListRefsResult{Refs: refs, Exhaustive: true}, nil
}
