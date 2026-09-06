package invoke

import "testing"

type invalidElectionPolicy struct {
	CompositionPolicy
	selection RealizationPolicySelection
}

func (p invalidElectionPolicy) SelectRealization([]ProviderRealizationDescriptor, RealizationSelector) RealizationPolicySelection {
	return p.selection
}

func TestCompositionRejectsInvalidRealizationPolicyResult(t *testing.T) {
	for _, result := range []RealizationPolicySelection{
		{Status: "selected"},
		{Status: "selected", Realization: &ProviderRealizationDescriptor{BindingKey: "unknown"}},
		{Status: "invented"},
		{Status: "ambiguous", Ambiguous: []ProviderRealizationDescriptor{{BindingKey: "unknown"}}},
	} {
		provider := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "provider")
		session, err := NewCompositionSession(CompositionSessionOptions{
			Consumer:  compositionConsumer(t, map[string]any{"type": "string"}),
			Providers: []ProviderRegistration{{Provider: provider}},
			Policy:    invalidElectionPolicy{ReferenceCompositionPolicy, result},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = session.resolve(t.Context(), "delivery"); err == nil {
			t.Fatalf("accepted invalid policy result: %#v", result)
		}
	}
}
