package mcp

import (
	"fmt"
	"sort"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

const DefaultSourceName = "mcpServer"

func convertToInterface(disc *discovery, sourceLocation string) (*openbindings.Interface, error) {
	if disc == nil {
		return nil, fmt.Errorf("nil discovery result")
	}

	sourceEntry := openbindings.Source{
		Format: FormatToken,
	}
	if sourceLocation != "" {
		sourceEntry.Location = sourceLocation
	}

	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
	}

	if disc.ServerInfo != nil {
		iface.Name = disc.ServerInfo.Name
		iface.Version = disc.ServerInfo.Version
		if disc.ServerInfo.Title != "" {
			iface.Description = disc.ServerInfo.Title
		}
	}

	usedKeys := map[string]string{}

	sort.Slice(disc.Tools, func(i, j int) bool { return disc.Tools[i].Name < disc.Tools[j].Name })
	sort.Slice(disc.Resources, func(i, j int) bool { return disc.Resources[i].Name < disc.Resources[j].Name })
	sort.Slice(disc.ResourceTemplates, func(i, j int) bool { return disc.ResourceTemplates[i].Name < disc.ResourceTemplates[j].Name })
	sort.Slice(disc.Prompts, func(i, j int) bool { return disc.Prompts[i].Name < disc.Prompts[j].Name })

	for _, tool := range disc.Tools {
		opKey := openbindings.SanitizeKey(tool.Name)
		opKey = openbindings.ResolveKeyCollision(opKey, "tool", usedKeys)
		usedKeys[opKey] = "tool"

		desc := tool.Description
		if desc == "" {
			desc = tool.Title
		}

		op := openbindings.Operation{
			Description: desc,
		}

		if tool.InputSchema != nil {
			if schemaMap, ok := tool.InputSchema.(map[string]any); ok {
				op.Input = schemaMap
			}
		}

		if tool.OutputSchema != nil {
			if schemaMap, ok := tool.OutputSchema.(map[string]any); ok {
				op.Output = schemaMap
			}
		}

		iface.Operations[opKey] = op

		bindingKey := opKey + "." + DefaultSourceName
		iface.Bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       refPrefixTools + tool.Name,
		}
	}

	for _, resource := range disc.Resources {
		opKey := openbindings.SanitizeKey(resource.Name)
		opKey = openbindings.ResolveKeyCollision(opKey, "resource", usedKeys)
		usedKeys[opKey] = "resource"

		desc := resource.Description
		if desc == "" {
			desc = resource.Title
		}

		op := openbindings.Operation{
			Description: desc,
		}

		op.Input = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uri": map[string]any{
					"type":        "string",
					"const":       resource.URI,
					"description": "Resource URI",
				},
			},
		}

		iface.Operations[opKey] = op

		bindingKey := opKey + "." + DefaultSourceName
		iface.Bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       refPrefixResources + resource.URI,
		}
	}

	for _, tmpl := range disc.ResourceTemplates {
		opKey := openbindings.SanitizeKey(tmpl.Name)
		opKey = openbindings.ResolveKeyCollision(opKey, "resource_template", usedKeys)
		usedKeys[opKey] = "resource_template"

		desc := tmpl.Description
		if desc == "" {
			desc = tmpl.Title
		}

		op := openbindings.Operation{
			Description: desc,
		}

		op.Input = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uriTemplate": map[string]any{
					"type":        "string",
					"const":       tmpl.URITemplate,
					"description": "URI template (RFC 6570)",
				},
			},
		}

		iface.Operations[opKey] = op

		bindingKey := opKey + "." + DefaultSourceName
		iface.Bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       refPrefixResources + tmpl.URITemplate,
		}
	}

	for _, prompt := range disc.Prompts {
		opKey := openbindings.SanitizeKey(prompt.Name)
		opKey = openbindings.ResolveKeyCollision(opKey, "prompt", usedKeys)
		usedKeys[opKey] = "prompt"

		desc := prompt.Description
		if desc == "" {
			desc = prompt.Title
		}

		op := openbindings.Operation{
			Description: desc,
			Output:      promptOutputSchema(),
		}

		if len(prompt.Arguments) > 0 {
			op.Input = promptArgsToSchema(prompt.Arguments)
		}

		iface.Operations[opKey] = op

		bindingKey := opKey + "." + DefaultSourceName
		iface.Bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       refPrefixPrompts + prompt.Name,
		}
	}

	// MCP discovery does not expose security metadata, so we leave the
	// security section empty. If the server requires auth, the executor's
	// auth retry flow will handle it (401 → resolve credentials → retry).

	return &iface, nil
}

// promptOutputSchema returns a JSON Schema describing the standard MCP
// GetPromptResult structure: an object with messages and optional description.
func promptOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"description": map[string]any{
				"type":        "string",
				"description": "Optional description of the prompt result",
			},
			"messages": map[string]any{
				"type":        "array",
				"description": "Sequence of LLM messages",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"role":    map[string]any{"type": "string"},
						"content": map[string]any{},
					},
					"required": []string{"role", "content"},
				},
			},
		},
		"required": []string{"messages"},
	}
}

func promptArgsToSchema(args []*gomcp.PromptArgument) map[string]any {
	properties := map[string]any{}
	var required []string

	for _, arg := range args {
		if arg == nil {
			continue
		}
		prop := map[string]any{
			"type": "string",
		}
		if arg.Description != "" {
			prop["description"] = arg.Description
		}
		properties[arg.Name] = prop

		if arg.Required {
			required = append(required, arg.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}
