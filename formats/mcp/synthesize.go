package mcp

import (
	"fmt"
	"sort"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/yosida95/uritemplate/v3"
)

const DefaultSourceName = "mcpServer"

func convertToInterface(disc *discovery, sourceLocation string) (*openbindings.Interface, error) {
	if disc == nil {
		return nil, fmt.Errorf("nil discovery result")
	}

	sourceEntry := openbindings.Source{
		BindingSpec: BindingSpec,
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

		// Static resources take no input value (openbindings.mcp@1 §8/§9.1):
		// the URI is the binding's ref, not caller input, so the operation
		// declares no input schema.
		op := openbindings.Operation{
			Description: desc,
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
		if in := templateInputSchema(tmpl.URITemplate); in != nil {
			op.Input = in
		}

		iface.Operations[opKey] = op

		bindingKey := opKey + "." + DefaultSourceName
		iface.Bindings[bindingKey] = openbindings.BindingEntry{
			Operation: opKey,
			Source:    DefaultSourceName,
			Ref:       refPrefixResourceTemplates + tmpl.URITemplate,
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

	// MCP discovery does not expose security metadata, and OBI documents carry
	// no security section: runtime context is negotiated at call time. If the
	// server requires auth, an unauthenticated call surfaces as a terminal
	// ERR_AUTH_REQUIRED (this invoker derives no static CONTEXT_REQUIRED
	// challenge; resolution happens above the binding).

	return &iface, nil
}

// templateInputSchema derives a resource template's input schema from its
// RFC 6570 variables — the operation's input value per openbindings.mcp@1
// §8/§9.1: one string property per declared variable (template variables are
// string-typed, never coerced), none required (an unsupplied variable
// follows RFC 6570's undefined-value expansion), and no undeclared members
// (the invoker refuses them, hence additionalProperties: false). A template
// that does not parse yields no input schema; the invoker refuses it loudly
// at invocation time.
func templateInputSchema(template string) map[string]any {
	tmpl, err := uritemplate.New(template)
	if err != nil {
		return nil
	}
	properties := map[string]any{}
	for _, name := range tmpl.Varnames() {
		properties[name] = map[string]any{"type": "string"}
	}
	return map[string]any{
		"type":                 "object",
		"description":          fmt.Sprintf("Variables of RFC 6570 template %q", template),
		"properties":           properties,
		"additionalProperties": false,
	}
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
