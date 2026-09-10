package invoke

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync/atomic"

	openbindings "github.com/openbindings/openbindings-go"
)

// ProviderRegistration is one immutable session input.
type ProviderRegistration struct {
	Provider   *PreparedProvider
	Preference float64
}

// CompositionSessionOptions configures application-scoped composition.
type CompositionSessionOptions struct {
	Consumer  *openbindings.PreparedInterface
	Providers []ProviderRegistration
	Policy    CompositionPolicy
}

// CompositionAssessment is stable, serializable refusal evidence. It never
// contains a raw provider or runtime object.
type CompositionAssessment struct {
	Code         string              `json:"code"`
	ProviderKey  string              `json:"providerKey,omitempty"`
	OperationKey string              `json:"operationKey,omitempty"`
	BindingKey   string              `json:"bindingKey,omitempty"`
	BindingSpec  string              `json:"bindingSpec,omitempty"`
	Evidence     *ContractEvidence   `json:"evidence,omitempty"`
	Failure      *CompositionFailure `json:"failure,omitempty"`
	Detail       string              `json:"detail,omitempty"`
}

// CompositionFailure retains a portable invocation failure when one exists.
type CompositionFailure struct {
	Code string `json:"code"`
	Data any    `json:"data,omitempty"`
}

// InspectedRealization is a serializable eligible route identity.
type InspectedRealization struct {
	ProviderKey              string           `json:"providerKey"`
	ProviderOperationKey     string           `json:"providerOperationKey"`
	CorrespondenceIdentifier string           `json:"correspondenceIdentifier"`
	BindingKey               string           `json:"bindingKey"`
	SourceKey                string           `json:"sourceKey"`
	BindingSpec              string           `json:"bindingSpec"`
	Selector                 string           `json:"selector"`
	Evidence                 ContractEvidence `json:"evidence"`
}

// InspectedProvider is a provider policy candidate and its realizations.
type InspectedProvider struct {
	ProviderKey  string                 `json:"providerKey"`
	Preference   float64                `json:"preference"`
	Realizations []InspectedRealization `json:"realizations"`
}

// DependencyInspection is exhaustive static evidence for one dependency.
type DependencyInspection struct {
	SessionID            string                  `json:"sessionId"`
	PolicyID             string                  `json:"policyId"`
	DependencyKey        string                  `json:"dependencyKey"`
	RequiredOperationKey string                  `json:"requiredOperationKey"`
	Providers            []InspectedProvider     `json:"providers"`
	Assessments          []CompositionAssessment `json:"assessments"`
}

// CompositionAmbiguity distinguishes provider election from within-provider
// realization election.
type CompositionAmbiguity struct {
	Stage        string                 `json:"stage"`
	Providers    []string               `json:"providers"`
	Realizations []InspectedRealization `json:"realizations"`
}

// DependencyResolutionStatus is the conservative route-to-one state.
type DependencyResolutionStatus string

const (
	DependencyAvailable   DependencyResolutionStatus = "available"
	DependencyAmbiguous   DependencyResolutionStatus = "ambiguous"
	DependencyUnavailable DependencyResolutionStatus = "unavailable"
)

// DependencyResolution contains exactly one route, ambiguity, or assessment
// set according to Status.
type DependencyResolution[I, O any] struct {
	Status      DependencyResolutionStatus
	Route       *PreparedDependencyRoute[I, O]
	Ambiguity   *CompositionAmbiguity
	Assessments []CompositionAssessment
}

type eligibleRealization struct {
	provider       *PreparedProvider
	descriptor     ProviderRealizationDescriptor
	correspondence OperationCorrespondence
	evidence       ContractEvidence
}

type eligibleProvider struct {
	provider     *PreparedProvider
	providerKey  string
	preference   float64
	realizations []eligibleRealization
}

