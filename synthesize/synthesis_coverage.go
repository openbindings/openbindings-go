package synthesize

import (
	"fmt"
	"regexp"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

var synthesisReasonCodePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)+$`)

// NewSynthesisResult validates durable coverage evidence against the emitted
// Interface and derives FullyRepresented. Family synthesizers call this at the
// same boundary where they finalize and validate the OBI.
func NewSynthesisResult(iface *openbindings.Interface, entries []SynthesisCoverageEntry, exhaustive bool) (*SynthesizeResult, error) {
	var limitation *SynthesisCoverageLimitation
	if !exhaustive {
		limitation = &SynthesisCoverageLimitation{
			Code:    "synthesis.inventory_incomplete",
			Message: "the implementation could not establish that its source interaction inventory was exhaustive",
		}
	}
	return NewSynthesisResultWithLimitation(iface, entries, exhaustive, limitation)
}

// NewSynthesisResultWithLimitation is NewSynthesisResult with caller-supplied
// evidence for a non-exhaustive inventory. Exhaustive results reject a
// limitation; non-exhaustive results require one.
func NewSynthesisResultWithLimitation(iface *openbindings.Interface, entries []SynthesisCoverageEntry, exhaustive bool, limitation *SynthesisCoverageLimitation) (*SynthesizeResult, error) {
	if iface == nil {
		return nil, fmt.Errorf("synthesis coverage requires an interface")
	}
	if err := iface.Validate(); err != nil {
		return nil, fmt.Errorf("synthesis coverage interface is invalid: %w", err)
	}
	if exhaustive && limitation != nil {
		return nil, fmt.Errorf("exhaustive synthesis coverage must not carry a limitation")
	}
	if !exhaustive {
		if limitation == nil {
			return nil, fmt.Errorf("non-exhaustive synthesis coverage requires a limitation")
		}
		if !synthesisReasonCodePattern.MatchString(limitation.Code) || limitation.Message == "" {
			return nil, fmt.Errorf("synthesis coverage limitation requires a valid code and message")
		}
	}

	normalizedEntries := append([]SynthesisCoverageEntry(nil), entries...)
	seen := make(map[string]struct{}, len(normalizedEntries))
	fullyRepresented := exhaustive
	representedDependencies := 0
	for index := range normalizedEntries {
		entry := &normalizedEntries[index]
		if entry.SourceIndex < 0 {
			return nil, fmt.Errorf("synthesis coverage entry %d has negative sourceIndex", index)
		}
		if entry.SourceRef == "" {
			return nil, fmt.Errorf("synthesis coverage entry %d has empty sourceRef", index)
		}
		if entry.Scope != SynthesisCoverageTarget && entry.Scope != SynthesisCoverageAlternative && entry.Scope != SynthesisCoverageProjection && entry.Scope != SynthesisCoverageDependency {
			return nil, fmt.Errorf("synthesis coverage entry %d has invalid scope %q", index, entry.Scope)
		}
		key := fmt.Sprintf("%d\x00%s\x00%s", entry.SourceIndex, entry.Scope, entry.SourceRef)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate synthesis coverage entry for source %d %s %q", entry.SourceIndex, entry.Scope, entry.SourceRef)
		}
		seen[key] = struct{}{}

		requirements := make(map[string]struct{}, len(entry.Requirements))
		for _, requirement := range entry.Requirements {
			if requirement == "" {
				return nil, fmt.Errorf("synthesis coverage entry %d has an empty requirement", index)
			}
			if _, duplicate := requirements[requirement]; duplicate {
				return nil, fmt.Errorf("synthesis coverage entry %d repeats requirement %q", index, requirement)
			}
			requirements[requirement] = struct{}{}
		}

		if entry.Scope == SynthesisCoverageDependency {
			if entry.Status != SynthesisRepresented {
				return nil, fmt.Errorf("dependency synthesis coverage entry %d must be represented", index)
			}
			if entry.OperationKey != "" || entry.BindingKey != "" || entry.BindingSelector != "" || entry.SourceKey != "" || entry.ReasonCode != "" {
				return nil, fmt.Errorf("represented dependency synthesis coverage entry %d must not carry operation or binding identity", index)
			}
			representedDependencies++
			continue
		}

		if entry.Status == SynthesisRepresented || entry.Status == SynthesisLossy {
			if entry.OperationKey == "" {
				return nil, fmt.Errorf("%s synthesis coverage entry %d requires operationKey", entry.Status, index)
			}
			if entry.Status == SynthesisRepresented && entry.ReasonCode != "" {
				return nil, fmt.Errorf("represented synthesis coverage entry %d must not carry reasonCode", index)
			}
			if _, ok := iface.Operations[entry.OperationKey]; !ok {
				return nil, fmt.Errorf("%s synthesis coverage entry %d names missing operation %q", entry.Status, index, entry.OperationKey)
			}
			if entry.BindingKey == "" {
				for bindingKey, binding := range iface.Bindings {
					if binding.Operation == entry.OperationKey && binding.Selector == entry.BindingSelector {
						if entry.BindingKey != "" {
							return nil, fmt.Errorf("%s synthesis coverage entry %d matches several bindings; bindingKey is required to disambiguate", entry.Status, index)
						}
						entry.BindingKey = bindingKey
					}
				}
			}
			binding, ok := iface.Bindings[entry.BindingKey]
			if !ok || binding.Operation != entry.OperationKey || binding.Selector != entry.BindingSelector {
				return nil, fmt.Errorf("%s synthesis coverage entry %d has no matching binding for operation %q and selector %q", entry.Status, index, entry.OperationKey, entry.BindingSelector)
			}
			if entry.SourceKey == "" {
				entry.SourceKey = binding.Source
			}
			if binding.Source != entry.SourceKey {
				return nil, fmt.Errorf("%s synthesis coverage entry %d binding %q uses source %q, not %q", entry.Status, index, entry.BindingKey, binding.Source, entry.SourceKey)
			}
			if _, ok := iface.Sources[entry.SourceKey]; !ok {
				return nil, fmt.Errorf("%s synthesis coverage entry %d names missing source %q", entry.Status, index, entry.SourceKey)
			}
		}

		switch entry.Status {
		case SynthesisRepresented:
		case SynthesisExcluded, SynthesisInvalid, SynthesisLossy, SynthesisImplementationUnsupported:
			if !synthesisReasonCodePattern.MatchString(entry.ReasonCode) {
				return nil, fmt.Errorf("synthesis coverage entry %d has invalid reasonCode %q", index, entry.ReasonCode)
			}
			if entry.Message == "" {
				return nil, fmt.Errorf("synthesis coverage entry %d requires a message", index)
			}
			// Every non-represented status clears FullyRepresented — invalid
			// included. An upstream-invalid unit is still an inventoried unit
			// the emitted OBI does not represent (MC5 seal-1 finding F-V3-1: a
			// document whose every target was invalid reported
			// fullyRepresented true).
			fullyRepresented = false
		default:
			return nil, fmt.Errorf("synthesis coverage entry %d has invalid status %q", index, entry.Status)
		}
	}
	if representedDependencies != len(iface.Dependencies) {
		return nil, fmt.Errorf("dependency synthesis coverage represents %d source interactions for %d emitted dependencies", representedDependencies, len(iface.Dependencies))
	}

	return &SynthesizeResult{
		Interface: iface,
		Coverage: SynthesisCoverage{
			Entries:          normalizedEntries,
			Exhaustive:       exhaustive,
			FullyRepresented: fullyRepresented,
			Limitation:       limitation,
		},
	}, nil
}

// RepresentedCoverageEntries returns one target-level represented entry per
// emitted binding. It is useful only for a family that has separately proved
// that every upstream interaction unit maps one-to-one to a binding; callers
// must not use it to label an unknown inventory exhaustive.
func RepresentedCoverageEntries(iface *openbindings.Interface, sourceIndex int) []SynthesisCoverageEntry {
	if iface == nil {
		return nil
	}
	entries := make([]SynthesisCoverageEntry, 0, len(iface.Bindings))
	bindingKeys := make([]string, 0, len(iface.Bindings))
	for key := range iface.Bindings {
		bindingKeys = append(bindingKeys, key)
	}
	sort.Strings(bindingKeys)
	for _, key := range bindingKeys {
		binding := iface.Bindings[key]
		entries = append(entries, SynthesisCoverageEntry{
			SourceIndex:     sourceIndex,
			SourceKey:       binding.Source,
			SourceRef:       binding.Selector,
			Scope:           SynthesisCoverageTarget,
			Status:          SynthesisRepresented,
			OperationKey:    binding.Operation,
			BindingKey:      key,
			BindingSelector: binding.Selector,
		})
	}
	return entries
}
