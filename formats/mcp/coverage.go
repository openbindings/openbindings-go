package mcp

import (
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/yosida95/uritemplate/v3"
)

// synthesisCoverage accounts for every entity in the pagination-exhausted
// listing observed by synthesis. The four MCP list families are already the
// binding specification's interaction inventory.
func synthesisCoverage(disc *discovery, iface *openbindings.Interface) []openbindings.SynthesisCoverageEntry {
	if disc == nil || iface == nil {
		return []openbindings.SynthesisCoverageEntry{}
	}
	type identity struct {
		operation string
		ref       string
	}
	represented := make(map[string]identity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		represented[binding.Ref] = identity{operation: binding.Operation, ref: binding.Ref}
	}
	bindingSpec := BindingSpec
	if source, ok := iface.Sources[DefaultSourceName]; ok && source.BindingSpec != "" {
		bindingSpec = source.BindingSpec
	}

	toolCounts := map[string]int{}
	resourceCounts := map[string]int{}
	templateCounts := map[string]int{}
	promptCounts := map[string]int{}
	for _, entity := range disc.Tools {
		if entity != nil {
			toolCounts[entity.Name]++
		}
	}
	for _, entity := range disc.Resources {
		if entity != nil {
			resourceCounts[entity.URI]++
		}
	}
	for _, entity := range disc.ResourceTemplates {
		if entity != nil {
			templateCounts[entity.URITemplate]++
		}
	}
	for _, entity := range disc.Prompts {
		if entity != nil {
			promptCounts[entity.Name]++
		}
	}

	var entries []openbindings.SynthesisCoverageEntry
	add := func(family, value string, index, count int, invalid string, excludedCode, excludedRule, excludedMessage string) {
		sourceRef := family + "/" + value
		if count > 1 || value == "" {
			sourceRef = fmt.Sprintf("%s#listing-index=%d", sourceRef, index)
		}
		if invalid != "" {
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: sourceRef, Scope: openbindings.SynthesisCoverageTarget,
				Status: openbindings.SynthesisInvalid, ReasonCode: "mcp.invalid_entity", Message: invalid,
			})
			return
		}
		if excludedCode != "" {
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: sourceRef, Scope: openbindings.SynthesisCoverageTarget,
				Status: openbindings.SynthesisExcluded, ReasonCode: excludedCode, Rule: excludedRule, Message: excludedMessage,
			})
			return
		}
		ref := family + "/" + value
		id, ok := represented[ref]
		if !ok {
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: sourceRef, Scope: openbindings.SynthesisCoverageTarget,
				Status: openbindings.SynthesisImplementationUnsupported, ReasonCode: "mcp.missing_emitted_binding",
				Message: "the synthesizer returned without emitting this resolvable listed entity",
			})
			return
		}
		entries = append(entries, openbindings.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: sourceRef, Scope: openbindings.SynthesisCoverageTarget,
			Status: openbindings.SynthesisRepresented, OperationKey: id.operation, BindingRef: id.ref,
		})
	}

	for index, entity := range disc.Tools {
		if entity == nil {
			add("tools", "", index, 1, "tools listing entry is null", "", "", "")
			continue
		}
		if entity.Name == "" {
			add("tools", "", index, 1, "tool name is empty", "", "", "")
			continue
		}
		if toolCounts[entity.Name] > 1 {
			add("tools", entity.Name, index, toolCounts[entity.Name], "", "mcp.ambiguous_identity", "MCP-P-02", "more than one listed tool has this ref identity")
			continue
		}
		if disc.RequiredTaskTools[entity.Name] {
			add("tools", entity.Name, index, 1, "", "mcp.required_task", "MCP-P-08", "the tool requires task augmentation, which this binding revision excludes")
			continue
		}
		if bindingSpec == BindingSpec && entity.OutputSchema == nil {
			add("tools", entity.Name, index, 1, "", "mcp.missing_application_output_schema", "MCP-P-04", "the tool listing does not declare an application outputSchema")
			continue
		}
		add("tools", entity.Name, index, 1, "", "", "", "")
	}
	for index, entity := range disc.Resources {
		if entity == nil || entity.URI == "" {
			add("resources", "", index, 1, "resource URI is absent", "", "", "")
			continue
		}
		if resourceCounts[entity.URI] > 1 {
			add("resources", entity.URI, index, resourceCounts[entity.URI], "", "mcp.ambiguous_identity", "MCP-P-02", "more than one listed resource has this ref identity")
			continue
		}
		if bindingSpec == BindingSpec {
			add("resources", entity.URI, index, 1, "", "mcp.no_application_output_contract", "MCP-P-04", "MCP resource listings do not declare an application output schema")
			continue
		}
		add("resources", entity.URI, index, 1, "", "", "", "")
	}
	for index, entity := range disc.ResourceTemplates {
		if entity == nil || entity.URITemplate == "" {
			add("resourceTemplates", "", index, 1, "resource template identity is absent", "", "", "")
			continue
		}
		if templateCounts[entity.URITemplate] > 1 {
			add("resourceTemplates", entity.URITemplate, index, templateCounts[entity.URITemplate], "", "mcp.ambiguous_identity", "MCP-P-02", "more than one listed resource template has this ref identity")
			continue
		}
		if _, err := uritemplate.New(entity.URITemplate); err != nil {
			add("resourceTemplates", entity.URITemplate, index, 1, "resource template is not valid RFC 6570: "+err.Error(), "", "", "")
			continue
		}
		if bindingSpec == BindingSpec {
			add("resourceTemplates", entity.URITemplate, index, 1, "", "mcp.no_application_output_contract", "MCP-P-04", "MCP resource-template listings do not declare an application output schema")
			continue
		}
		add("resourceTemplates", entity.URITemplate, index, 1, "", "", "", "")
	}
	for index, entity := range disc.Prompts {
		if entity == nil || entity.Name == "" {
			add("prompts", "", index, 1, "prompt name is empty", "", "", "")
			continue
		}
		if promptCounts[entity.Name] > 1 {
			add("prompts", entity.Name, index, promptCounts[entity.Name], "", "mcp.ambiguous_identity", "MCP-P-02", "more than one listed prompt has this ref identity")
			continue
		}
		if bindingSpec == BindingSpec {
			add("prompts", entity.Name, index, 1, "", "mcp.no_application_output_contract", "MCP-P-04", "MCP prompt listings do not declare an application output schema")
			continue
		}
		add("prompts", entity.Name, index, 1, "", "", "", "")
	}
	return entries
}