// PreparedDependencyRoute is a retained, statically verified route for one
// exact consumer dependency.
type PreparedDependencyRoute[I, O any] struct {
	PolicyID                 string `json:"policyId"`
	ConsumerSnapshotID       string `json:"consumerSnapshotId"`
	DependencyKey            string `json:"dependencyKey"`
	RequiredOperationKey     string `json:"requiredOperationKey"`
	ProviderKey              string `json:"providerKey"`
	ProviderOperationKey     string `json:"providerOperationKey"`
	CorrespondenceIdentifier string `json:"correspondenceIdentifier"`
	BindingKey               string `json:"bindingKey"`
	SourceKey                string `json:"sourceKey"`
	BindingSpec              string `json:"bindingSpec"`

	realization *PreparedRealization
}

// Invoke uses the retained exact realization with typed codegen boundaries.
func (r *PreparedDependencyRoute[I, O]) Invoke(ctx context.Context, opts ...InvokeOption) *TypedInvocation[I, O] {
	return NewTypedInvocation[I, O](r.realization.Invoke(ctx, opts...))
}

// Preflight evaluates current context separately from static route closure.
func (r *PreparedDependencyRoute[I, O]) Preflight(ctx context.Context, opts ...InvokeOption) (*ContextRequiredDetails, error) {
	return r.realization.Preflight(ctx, opts...)
}

// CompositionSession owns immutable consumer/provider revisions and policy.
type CompositionSession struct {
	consumer *openbindings.PreparedInterface
	policy   CompositionPolicy
	revision string

	registrations []ProviderRegistration
}

// Consumer returns the immutable consumer snapshot captured at construction.
func (s *CompositionSession) Consumer() *openbindings.PreparedInterface { return s.consumer }

// Policy returns the policy captured at construction. Custom policy behavior
// must remain stable for the session lifetime, including any captured state.
func (s *CompositionSession) Policy() CompositionPolicy { return s.policy }

var nextSessionID atomic.Uint64

// SessionID correlates this retained local session, never its document values.
func (s *CompositionSession) SessionID() string { return s.revision }

// NewCompositionSession validates and snapshots application registrations.
func NewCompositionSession(options CompositionSessionOptions) (*CompositionSession, error) {
	if options.Consumer == nil {
		return nil, fmt.Errorf("openbindings: prepared consumer interface is required")
	}
	policy := options.Policy
	if policy == nil {
		policy = ReferenceCompositionPolicy
	}
	seen := make(map[string]bool)
	registrations := append([]ProviderRegistration(nil), options.Providers...)
	for _, registration := range registrations {
		if registration.Provider == nil {
			return nil, fmt.Errorf("openbindings: prepared provider is required")
		}
		key := registration.Provider.Key()
		if seen[key] {
			return nil, fmt.Errorf("openbindings: duplicate prepared provider key: %q", key)
		}
		seen[key] = true
		if math.IsNaN(registration.Preference) || math.IsInf(registration.Preference, 0) {
			return nil, fmt.Errorf("openbindings: provider %q preference must be finite", key)
		}
	}
	return &CompositionSession{
		consumer:      options.Consumer,
		policy:        policy,
		revision:      fmt.Sprintf("session:%d", nextSessionID.Add(1)),
		registrations: registrations,
	}, nil
}

// InspectDependency returns exhaustive static evidence without closing a
// realization or running live preflight.
func (s *CompositionSession) InspectDependency(ctx context.Context, dependencyKey string) (*DependencyInspection, error) {
	required, ok := s.consumer.Dependency(dependencyKey)
	if !ok {
		return nil, fmt.Errorf("%w: %s", openbindings.ErrDependencyNotFound, dependencyKey)
	}
	providers, assessments, err := s.evaluate(ctx, required)
	if err != nil {
		return nil, err
	}
	inspectedProviders := make([]InspectedProvider, 0, len(providers))
	for _, provider := range providers {
		realizations := make([]InspectedRealization, 0, len(provider.realizations))
		for _, realization := range provider.realizations {
			realizations = append(realizations, inspectRealization(realization))
		}
		inspectedProviders = append(inspectedProviders, InspectedProvider{
			ProviderKey:  provider.providerKey,
			Preference:   provider.preference,
			Realizations: realizations,
		})
	}
	sort.Slice(inspectedProviders, func(i, j int) bool {
		if inspectedProviders[i].Preference != inspectedProviders[j].Preference {
			return inspectedProviders[i].Preference > inspectedProviders[j].Preference
		}
		return inspectedProviders[i].ProviderKey < inspectedProviders[j].ProviderKey
	})
	return &DependencyInspection{
		SessionID:            s.revision,
		PolicyID:             s.policy.ID(),
		DependencyKey:        dependencyKey,
		RequiredOperationKey: required.OperationKey,
		Providers:            inspectedProviders,
		Assessments:          assessments,
	}, nil
}

