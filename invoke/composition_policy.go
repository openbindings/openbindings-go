package invoke

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/compare"
)

// ReferenceCompositionPolicyID is the portable identifier of the first SDK
// reference policy.
const ReferenceCompositionPolicyID = "openbindings.reference-composition@1"

// ContractVerdict is three-valued compatibility evidence.
type ContractVerdict string

const (
	ContractCompatible    ContractVerdict = "compatible"
	ContractIncompatible  ContractVerdict = "incompatible"
	ContractIndeterminate ContractVerdict = "indeterminate"
)

// OperationCorrespondence records a name/alias intersection before contract
// compatibility is assessed.
type OperationCorrespondence struct {
	Identifier string
	Required   openbindings.PreparedOperationDescriptor
	Provider   openbindings.PreparedOperationDescriptor
}

// ContractEvidence explains an exact or directional compatibility decision.
type ContractEvidence struct {
	Verdict ContractVerdict              `json:"verdict"`
	Method  string                       `json:"method"`
	Issues  []compare.CompatibilityIssue `json:"issues"`
	Detail  string                       `json:"detail,omitempty"`
}

// ProviderPolicyCandidate is application-owned provider ranking input.
type ProviderPolicyCandidate struct {
	ProviderKey string  `json:"providerKey"`
	Preference  float64 `json:"preference"`
}

// ProviderPolicySelection is the provider-election result.
type ProviderPolicySelection struct {
	Status    string
	Provider  *ProviderPolicyCandidate
	Ambiguous []ProviderPolicyCandidate
}

// RealizationPolicySelection is the within-provider binding election result.
type RealizationPolicySelection struct {
	Status      string
	Realization *ProviderRealizationDescriptor
	Ambiguous   []ProviderRealizationDescriptor
	Detail      string
}

// CompositionPolicy keeps correspondence, compatibility, provider election,
// and realization election explicit and replaceable.
type CompositionPolicy interface {
	ID() string
	// ProviderInspectionGroups orders provider election tiers. Resolution
	// advances only when the current tier contains no eligible provider;
	// exhaustive inspection deliberately ignores this plan.
	ProviderInspectionGroups([]ProviderPolicyCandidate) [][]ProviderPolicyCandidate
	Correspondences(openbindings.PreparedOperationDescriptor, *openbindings.PreparedInterface) []OperationCorrespondence
	AssessContract(context.Context, *openbindings.PreparedInterface, OperationCorrespondence, *openbindings.PreparedInterface) (ContractEvidence, error)
	SelectProvider([]ProviderPolicyCandidate) ProviderPolicySelection
	SelectRealization([]ProviderRealizationDescriptor, RealizationSelector) RealizationPolicySelection
}

type referenceCompositionPolicy struct{}

// ReferenceCompositionPolicy implements the explicit 0.2 SDK convention; it
// is not a Core OBI semantic.
var ReferenceCompositionPolicy CompositionPolicy = referenceCompositionPolicy{}

func (referenceCompositionPolicy) ID() string { return ReferenceCompositionPolicyID }

func (referenceCompositionPolicy) ProviderInspectionGroups(
	candidates []ProviderPolicyCandidate,
) [][]ProviderPolicyCandidate {
	ordered := append([]ProviderPolicyCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Preference != ordered[j].Preference {
			return ordered[i].Preference > ordered[j].Preference
		}
		return ordered[i].ProviderKey < ordered[j].ProviderKey
	})
	groups := make([][]ProviderPolicyCandidate, 0)
	for _, candidate := range ordered {
		if len(groups) == 0 || groups[len(groups)-1][0].Preference != candidate.Preference {
			groups = append(groups, []ProviderPolicyCandidate{candidate})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], candidate)
		}
	}
	return groups
}

func (referenceCompositionPolicy) Correspondences(
	required openbindings.PreparedOperationDescriptor,
	provider *openbindings.PreparedInterface,
) []OperationCorrespondence {
	byCanonical := make(map[string]OperationCorrespondence)
	for _, identifier := range required.Identifiers {
		offered, ok := provider.Operation(identifier)
		if !ok {
			continue
		}
		if _, exists := byCanonical[offered.CanonicalKey]; exists {
			continue
		}
		byCanonical[offered.CanonicalKey] = OperationCorrespondence{
			Identifier: identifier,
			Required:   required,
			Provider:   offered,
		}
	}
	result := make([]OperationCorrespondence, 0, len(byCanonical))
	for _, correspondence := range byCanonical {
		result = append(result, correspondence)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider.CanonicalKey != result[j].Provider.CanonicalKey {
			return result[i].Provider.CanonicalKey < result[j].Provider.CanonicalKey
		}
		return result[i].Identifier < result[j].Identifier
	})
	return result
}

