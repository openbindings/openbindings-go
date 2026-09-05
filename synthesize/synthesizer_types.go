package synthesize

import (
	"encoding/json"
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
)

// SynthesizeSource describes a binding source for interface synthesis.
// Content carries raw JSON with the same presence semantics as Source.
type SynthesizeSource struct {
	BindingSpec    string          `json:"bindingSpec"`
	Name           string          `json:"name,omitempty"`
	Location       string          `json:"location,omitempty"`
	Content        json.RawMessage `json:"content,omitempty"`
	OutputLocation string          `json:"outputLocation,omitempty"`
	Embed          bool            `json:"embed,omitempty"`
	Description    string          `json:"description,omitempty"`
}

// SynthesizeInput is the input for synthesizing an OpenBindings interface from format-specific sources.
type SynthesizeInput struct {
	OpenBindingsVersion string             `json:"openbindingsVersion,omitempty"`
	Sources             []SynthesizeSource `json:"sources,omitempty"`
	Name                string             `json:"name,omitempty"`
	Version             string             `json:"version,omitempty"`
	Description         string             `json:"description,omitempty"`

	// OnWarning, when set, is invoked for a non-fatal, usable but lossy schema
	// projection. It must never stand in for omitting a callable target or for
	// returning an operation that is statically guaranteed to refuse; those
	// conditions fail synthesis. nil is acceptable because warning-free use of
	// the returned Interface remains sound.
	OnWarning func(SynthesizerWarning) `json:"-"`
}

// SynthesisSkeleton returns the deterministic source-less result required by
// the interface-synthesizer contract. Family synthesizers use the same helper
// as the combined dispatcher so a source-less call has no family-dependent
// behavior.
func SynthesisSkeleton(in *SynthesizeInput) (openbindings.Interface, error) {
	version := openbindings.MaxTestedVersion
	var name, contractVersion, description string
	if in != nil {
		if in.OpenBindingsVersion != "" {
			version = in.OpenBindingsVersion
		}
		name = in.Name
		contractVersion = in.Version
		description = in.Description
	}
	iface := openbindings.Interface{
		OpenBindings: version,
		Name:         name,
		Version:      contractVersion,
		Description:  description,
		Operations:   map[string]openbindings.Operation{},
	}
	if err := iface.Validate(); err != nil {
		return openbindings.Interface{}, fmt.Errorf("source-less synthesis target is invalid: %w", err)
	}
	return iface, nil
}

// FinalizeSynthesis applies the format-neutral authoring directives shared by
// all single-source synthesizers and validates the emitted OBI. Artifact
// acquisition and the embed directive remain family work because only the
// governing binding specification knows what bytes constitute the source.
func FinalizeSynthesis(iface *openbindings.Interface, in *SynthesizeInput, defaultSourceName, bindingSpec string) error {
	if iface == nil || in == nil || len(in.Sources) != 1 {
		return fmt.Errorf("finalize synthesis requires one source and one interface")
	}
	src := in.Sources[0]
	if src.BindingSpec != bindingSpec {
		return fmt.Errorf("synthesizer supports exact binding specification %q, got %q", bindingSpec, src.BindingSpec)
	}
	if in.OpenBindingsVersion != "" {
		iface.OpenBindings = in.OpenBindingsVersion
	}
	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}

	entry, ok := iface.Sources[defaultSourceName]
	if !ok {
		return fmt.Errorf("synthesizer emitted no source %q", defaultSourceName)
	}
	entry.BindingSpec = bindingSpec
	if src.OutputLocation != "" {
		entry.Location = src.OutputLocation
	}
	if src.Description != "" {
		entry.Description = src.Description
	}
	outputName := defaultSourceName
	if src.Name != "" {
		outputName = src.Name
	}
	delete(iface.Sources, defaultSourceName)
	iface.Sources[outputName] = entry
	if outputName != defaultSourceName {
		for key, binding := range iface.Bindings {
			if binding.Source == defaultSourceName {
				binding.Source = outputName
				iface.Bindings[key] = binding
			}
		}
	}
	if err := iface.Validate(); err != nil {
		return fmt.Errorf("synthesized interface is invalid: %w", err)
	}
	return nil
}

// SynthesizerWarning describes a non-fatal, usable but lossy projection made
// while building an Interface. Warnings do not block synthesis; every returned
// operation remains bindable. Consumers may surface warnings in tooling output
// (CLI, registry publish checks, CI) to inform users about lossy conversions.
type SynthesizerWarning struct {
	// Code is a stable machine-readable identifier for programmatic handling.
	// Format-specific codes should be namespaced with the format token as a
	// prefix (e.g., "grpc.multi_group_oneof").
	Code string `json:"code"`
	// Message is a human-readable description of the limitation.
	Message string `json:"message"`
	// Path identifies the location within the Interface that the warning
	// refers to, using dotted notation (e.g., "operations.GetItem.input").
	// Empty when the warning applies to the whole interface.
	Path string `json:"path,omitempty"`
	// Details carries format-specific context. May be nil.
	Details map[string]any `json:"details,omitempty"`
}

// SynthesisCoverageScope identifies the granularity of a synthesis coverage
// entry.
type SynthesisCoverageScope string