// ResolveDependency is the typed free-function form required by Go's lack of
// generic methods.
func ResolveDependency[I, O any](
	ctx context.Context,
	session *CompositionSession,
	signature DependencySignature[I, O],
) (*DependencyResolution[I, O], error) {
	untyped, err := session.resolve(ctx, signature.Key())
	if err != nil {
		return nil, err
	}
	result := &DependencyResolution[I, O]{
		Status:      untyped.status,
		Ambiguity:   untyped.ambiguity,
		Assessments: untyped.assessments,
	}
	if untyped.route != nil {
		result.Route = &PreparedDependencyRoute[I, O]{
			PolicyID:                 untyped.route.PolicyID,
			ConsumerSnapshotID:       untyped.route.ConsumerSnapshotID,
			DependencyKey:            untyped.route.DependencyKey,
			RequiredOperationKey:     untyped.route.RequiredOperationKey,
			ProviderKey:              untyped.route.ProviderKey,
			ProviderOperationKey:     untyped.route.ProviderOperationKey,
			CorrespondenceIdentifier: untyped.route.CorrespondenceIdentifier,
			BindingKey:               untyped.route.BindingKey,
			SourceKey:                untyped.route.SourceKey,
			BindingSpec:              untyped.route.BindingSpec,
			realization:              untyped.route.realization,
		}
	}
	return result, nil
}

type untypedResolution struct {
	status      DependencyResolutionStatus
	route       *PreparedDependencyRoute[any, any]
	ambiguity   *CompositionAmbiguity
	assessments []CompositionAssessment
}

