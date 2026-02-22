package schemaprofile

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// conformanceCaseFile matches the spec's conformance JSON structure.
type conformanceCaseFile struct {
	Cases []json.RawMessage `json:"cases"`
}

type schemaComparisonCase struct {
	Comment    string         `json:"$comment,omitempty"`
	Name       string         `json:"name"`
	Direction  string         `json:"direction"`
	Target     map[string]any `json:"target"`
	Candidate  map[string]any `json:"candidate"`
	Compatible *bool          `json:"compatible,omitempty"`
	Error      string         `json:"error,omitempty"`
}

type normalizationCase struct {
	Comment  string         `json:"$comment,omitempty"`
	Name     string         `json:"name"`
	Input    map[string]any `json:"input"`
	Expected map[string]any `json:"expected,omitempty"`
	Error    string         `json:"error,omitempty"`
}

const conformanceDir = "../../../spec/conformance/"

func TestConformance_SchemaComparison(t *testing.T) {
	data, err := os.ReadFile(conformanceDir + "schema-comparison.json")
	if err != nil {
		t.Fatalf("read schema-comparison.json: %v", err)
	}
	var f conformanceCaseFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var ran int
	for _, raw := range f.Cases {
		var c schemaComparisonCase
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("unmarshal case: %v", err)
		}
		if c.Name == "" {
			continue // comment-only entries
		}

		t.Run(c.Name, func(t *testing.T) {
			n := &Normalizer{Root: map[string]any{}}
			var (
				ok      bool
				compErr error
			)
			switch c.Direction {
			case "input":
				ok, compErr = n.InputCompatible(c.Target, c.Candidate)
			case "output":
				ok, compErr = n.OutputCompatible(c.Target, c.Candidate)
			default:
				t.Fatalf("unknown direction %q", c.Direction)
			}

			if c.Error != "" {
				if compErr == nil {
					t.Fatalf("expected error %q, got compatible=%v", c.Error, ok)
				}
				switch c.Error {
				case "outside_profile":
					var ope *OutsideProfileError
					if !errors.As(compErr, &ope) {
						t.Fatalf("expected OutsideProfileError, got %T: %v", compErr, compErr)
					}
				case "ref_cycle":
					var re *RefError
					if !errors.As(compErr, &re) {
						t.Fatalf("expected RefError (cycle), got %T: %v", compErr, compErr)
					}
				case "schema_error":
					// Any error is acceptable for schema_error
				default:
					t.Fatalf("unknown error category %q", c.Error)
				}
				return
			}

			if compErr != nil {
				t.Fatalf("unexpected error: %v", compErr)
			}
			if c.Compatible == nil {
				t.Fatalf("missing compatible field")
			}
			if ok != *c.Compatible {
				t.Fatalf("expected compatible=%v, got %v", *c.Compatible, ok)
			}
		})
		ran++
	}
	t.Logf("ran %d schema comparison cases", ran)
}

func TestConformance_Normalization(t *testing.T) {
	data, err := os.ReadFile(conformanceDir + "normalization.json")
	if err != nil {
		t.Fatalf("read normalization.json: %v", err)
	}
	var f conformanceCaseFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var ran int
	for _, raw := range f.Cases {
		var c normalizationCase
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("unmarshal case: %v", err)
		}
		if c.Name == "" {
			continue // comment-only entries
		}

		t.Run(c.Name, func(t *testing.T) {
			n := &Normalizer{Root: c.Input}
			result, normErr := n.Normalize(c.Input)

			if c.Error != "" {
				if normErr == nil {
					t.Fatalf("expected error %q, got result %v", c.Error, result)
				}
				switch c.Error {
				case "outside_profile":
					var ope *OutsideProfileError
					if !errors.As(normErr, &ope) {
						t.Fatalf("expected OutsideProfileError, got %T: %v", normErr, normErr)
					}
				case "ref_cycle":
					var re *RefError
					if !errors.As(normErr, &re) {
						t.Fatalf("expected RefError (cycle), got %T: %v", normErr, normErr)
					}
				case "schema_error":
					// Any error is acceptable
				default:
					t.Fatalf("unknown error category %q", c.Error)
				}
				return
			}

			if normErr != nil {
				t.Fatalf("unexpected error: %v", normErr)
			}

			// Compare result to expected via canonical JSON.
			gotJSON, err := CanonicalString(result)
			if err != nil {
				t.Fatalf("canonical result: %v", err)
			}
			wantJSON, err := CanonicalString(c.Expected)
			if err != nil {
				t.Fatalf("canonical expected: %v", err)
			}
			if gotJSON != wantJSON {
				t.Fatalf("normalization mismatch:\n  got:  %s\n  want: %s", gotJSON, wantJSON)
			}
		})
		ran++
	}
	t.Logf("ran %d normalization cases", ran)
}
