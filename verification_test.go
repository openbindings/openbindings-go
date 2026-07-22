package openbindings

import (
	"errors"
	"reflect"
	"testing"
)

func TestValidateAgainstSchemaDistinguishesGraphUnavailable(t *testing.T) {
	err := ValidateAgainstSchema("value", map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"$ref": "https://example.invalid/missing.json"},
		},
	}, nil)
	var unavailable *SchemaGraphUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected SchemaGraphUnavailableError, got %T: %v", err, err)
	}

	err = ValidateAgainstSchema(42, map[string]any{"type": "string"}, nil)
	if err == nil {
		t.Fatal("expected instance mismatch")
	}
	if errors.As(err, &unavailable) {
		t.Fatalf("instance mismatch classified as unavailable: %v", err)
	}
}

func TestConcludeVerification(t *testing.T) {
	tests := []struct {
		name     string
		evidence map[string]RuleEvidenceStatus
		want     VerificationReport
	}{
		{
			name: "complete success",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-02": EvidenceSatisfied,
				"OBI-D-13": EvidenceNotApplicable,
			},
			want: VerificationReport{Conclusion: ConclusionConformant},
		},
		{
			name: "incomplete without violation",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-02": EvidenceSatisfied,
				"OBI-D-11": EvidenceUnverified,
			},
			want: VerificationReport{
				Conclusion: ConclusionConformanceUndetermined,
				Unverified: []string{"OBI-D-11"},
			},
		},
		{
			name: "violation is decisive and incompleteness is retained",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-17": EvidenceUnverified,
				"OBI-D-03": EvidenceViolated,
				"OBI-D-02": EvidenceViolated,
			},
			want: VerificationReport{
				Conclusion: ConclusionNonConformant,
				Violated:   []string{"OBI-D-02", "OBI-D-03"},
				Unverified: []string{"OBI-D-17"},
			},
		},
		{
			name: "unknown runtime status cannot produce conformant",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-02": RuleEvidenceStatus("misspelled"),
			},
			want: VerificationReport{
				Conclusion: ConclusionConformanceUndetermined,
				Unverified: []string{"OBI-D-02"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConcludeVerification(tt.evidence); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ConcludeVerification() = %#v; want %#v", got, tt.want)
			}
		})
	}
}