func (s *CompositionSession) resolve(ctx context.Context, dependencyKey string) (*untypedResolution, error) {
	required, ok := s.consumer.Dependency(dependencyKey)
	if !ok {
		return nil, fmt.Errorf("%w: %s", openbindings.ErrDependencyNotFound, dependencyKey)
	}
	registrationsByKey := make(map[string]ProviderRegistration, len(s.registrations))
	policyCandidates := make([]ProviderPolicyCandidate, 0, len(s.registrations))
	for _, registration := range s.registrations {
		registrationsByKey[registration.Provider.Key()] = registration
		policyCandidates = append(policyCandidates, ProviderPolicyCandidate{
			ProviderKey: registration.Provider.Key(),
			Preference:  registration.Preference,
		})
	}
	inspectionPlan := s.policy.ProviderInspectionGroups(policyCandidates)
	planned := make(map[string]bool, len(s.registrations))
	for _, group := range inspectionPlan {
		for _, candidate := range group {
			if planned[candidate.ProviderKey] {
				return nil, fmt.Errorf("openbindings: composition policy planned provider %q more than once", candidate.ProviderKey)
			}
			if _, exists := registrationsByKey[candidate.ProviderKey]; !exists {
				return nil, fmt.Errorf("openbindings: composition policy planned unknown provider %q", candidate.ProviderKey)
			}
			planned[candidate.ProviderKey] = true
		}
	}
	if len(planned) != len(s.registrations) {
		return nil, fmt.Errorf("openbindings: composition policy provider inspection plan omitted a provider")
	}

	assessments := make([]CompositionAssessment, 0)
	var selectedProvider eligibleProvider
	for _, group := range inspectionPlan {
		registrations := make([]ProviderRegistration, 0, len(group))
		for _, candidate := range group {
			registrations = append(registrations, registrationsByKey[candidate.ProviderKey])
		}
		providers, groupAssessments, err := s.evaluateRegistrations(ctx, required, registrations)
		if err != nil {
			return nil, err
		}
		assessments = append(assessments, groupAssessments...)
		if len(providers) == 0 {
			continue
		}
		eligibleCandidates := make([]ProviderPolicyCandidate, 0, len(providers))
		for _, provider := range providers {
			eligibleCandidates = append(eligibleCandidates, ProviderPolicyCandidate{
				ProviderKey: provider.providerKey,
				Preference:  provider.preference,
			})
		}
		providerSelection := s.policy.SelectProvider(eligibleCandidates)
		validProvider := func(key string) bool {
			for _, candidate := range eligibleCandidates {
				if candidate.ProviderKey == key {
					return true
				}
			}
			return false
		}
		if providerSelection.Status != "selected" && providerSelection.Status != "ambiguous" && providerSelection.Status != "unavailable" {
			return nil, fmt.Errorf("openbindings: composition policy returned an invalid provider selection status")
		}
		if providerSelection.Status == "selected" && (providerSelection.Provider == nil || !validProvider(providerSelection.Provider.ProviderKey)) {
			return nil, fmt.Errorf("openbindings: composition policy selected an unknown provider")
		}
		if providerSelection.Status == "ambiguous" {
			seen := make(map[string]bool)
			for _, candidate := range providerSelection.Ambiguous {
				if seen[candidate.ProviderKey] || !validProvider(candidate.ProviderKey) {
					return nil, fmt.Errorf("openbindings: composition policy returned invalid provider ambiguity")
				}
				seen[candidate.ProviderKey] = true
			}
			if len(seen) < 2 {
				return nil, fmt.Errorf("openbindings: composition policy returned invalid provider ambiguity")
			}
		}
		switch providerSelection.Status {
		case "unavailable":
			continue
		case "ambiguous":
			keys := make([]string, 0, len(providerSelection.Ambiguous))
			keySet := make(map[string]bool)
			for _, candidate := range providerSelection.Ambiguous {
				keys = append(keys, candidate.ProviderKey)
				keySet[candidate.ProviderKey] = true
			}
			realizations := make([]InspectedRealization, 0)
			for _, provider := range providers {
				if keySet[provider.providerKey] {
					for _, candidate := range provider.realizations {
						realizations = append(realizations, inspectRealization(candidate))
					}
				}
			}
			sort.Strings(keys)
			return &untypedResolution{
				status:    DependencyAmbiguous,
				ambiguity: &CompositionAmbiguity{Stage: "provider", Providers: keys, Realizations: realizations},
			}, nil
		}
		for _, provider := range providers {
			if providerSelection.Provider != nil && provider.providerKey == providerSelection.Provider.ProviderKey {
				selectedProvider = provider
				break
			}
		}
		if selectedProvider.provider != nil {
			break
		}
	}
	if selectedProvider.provider == nil {
		return &untypedResolution{status: DependencyUnavailable, assessments: assessments}, nil
	}
	descriptors := make([]ProviderRealizationDescriptor, 0, len(selectedProvider.realizations))
	for _, candidate := range selectedProvider.realizations {
		descriptors = append(descriptors, candidate.descriptor)
	}
	realizationSelection := s.policy.SelectRealization(descriptors, selectedProvider.provider.RealizationSelector())
	validRealization := func(key string) bool {
		for _, candidate := range selectedProvider.realizations {
			if candidate.descriptor.BindingKey == key {
				return true
			}
		}
		return false
	}
	if realizationSelection.Status != "selected" && realizationSelection.Status != "ambiguous" && realizationSelection.Status != "unavailable" {
		return nil, fmt.Errorf("openbindings: composition policy returned an invalid realization selection status")
	}
	if realizationSelection.Status == "selected" && (realizationSelection.Realization == nil || !validRealization(realizationSelection.Realization.BindingKey)) {
		return nil, fmt.Errorf("openbindings: composition policy selected an unknown realization")
	}
	if realizationSelection.Status == "ambiguous" {
		seen := make(map[string]bool)
		for _, candidate := range realizationSelection.Ambiguous {
			if seen[candidate.BindingKey] || !validRealization(candidate.BindingKey) {
				return nil, fmt.Errorf("openbindings: composition policy returned invalid realization ambiguity")
			}
			seen[candidate.BindingKey] = true
		}
		if len(seen) < 2 {
			return nil, fmt.Errorf("openbindings: composition policy returned invalid realization ambiguity")
		}
	}
	switch realizationSelection.Status {
	case "ambiguous":
		keySet := make(map[string]bool)
		for _, descriptor := range realizationSelection.Ambiguous {
			keySet[descriptor.BindingKey] = true
		}
		realizations := make([]InspectedRealization, 0)
		for _, candidate := range selectedProvider.realizations {
			if keySet[candidate.descriptor.BindingKey] {
				realizations = append(realizations, inspectRealization(candidate))
			}
		}
		return &untypedResolution{
			status: DependencyAmbiguous,
			ambiguity: &CompositionAmbiguity{
				Stage: "realization", Providers: []string{selectedProvider.providerKey}, Realizations: realizations,
			},
		}, nil
	case "unavailable":
		return &untypedResolution{
			status: DependencyUnavailable,
			assessments: append(assessments, CompositionAssessment{
				Code: "realization_selection_declined", ProviderKey: selectedProvider.providerKey, Detail: realizationSelection.Detail,
			}),
		}, nil
	}

	var selected eligibleRealization
	for _, candidate := range selectedProvider.realizations {
		if candidate.descriptor.BindingKey == realizationSelection.Realization.BindingKey {
			selected = candidate
			break
		}
	}
	realization, err := selected.provider.CloseRealization(ctx, selected.descriptor.BindingKey)
	if err != nil {
		assessment := CompositionAssessment{
			Code:         "realization_closure_failed",
			ProviderKey:  selected.provider.Key(),
			OperationKey: selected.correspondence.Provider.CanonicalKey,
			BindingKey:   selected.descriptor.BindingKey,
			BindingSpec:  selected.descriptor.BindingSpec,
			Detail:       err.Error(),
		}
		if invocationError, ok := err.(*InvocationError); ok {
			assessment.Failure = &CompositionFailure{Code: invocationError.Code, Data: invocationError.Data}
		}
		return &untypedResolution{
			status:      DependencyUnavailable,
			assessments: append(assessments, assessment),
		}, nil
	}
	return &untypedResolution{
		status: DependencyAvailable,
		route: &PreparedDependencyRoute[any, any]{
			PolicyID:                 s.policy.ID(),
			ConsumerSnapshotID:       s.consumer.SnapshotID(),
			DependencyKey:            required.Key,
			RequiredOperationKey:     required.OperationKey,
			ProviderKey:              selected.provider.Key(),
			ProviderOperationKey:     selected.correspondence.Provider.CanonicalKey,
			CorrespondenceIdentifier: selected.correspondence.Identifier,
			BindingKey:               selected.descriptor.BindingKey,
			SourceKey:                selected.descriptor.SourceKey,
			BindingSpec:              selected.descriptor.BindingSpec,
			realization:              realization,
		},
	}, nil
}

