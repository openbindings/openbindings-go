package schemaprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Additional official SDK qualification, not universal Core conformance.
// The comparison corpus supplies exact documents and authored point witnesses:
// values each side of a case accepts or refuses. These assertions check that
// the core SDK's validation of values against an operation's contract
// (OBI-T-16) agrees with them.
func TestOfficialSDKQualification_OperationContractWitnesses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(comparisonCorpusDir(t), "comparison", "exact-values.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scope, Profile string
		Cases          []struct {
			ID, Direction, LeftJSON, RightJSON string
			Witness                            *struct {
				InstanceJSON                string
				TargetValid, CandidateValid bool
			}
		}
	}
	if err = json.Unmarshal(raw, &pack); err != nil {
		t.Fatal(err)
	}
	if pack.Scope != "comparison-profile" || pack.Profile != "OB-2020-12" || len(pack.Cases) == 0 {
		t.Fatal("invalid fixture pack")
	}
	for _, c := range pack.Cases {
		// Corpus well-formedness is separate from the stronger snapshot assertions.
		t.Run("fixture-validity/"+c.ID, func(t *testing.T) {
			for _, raw := range []string{c.LeftJSON, c.RightJSON} {
				if _, _, err := openbindings.ValidateDocument([]byte(raw), openbindings.ValidateOptions{}); err != nil {
					t.Fatalf("non-conformant fixture document: %v", err)
				}
			}
		})
		if c.Witness == nil {
			continue
		}
		for _, side := range []struct {
			name, raw string
			want      bool
		}{{"target", c.LeftJSON, c.Witness.TargetValid}, {"candidate", c.RightJSON, c.Witness.CandidateValid}} {
			t.Run(c.ID+"/"+side.name, func(t *testing.T) {
				var iface openbindings.Interface
				if err := json.Unmarshal([]byte(side.raw), &iface); err != nil {
					t.Fatal(err)
				}
				decoder := json.NewDecoder(strings.NewReader(c.Witness.InstanceJSON))
				decoder.UseNumber()
				var sample any
				if err := decoder.Decode(&sample); err != nil {
					t.Fatal(err)
				}
				compiled, err := openbindings.CompileOperationSchema(&iface, "test", c.Direction)
				if err != nil {
					t.Fatal(err)
				}
				if got := compiled.Validate(sample) == nil; got != side.want {
					t.Fatalf("witness: got %v want %v", got, side.want)
				}
			})
		}
	}
}
