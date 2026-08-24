package usage

import (
	"errors"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesize"
)

// synthesisCoverage inventories the root command plus every exact primary or
// alias command path admitted by the descriptor. Command-local
// unresolvability is reported without hiding otherwise bindable siblings.
func synthesisCoverage(spec *Spec, iface *openbindings.Interface) []synthesize.SynthesisCoverageEntry {
	if spec == nil || iface == nil {
		return []synthesize.SynthesisCoverageEntry{}
	}
	type identity struct {
		operation string
		selector  string
	}
	represented := make(map[string]identity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		represented[binding.Selector] = identity{operation: binding.Operation, selector: binding.Selector}
	}
	var entries []synthesize.SynthesisCoverageEntry
	add := func(sourceRef, bindingSelector string, scope synthesize.SynthesisCoverageScope, exclusion *synthesize.SynthesisCoverageEntry) {
		if exclusion != nil {
			exclusion.Scope = scope
			entries = append(entries, *exclusion)
			return
		}
		id, ok := represented[bindingSelector]
		if !ok {
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: sourceRef, Scope: scope,
				Status: synthesize.SynthesisImplementationUnsupported, ReasonCode: "usage.missing_emitted_binding",
				Message: "the synthesizer returned without emitting this resolvable command path",
			})
			return
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: sourceRef, Scope: scope,
			Status: synthesize.SynthesisRepresented, OperationKey: id.operation, BindingSelector: id.selector,
		})
	}
	excluded := func(sourceRef, reasonCode, rule, message string) *synthesize.SynthesisCoverageEntry {
		return &synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: sourceRef,
			Status: synthesize.SynthesisExcluded, ReasonCode: reasonCode, Rule: rule, Message: message,
		}
	}

	meta := spec.Meta()
	missingBin := meta.Bin == ""
	rc := rootCommand(spec)
	if rc == nil {
		rc = &Command{}
	}
	_, rootErr := generateInputSchema(*rc, nil)
	switch {
	case missingBin:
		add("<root>", "", synthesize.SynthesisCoverageTarget, excluded("<root>", "usage.missing_target_identity", "USAGE-P-03", "the descriptor has no non-empty bin target identity"))
	case rootErr != nil:
		add("<root>", "", synthesize.SynthesisCoverageTarget, excluded("<root>", "usage.unresolvable_surface", "USAGE-P-04", rootErr.Error()))
	default:
		add("<root>", "", synthesize.SynthesisCoverageTarget, nil)
	}

	ambiguousReported := map[string]bool{}
	walkWithGlobals(spec, func(path []string, cmd Command, inherited []Flag) {
		if len(path) == 0 {
			return
		}
		// A subcommand-required group is navigation in the artifact, not an
		// invocable interaction. Its descendants are still inventoried.
		if cmd.SubcommandRequired {
			return
		}
		var selectors []string
		for _, selector := range commandSelectorAlternatives(spec, path) {
			resolved, err := findCommand(spec, selector)
			if errors.Is(err, errAmbiguousCommandSpelling) {
				if !ambiguousReported[selector] {
					ambiguousReported[selector] = true
					sourceRef := "ambiguous-selector:" + selector
					add(sourceRef, "", synthesize.SynthesisCoverageAlternative, excluded(
						sourceRef,
						"usage.ambiguous_command_spelling",
						"USAGE-D-03",
						"the command path matches more than one sibling command and declaration order is not target identity",
					))
				}
				continue
			}
			if err == nil && strings.Join(resolved.canonicalPath, "\x00") == strings.Join(path, "\x00") {
				selectors = append(selectors, selector)
			}
		}
		if len(selectors) == 0 {
			sourceRef := "command:" + commandSelector(path)
			add(sourceRef, "", synthesize.SynthesisCoverageTarget, excluded(
				sourceRef,
				"usage.no_unique_command_selector",
				"USAGE-D-03",
				"the command has no spelling path that resolves uniquely through the descriptor",
			))
			return
		}
		if missingBin {
			for _, selector := range selectors {
				add(selector, selector, synthesize.SynthesisCoverageTarget, excluded(selector, "usage.missing_target_identity", "USAGE-P-03", "the descriptor has no non-empty bin target identity"))
			}
			return
		}
		if _, err := generateInputSchema(cmd, inherited); err != nil {
			for _, selector := range selectors {
				add(selector, selector, synthesize.SynthesisCoverageTarget, excluded(selector, "usage.unresolvable_surface", "USAGE-P-04", err.Error()))
			}
			return
		}
		for _, selector := range selectors {
			add(selector, selector, synthesize.SynthesisCoverageTarget, nil)
		}
	})
	return entries
}
