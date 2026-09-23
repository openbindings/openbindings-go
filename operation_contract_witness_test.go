package openbindings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// Additional official SDK qualification, not universal Core conformance.
// The comparison corpus supplies exact documents and authored point witnesses;
// these assertions check operation-contract validation (OBI-T-16) against them.
func TestOfficialSDKQualification_OperationContractWitnesses(t *testing.T) {
	dir := os.Getenv("OB_INTERFACES_CORPUS")
	if dir == "" {
		dir = filepath.Join("..", "interfaces", "conformance")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "comparison", "exact-values.json"))
	if err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip("interfaces corpus unavailable")
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
				if _, _, err := ValidateDocument([]byte(raw)); err != nil {
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
				var iface Interface
				if err := jsonvalue.Unmarshal([]byte(side.raw), &iface); err != nil {
					t.Fatal(err)
				}
				var sample any
				if err := jsonvalue.Unmarshal([]byte(c.Witness.InstanceJSON), &sample); err != nil {
					t.Fatal(err)
				}
				compiled, err := CompileOperationSchema(&iface, "test", c.Direction)
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
