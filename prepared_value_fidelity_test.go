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
// these assertions concern the SDK's detached snapshot/validation boundary.
func TestOfficialSDKQualification_ExactSnapshots(t *testing.T) {
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
				if _, err := ValidateDocument([]byte(raw)); err != nil {
					t.Fatalf("invalid fixture document: %v", err)
				}
			}
		})
		// Exercise the current fast-path evidence seam, not profile-normalized
		// equality. The implementation migration must replace this adapter too.
		identities := map[string]bool{
			"structural-wide-difference":     false,
			"structural-equivalent-spelling": true,
			"union-permutation":              false, // authored array order matters
			"object-member-order":            true,
		}
		if want, selected := identities[c.ID]; selected {
			t.Run("authored-identity/"+c.ID, func(t *testing.T) {
				boundary := func(raw string) PreparedBoundaryContract {
					var iface Interface
					if err := jsonvalue.Unmarshal([]byte(raw), &iface); err != nil {
						t.Fatal(err)
					}
					p, err := PrepareInterface(&iface)
					if err != nil {
						t.Fatal(err)
					}
					before, _, err := p.SchemaValidator("test", c.Direction)
					if err != nil {
						t.Fatal(err)
					}
					b, found, err := p.BoundaryContract("test")
					if err != nil || !found || !b.Complete {
						t.Fatalf("complete boundary unavailable: %v", err)
					}
					if after, _, err := p.SchemaValidator("test", c.Direction); err != nil || before != after {
						t.Fatalf("identity inspection invalidated owner-local compiler cache: %v", err)
					}
					return b
				}
				a, b := boundary(c.LeftJSON), boundary(c.RightJSON)
				identity, err := CompareBoundaryContracts(a, b)
				if err != nil {
					t.Fatal(err)
				}
				if got := identity == "equal"; got != want {
					t.Errorf("authored boundary identity: got %v want %v", got, want)
				}
			})
		}
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
				direct, err := CompileOperationSchema(&iface, "test", c.Direction)
				if err != nil {
					t.Fatal(err)
				}
				if got := direct.Validate(sample) == nil; got != side.want {
					t.Fatalf("direct witness: got %v want %v", got, side.want)
				}
				p, err := PrepareInterface(&iface)
				if err != nil {
					t.Fatalf("exact preparation unavailable: %v", err)
				}
				v, found, err := p.SchemaValidator("test", c.Direction)
				if err != nil || !found {
					t.Fatalf("prepared validator unavailable: %v", err)
				}
				if got := v.Validate(sample) == nil; got != side.want {
					t.Errorf("snapshot changed witness validation: got %v want %v", got, side.want)
				}
			})
		}
	}
}