func (referenceCompositionPolicy) AssessContract(
	ctx context.Context,
	required *openbindings.PreparedInterface,
	correspondence OperationCorrespondence,
	provider *openbindings.PreparedInterface,
) (ContractEvidence, error) {
	if err := ctx.Err(); err != nil {
		return ContractEvidence{}, err
	}
	requiredContract, found, err := required.BoundaryContract(correspondence.Required.CanonicalKey)
	if err != nil || !found {
		return ContractEvidence{}, fmt.Errorf("openbindings: required boundary contract: %w", err)
	}
	providerContract, found, err := provider.BoundaryContract(correspondence.Provider.CanonicalKey)
	if err != nil || !found {
		return ContractEvidence{}, fmt.Errorf("openbindings: provider boundary contract: %w", err)
	}
	if requiredContract.Complete && providerContract.Complete &&
		requiredContract.Revision == providerContract.Revision &&
		bytes.Equal(requiredContract.Canonical, providerContract.Canonical) {
		return ContractEvidence{Verdict: ContractCompatible, Method: "exact", Issues: []compare.CompatibilityIssue{}}, nil
	}
	if err := ctx.Err(); err != nil {
		return ContractEvidence{}, err
	}
	issues, err := compare.CheckOperationCompatibility(
		required.InterfaceSnapshot(),
		correspondence.Identifier,
		provider.InterfaceSnapshot(),
	)
	if err != nil {
		return ContractEvidence{}, err
	}
	for _, issue := range issues {
		if strings.Contains(issue.Detail, "schema check failed:") {
			return ContractEvidence{
				Verdict: ContractIndeterminate,
				Method:  "directional-profile",
				Issues:  issues,
				Detail:  "the directional schema profile could not decide this contract",
			}, nil
		}
	}
	verdict := ContractCompatible
	if len(issues) > 0 {
		verdict = ContractIncompatible
	}
	return ContractEvidence{Verdict: verdict, Method: "directional-profile", Issues: issues}, nil
}

func (referenceCompositionPolicy) SelectProvider(candidates []ProviderPolicyCandidate) ProviderPolicySelection {
	if len(candidates) == 0 {
		return ProviderPolicySelection{Status: "unavailable"}
	}
	highest := math.Inf(-1)
	for _, candidate := range candidates {
		if math.IsNaN(candidate.Preference) || math.IsInf(candidate.Preference, 0) {
			panic(fmt.Sprintf("openbindings: provider %q preference must be finite", candidate.ProviderKey))
		}
		if candidate.Preference > highest {
			highest = candidate.Preference
		}
	}
	preferred := make([]ProviderPolicyCandidate, 0)
	for _, candidate := range candidates {
		if candidate.Preference == highest {
			preferred = append(preferred, candidate)
		}
	}
	sort.Slice(preferred, func(i, j int) bool { return preferred[i].ProviderKey < preferred[j].ProviderKey })
	if len(preferred) != 1 {
		return ProviderPolicySelection{Status: "ambiguous", Ambiguous: preferred}
	}
	selected := preferred[0]
	return ProviderPolicySelection{Status: "selected", Provider: &selected}
}

func (referenceCompositionPolicy) SelectRealization(
	candidates []ProviderRealizationDescriptor,
	selector RealizationSelector,
) RealizationPolicySelection {
	ordered := append([]ProviderRealizationDescriptor(nil), candidates...)
	sortProviderRealizations(ordered)
	if selector != nil {
		key, ok := selector(append([]ProviderRealizationDescriptor(nil), ordered...))
		if !ok {
			return RealizationPolicySelection{Status: "unavailable", Detail: "the provider realization selector declined every realization"}
		}
		for _, candidate := range ordered {
			if candidate.BindingKey == key {
				selected := candidate
				return RealizationPolicySelection{Status: "selected", Realization: &selected}
			}
		}
		return RealizationPolicySelection{Status: "unavailable", Detail: fmt.Sprintf("the provider realization selector returned unknown binding %q", key)}
	}
	if len(ordered) == 0 {
		return RealizationPolicySelection{Status: "unavailable"}
	}
	if len(ordered) != 1 {
		return RealizationPolicySelection{Status: "ambiguous", Ambiguous: ordered}
	}
	selected := ordered[0]
	return RealizationPolicySelection{Status: "selected", Realization: &selected}
}
