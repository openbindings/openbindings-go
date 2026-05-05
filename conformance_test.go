package openbindings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type conformanceFixture struct {
	Rule        string            `json:"rule"`
	Section     string            `json:"section"`
	Description string            `json:"description"`
	Tests       []conformanceTest `json:"tests"`
}

type conformanceTest struct {
	Description       string          `json:"description"`
	Document          json.RawMessage `json:"document"`
	Valid             bool            `json:"valid"`
	Violates          []string        `json:"violates,omitempty"`
	RequiresMaxTested string          `json:"requiresMaxTested,omitempty"`
}

func TestConformanceCorpus(t *testing.T) {
	corpusDir := findConformanceCorpus()

	if corpusDir == "" {
		t.Skip("spec conformance corpus not found")
	}

	for _, subdir := range []string{"document", "tool"} {
		t.Run(subdir, func(t *testing.T) {
			runConformanceDir(t, filepath.Join(corpusDir, subdir))
		})
	}
}

func findConformanceCorpus() string {
	for _, candidate := range []string{
		filepath.Join("..", "spec", "conformance"),
		filepath.Join("spec", "conformance"),
	} {
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
	}
	return ""
}

func runConformanceDir(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}

	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		var fix conformanceFixture
		if err := json.Unmarshal(data, &fix); err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}

		for _, tt := range fix.Tests {
			tt := tt
			name := fix.Rule + "/" + tt.Description
			t.Run(name, func(t *testing.T) {
				if tt.RequiresMaxTested != "" {
					higher, err := IsHigherMajorOrPre1MinorThanMaxTested(tt.RequiresMaxTested)
					if err == nil && higher {
						t.Skip("requires MaxTested >=", tt.RequiresMaxTested)
					}
				}

				iface, parseErr := ParseDocument(tt.Document)
				var validateErr error
				if parseErr == nil {
					// Enable all opt-in validation so the conformance corpus can
					// exercise checks like OBI-D-15 (example schema validation).
					validateErr = iface.ValidateInterface(WithExampleValidation())
				}
				actualValid := parseErr == nil && validateErr == nil

				if actualValid != tt.Valid {
					if tt.Valid {
						if parseErr != nil {
							t.Errorf("expected valid, got parse error: %v", parseErr)
						} else {
							t.Errorf("expected valid, got validate error: %v", validateErr)
						}
					} else {
						t.Errorf("expected invalid, but SDK accepted the document")
					}
				}
			})
		}
	}
}
