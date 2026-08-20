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
		ref       string
	}
	represented := make(map[string]identity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		represented[binding.Ref] = identity{operation: binding.Operation, ref: binding.Ref}
	}
	var entries []synthesize.SynthesisCoverageEntry
	add := func(sourceRef, bindingRef string, scope synthesize.SynthesisCoverageScope, exclusion *synthesize.SynthesisCoverageEntry) {
		if exclusion != nil {
			exclusion.Scope = scope
			entries = append(entries, *exclusion)
			return
		}
		id, ok := represented[bindingRef]
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
			Status: synthesize.SynthesisRepresented, OperationKey: id.operation, BindingRef: id.ref,
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
		var refs []string
		for _, ref := range commandRefAlternatives(spec, path) {
			resolved, err := findCommand(spec, ref)
			if errors.Is(err, errAmbiguousCommandSpelling) {
				if !ambiguousReported[ref] {
					ambiguousReported[ref] = true
					sourceRef := "ambiguous-ref:" + ref
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
				refs = append(refs, ref)
			}
		}
		if len(refs) == 0 {
			sourceRef := "command:" + commandRef(path)
			add(sourceRef, "", synthesize.SynthesisCoverageTarget, excluded(
				sourceRef,
				"usage.no_unique_command_ref",
				"USAGE-D-03",
				"the command has no spelling path that resolves uniquely through the descriptor",
			))
			return
		}
		if missingBin {
			for _, ref := range refs {
				add(ref, ref, synthesize.SynthesisCoverageTarget, excluded(ref, "usage.missing_target_identity", "USAGE-P-03", "the descriptor has no non-empty bin target identity"))
			}
			return
		}
		if _, err := generateInputSchema(cmd, inherited); err != nil {
			for _, ref := range refs {
				add(ref, ref, synthesize.SynthesisCoverageTarget, excluded(ref, "usage.unresolvable_surface", "USAGE-P-04", err.Error()))
			}
			return
		}
		for _, ref := range refs {
			add(ref, ref, synthesize.SynthesisCoverageTarget, nil)
		}
	})
	return entries
}
