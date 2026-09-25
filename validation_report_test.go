package openbindings

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestValidateOperationInputDistinguishesGraphUnavailable(t *testing.T) {
	err := ValidateOperationInput("value", documentWithInput(map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"$ref": "https://example.invalid/missing.json"},
		},
	}, nil), "op")
	var unavailable *SchemaGraphUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected SchemaGraphUnavailableError, got %T: %v", err, err)
	}

	err = ValidateOperationInput(42, documentWithInput(map[string]any{"type": "string"}, nil), "op")
	if err == nil {
		t.Fatal("expected instance mismatch")
	}
	if errors.As(err, &unavailable) {
		t.Fatalf("instance mismatch classified as unavailable: %v", err)
	}
}

func TestConcludeConformance(t *testing.T) {
	tests := []struct {
		name     string
		evidence map[string]RuleEvidenceStatus
		want     ValidationReport
	}{
		{
			name: "complete success",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-02": EvidenceSatisfied,
				"OBI-D-14": EvidenceNotApplicable,
			},
			want: ValidationReport{Conclusion: ConclusionConformant},
		},
		{
			name: "incomplete without violation",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-02": EvidenceSatisfied,
				"OBI-D-10": EvidenceInconclusive,
			},
			want: ValidationReport{
				Conclusion:   ConclusionConformanceUndetermined,
				Inconclusive: []string{"OBI-D-10"},
			},
		},
		{
			name: "violation is decisive and incompleteness is retained",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-13": EvidenceInconclusive,
				"OBI-D-03": EvidenceViolated,
				"OBI-D-02": EvidenceViolated,
			},
			want: ValidationReport{
				Conclusion:   ConclusionNonConformant,
				Violated:     []string{"OBI-D-02", "OBI-D-03"},
				Inconclusive: []string{"OBI-D-13"},
			},
		},
		{
			name: "unknown runtime status cannot produce conformant",
			evidence: map[string]RuleEvidenceStatus{
				"OBI-D-02": RuleEvidenceStatus("misspelled"),
			},
			want: ValidationReport{
				Conclusion:   ConclusionConformanceUndetermined,
				Inconclusive: []string{"OBI-D-02"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The report carries the evidence it concluded from.
			tt.want.Evidence = tt.evidence
			if got := ConcludeConformance(tt.evidence); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ConcludeConformance() = %#v; want %#v", got, tt.want)
			}
		})
	}
}

// A report names the specification version its rule identifiers belong to:
// the version the document declares when this SDK supports it, and otherwise
// AuthoringVersion, whose rules judge a document declaring no version it can
// interpret. A report concluded from evidence alone names none.
func TestValidationReport_NamesTheVersionItsRulesBelongTo(t *testing.T) {
	cases := map[string]string{
		`{"openbindings":"0.2.0","operations":{}}`:         "0.2.0",
		`{"openbindings":"0.2.7+build.1","operations":{}}`: "0.2.7+build.1",
		`{"openbindings":"two","operations":{}}`:           AuthoringVersion,
		`{"operations":{}}`:                                AuthoringVersion,
		`not json`:                                         AuthoringVersion,
	}
	for document, want := range cases {
		_, report, _ := ValidateDocument([]byte(document), ValidateOptions{})
		if report.Version != want {
			t.Errorf("%s: Version = %q, want %q", document, report.Version, want)
		}
		var iface Interface
		if json.Unmarshal([]byte(document), &iface) == nil {
			if host, _ := iface.Validate(ValidateOptions{}); host.Version != want {
				t.Errorf("%s: host Version = %q, want %q", document, host.Version, want)
			}
		}
	}
	if got := ConcludeConformance(map[string]RuleEvidenceStatus{"OBI-D-01": EvidenceSatisfied}).Version; got != "" {
		t.Errorf("ConcludeConformance Version = %q, want empty", got)
	}
}
