package mcp

import (
	"context"
	"fmt"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

// InspectSource connects to an MCP server and returns all bindable targets
// (tools, resources, resource templates, and prompts).
func (c *Synthesizer) InspectSource(ctx context.Context, source *openbindings.Source) (*openbindings.SourceInspection, error) {
	if source.Location == "" {
		return nil, fmt.Errorf("MCP source requires a location (server URL)")
	}

	disc, err := discover(ctx, c.clientVersion, source.Location)
	if err != nil {
		return nil, fmt.Errorf("MCP discovery: %w", err)
	}

	var targets []openbindings.BindableTarget

	// Suggest the same operation keys SynthesizeInterface assigns (synthesize.go: a
	// SanitizeKey'd name with collision resolution against the entity kind,
	// sharing one usedKeys map across tools/resources/templates/prompts), so an
	// inspection previews exactly what synthesis names.
	usedKeys := map[string]string{}

	sort.Slice(disc.Tools, func(i, j int) bool { return disc.Tools[i].Name < disc.Tools[j].Name })
	for _, tool := range disc.Tools {
		desc := tool.Description
		if desc == "" {
			desc = tool.Title
		}
		opKey := openbindings.ResolveKeyCollision(openbindings.SanitizeKey(tool.Name), "tool", usedKeys)
		usedKeys[opKey] = "tool"
		targets = append(targets, bindableTarget(refPrefixTools+tool.Name, opKey, desc))
	}

	sort.Slice(disc.Resources, func(i, j int) bool { return disc.Resources[i].Name < disc.Resources[j].Name })
	for _, resource := range disc.Resources {
		desc := resource.Description
		if desc == "" {
			desc = resource.Title
		}
		opKey := openbindings.ResolveKeyCollision(openbindings.SanitizeKey(resource.Name), "resource", usedKeys)
		usedKeys[opKey] = "resource"
		targets = append(targets, bindableTarget(refPrefixResources+resource.URI, opKey, desc))
	}

	sort.Slice(disc.ResourceTemplates, func(i, j int) bool { return disc.ResourceTemplates[i].Name < disc.ResourceTemplates[j].Name })
	for _, tmpl := range disc.ResourceTemplates {
		desc := tmpl.Description
		if desc == "" {
			desc = tmpl.Title
		}
		opKey := openbindings.ResolveKeyCollision(openbindings.SanitizeKey(tmpl.Name), "resource_template", usedKeys)
		usedKeys[opKey] = "resource_template"
		targets = append(targets, bindableTarget(refPrefixResources+tmpl.URITemplate, opKey, desc))
	}

	sort.Slice(disc.Prompts, func(i, j int) bool { return disc.Prompts[i].Name < disc.Prompts[j].Name })
	for _, prompt := range disc.Prompts {
		desc := prompt.Description
		if desc == "" {
			desc = prompt.Title
		}
		opKey := openbindings.ResolveKeyCollision(openbindings.SanitizeKey(prompt.Name), "prompt", usedKeys)
		usedKeys[opKey] = "prompt"
		targets = append(targets, bindableTarget(refPrefixPrompts+prompt.Name, opKey, desc))
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