const (
	// SynthesisCoverageSource is the complete source artifact when a binding
	// defines a source-scope exclusion that is distinct from load refusal.
	SynthesisCoverageSource SynthesisCoverageScope = "source"
	// SynthesisCoverageTarget is an addressable source interaction.
	SynthesisCoverageTarget SynthesisCoverageScope = "target"
	// SynthesisCoverageAlternative is an independently selectable alternative
	// whose omission would remove a source-permitted invocation path.
	SynthesisCoverageAlternative SynthesisCoverageScope = "alternative"
	// SynthesisCoverageProjection is a source contract projection into an OBI
	// schema or operation framing.
	SynthesisCoverageProjection SynthesisCoverageScope = "projection"
	// SynthesisCoverageDependency is an inbound source interaction represented
	// as a targetless Core dependency rather than as a binding.
	SynthesisCoverageDependency SynthesisCoverageScope = "dependency"
)

// SynthesisCoverageStatus is the durable disposition of one observed source
// interaction unit.
type SynthesisCoverageStatus string

const (
	// SynthesisRepresented means the emitted OBI carries either an operation
	// and binding path for the unit or, at dependency scope, a Core dependency.
	SynthesisRepresented SynthesisCoverageStatus = "represented"
	// SynthesisExcluded means the governing binding-specification revision
	// explicitly excludes the upstream-valid unit.
	SynthesisExcluded SynthesisCoverageStatus = "excluded"
	// SynthesisInvalid means the source unit is malformed or internally
	// contradictory under its upstream authority.
	SynthesisInvalid SynthesisCoverageStatus = "invalid"
	// SynthesisLossy means invocation remains usable but the emitted OBI
	// framing cannot express part of the source contract exactly.
	SynthesisLossy SynthesisCoverageStatus = "lossy"
	// SynthesisImplementationUnsupported means the binding revision admits the
	// unit but this synthesizer cannot represent it. Reference SDK releases
	// must carry no such entries.
	SynthesisImplementationUnsupported SynthesisCoverageStatus = "implementation-unsupported"
)

// SynthesisCoverageEntry records the disposition of one source interaction
// unit observed during the same call that produced the synthesized Interface.
type SynthesisCoverageEntry struct {
	// SourceIndex is the zero-based index of the input source containing the
	// interaction unit.
	SourceIndex int `json:"sourceIndex"`
	// SourceKey is the corresponding source key in the emitted OBI. It is
	// required for represented and lossy entries.
	SourceKey string `json:"sourceKey,omitempty"`
	// SourceRef is a stable source-local identifier. It need not be a conformant
	// binding selector for an excluded or invalid unit.
	SourceRef string `json:"sourceRef"`
	// Scope distinguishes addressable targets from independently selectable
	// alternatives of a target.
	Scope SynthesisCoverageScope `json:"scope"`
	// Status is the unit's durable disposition.
	Status SynthesisCoverageStatus `json:"status"`
	// OperationKey, BindingKey, and BindingSelector are required for
	// represented and lossy entries.
	// BindingSelector is serialized even when empty because some binding
	// specifications identify a valid target by an omitted selector.
	OperationKey    string `json:"operationKey,omitempty"`
	BindingKey      string `json:"bindingKey,omitempty"`
	BindingSelector string `json:"bindingSelector"`
	// ReasonCode and Message are required for every non-represented entry.
	// ReasonCode is stable and family-namespaced; Message is diagnostic prose.
	ReasonCode string `json:"reasonCode,omitempty"`
	Rule       string `json:"rule,omitempty"`
	Message    string `json:"message,omitempty"`
	// Requirements names runtime prerequisites without treating them as
	// synthesis omissions.
	Requirements []string `json:"requirements,omitempty"`
	// Details carries family-specific structured evidence.
	Details map[string]any `json:"details,omitempty"`
}

// SynthesisCoverage is durable coverage evidence for one synthesis call.
type SynthesisCoverage struct {
	Entries []SynthesisCoverageEntry `json:"entries"`
	// Exhaustive is true when every interaction unit in every accepted source,
	// under the governing binding revision's inventory, has one disposition.
	Exhaustive bool `json:"exhaustive"`
	// FullyRepresented is true when Exhaustive is true and every inventoried
	// unit is represented. Any non-represented entry — lossy, excluded,
	// invalid, or implementation-unsupported — clears it.
	// NewSynthesisResult derives it from Entries.
	FullyRepresented bool `json:"fullyRepresented"`
	// Limitation explains what may be missing when Exhaustive is false.
	Limitation *SynthesisCoverageLimitation `json:"limitation,omitempty"`
}

// SynthesisCoverageLimitation explains why a synthesis inventory is not
// exhaustive.
type SynthesisCoverageLimitation struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// SynthesizeResult carries a creation-time-sound Interface and the coverage
// evidence derived from the same synthesis observation.
type SynthesizeResult struct {
	Interface *openbindings.Interface `json:"interface"`
	Coverage  SynthesisCoverage       `json:"coverage"`
}

// SourceInspection is the result of inspecting a source for bindable targets.
type SourceInspection struct {
	// Targets is the list of bindable targets discovered in the source.
	Targets []BindableTarget `json:"targets"`
	// Exhaustive is true when this is the complete list of targets for the
	// source. When false, additional targets may exist that were not enumerated.
	Exhaustive bool `json:"exhaustive"`
	// Limitation explains a non-exhaustive result. It is required when
	// Exhaustive is false.
	Limitation *InspectionLimitation `json:"limitation,omitempty"`
}

// InspectionLimitation explains why a source inspection is not exhaustive.
type InspectionLimitation struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// BindableTarget describes a target within a source that can be framed as an
// OpenBindings operation.
type BindableTarget struct {
	// Selector is the selector string to use in a binding entry.
	Selector string `json:"selector"`
	// OperationKey is an optional suggested operation key for this target.
	OperationKey string `json:"operationKey,omitempty"`
	// Operation is an optional OpenBindings operation framing for this target.
	Operation *openbindings.Operation `json:"operation,omitempty"`
}
