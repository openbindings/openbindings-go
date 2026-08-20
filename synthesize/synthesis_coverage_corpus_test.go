package synthesize

// Conformance corpus adapter for the format-neutral invariants of the
// interface-synthesizer coverage contract. Binding-family interaction
// inventories remain in the spec repository's synthesis scenarios.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

type synthesisCoverageCorpus struct {
	Tests []synthesisCoverageCase `json:"tests"`
}

type synthesisCoverageCase struct {
	Description string                   `json:"description"`
	Interface   json.RawMessage          `json:"interface"`
	Entries     []SynthesisCoverageEntry `json:"entries"`
	Exhaustive  bool                     `json:"exhaustive"`
	Expected    struct {
		Valid            bool   `json:"valid"`
		FullyRepresented bool   `json:"fullyRepresented"`
		ErrorContains    string `json:"errorContains"`
	} `json:"expected"`
}

func TestSynthesisCoverageCorpus(t *testing.T) {
	path := filepath.Join(selectionCorpusDir(t), "synthesis-coverage", "cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read synthesis coverage corpus: %v", err)
	}
	var corpus synthesisCoverageCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse synthesis coverage corpus: %v", err)
	}
	if len(corpus.Tests) == 0 {
		t.Fatal("synthesis coverage corpus contains no cases")
	}

	for _, tc := range corpus.Tests {
		tc := tc
		t.Run(tc.Description, func(t *testing.T) {
			iface, err := openbindings.ValidateDocument(tc.Interface)
			if err != nil {
				t.Fatalf("fixture interface does not validate: %v", err)
			}
			result, err := NewSynthesisResult(iface, tc.Entries, tc.Exhaustive)
			if !tc.Expected.Valid {
				if err == nil {
					t.Fatal("expected coverage validation error")
				}
				if !strings.Contains(err.Error(), tc.Expected.ErrorContains) {
					t.Fatalf("error %q does not contain %q", err, tc.Expected.ErrorContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected valid coverage: %v", err)
			}
			if result.Coverage.FullyRepresented != tc.Expected.FullyRepresented {
				t.Fatalf("fullyRepresented = %v, want %v", result.Coverage.FullyRepresented, tc.Expected.FullyRepresented)
			}
		})
	}
}