func (s *CompositionSession) evaluate(
	ctx context.Context,
	required openbindings.PreparedDependencyDescriptor,
) ([]eligibleProvider, []CompositionAssessment, error) {
	return s.evaluateRegistrations(ctx, required, s.registrations)
}

func (s *CompositionSession) evaluateRegistrations(
	ctx context.Context,
	required openbindings.PreparedDependencyDescriptor,
	registrations []ProviderRegistration,
) ([]eligibleProvider, []CompositionAssessment, error) {
	providers := make([]eligibleProvider, 0)
	assessments := make([]CompositionAssessment, 0)
	requiredOperation, _ := s.consumer.Operation(required.OperationKey)
	for _, registration := range registrations {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		provider := registration.Provider
		if provider.Disposed() {
			assessments = append(assessments, CompositionAssessment{Code: "provider_disposed", ProviderKey: provider.Key()})
			continue
		}
		correspondences := s.policy.Correspondences(requiredOperation, provider.PreparedInterface())
		if len(correspondences) == 0 {
			assessments = append(assessments, CompositionAssessment{
				Code: "operation_missing", ProviderKey: provider.Key(), OperationKey: required.OperationKey,
			})
			continue
		}
		realizationMap := make(map[string]eligibleRealization)
		for _, correspondence := range correspondences {
			evidence, err := assessContractAbortable(ctx, s.policy, s.consumer, correspondence, provider.PreparedInterface())
			if err != nil {
				return nil, nil, err
			}
			if evidence.Verdict != ContractCompatible {
				code := "contract_incompatible"
				if evidence.Verdict == ContractIndeterminate {
					code = "contract_indeterminate"
				}
				copyEvidence := evidence
				assessments = append(assessments, CompositionAssessment{
					Code: code, ProviderKey: provider.Key(), OperationKey: correspondence.Provider.CanonicalKey, Evidence: &copyEvidence,
				})
				continue
			}
			descriptors := provider.RealizationsForOperation(correspondence.Provider.CanonicalKey)
			if len(descriptors) == 0 {
				assessments = append(assessments, CompositionAssessment{
					Code: "operation_unbound", ProviderKey: provider.Key(), OperationKey: correspondence.Provider.CanonicalKey,
				})
				continue
			}
			for _, descriptor := range descriptors {
				if !bindingSpecAllowed(required, descriptor.BindingSpec) {
					assessments = append(assessments, CompositionAssessment{
						Code: "binding_spec_disallowed", ProviderKey: provider.Key(), OperationKey: descriptor.OperationKey,
						BindingKey: descriptor.BindingKey, BindingSpec: descriptor.BindingSpec,
					})
					continue
				}
				if !descriptor.Supported {
					assessments = append(assessments, CompositionAssessment{
						Code: "binding_spec_unsupported", ProviderKey: provider.Key(), OperationKey: descriptor.OperationKey,
						BindingKey: descriptor.BindingKey, BindingSpec: descriptor.BindingSpec,
					})
					continue
				}
				realizationMap[descriptor.BindingKey] = eligibleRealization{
					provider: provider, descriptor: descriptor, correspondence: correspondence, evidence: evidence,
				}
			}
		}
		if len(realizationMap) > 0 {
			realizations := make([]eligibleRealization, 0, len(realizationMap))
			for _, realization := range realizationMap {
				realizations = append(realizations, realization)
			}
			sort.Slice(realizations, func(i, j int) bool {
				return realizations[i].descriptor.BindingKey < realizations[j].descriptor.BindingKey
			})
			providers = append(providers, eligibleProvider{
				provider: provider, providerKey: provider.Key(), preference: registration.Preference, realizations: realizations,
			})
		}
	}
	sortAssessments(assessments)
	return providers, assessments, nil
}

func bindingSpecAllowed(dependency openbindings.PreparedDependencyDescriptor, bindingSpec string) bool {
	if !dependency.BindingSpecsPresent {
		return true
	}
	for _, allowed := range dependency.BindingSpecs {
		if allowed == bindingSpec {
			return true
		}
	}
	return false
}

func inspectRealization(candidate eligibleRealization) InspectedRealization {
	return InspectedRealization{
		ProviderKey:              candidate.provider.Key(),
		ProviderOperationKey:     candidate.correspondence.Provider.CanonicalKey,
		CorrespondenceIdentifier: candidate.correspondence.Identifier,
		BindingKey:               candidate.descriptor.BindingKey,
		SourceKey:                candidate.descriptor.SourceKey,
		BindingSpec:              candidate.descriptor.BindingSpec,
		Selector:                 candidate.descriptor.Selector,
		Evidence:                 candidate.evidence,
	}
}

func assessContractAbortable(
	ctx context.Context,
	policy CompositionPolicy,
	required *openbindings.PreparedInterface,
	correspondence OperationCorrespondence,
	provider *openbindings.PreparedInterface,
) (ContractEvidence, error) {
	type result struct {
		evidence ContractEvidence
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		evidence, err := policy.AssessContract(ctx, required, correspondence, provider)
		completed <- result{evidence: evidence, err: err}
	}()
	select {
	case <-ctx.Done():
		return ContractEvidence{}, ctx.Err()
	case result := <-completed:
		return result.evidence, result.err
	}
}

func sortAssessments(values []CompositionAssessment) {
	sort.SliceStable(values, func(i, j int) bool {
		left := values[i].ProviderKey + "\x00" + values[i].OperationKey + "\x00" + values[i].BindingKey + "\x00" + values[i].Code
		right := values[j].ProviderKey + "\x00" + values[j].OperationKey + "\x00" + values[j].BindingKey + "\x00" + values[j].Code
		return left < right
	})
}
