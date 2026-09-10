package openbindings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openbindings/jsonata/go/syntax"
)

// Historical expressions remain syntax regression controls, not a normative
// executable oracle. Use exactly the syntax-only surface used by validation;
// runtime initialization or numeric work limits must not decide OBI validity.
// Evaluation qualification belongs to the separately selected adapter.

type transformGateCase struct {
	ID   string `json:"id"`
	Expr string `json:"expr"`
}

type transformGateFile struct {
	Cases []transformGateCase `json:"cases"`
}

func TestTransformRejectsUndocumentedPrefix(t *testing.T) {
	for _, expression := range []string{`~$|{"x":1}|`, `{} ~> ~$|{"x":1}|`} {
		if jsonataParses(expression) {
			t.Errorf("OBI-D-18 syntax validator admitted %s", expression)
		}
	}
}

// transformAgreeDir locates spec/conformance/transforms/agree via
// OB_SPEC_CORPUS (the env the other corpus-backed tests read), appending the
// transforms subpath when the env points at the conformance root. It skips
// when the corpus is absent, UNLESS OB_CORPUS_REQUIRED is set — the CI-armed
// required-mode flag — in which case an absent corpus is a hard failure. The
// flag is read directly so this composes with it whenever it lands.
func transformAgreeDir(t *testing.T) string {
	t.Helper()
	required := os.Getenv("OB_CORPUS_REQUIRED") != ""
	absent := func(msg string) string {
		if required {
			t.Fatalf("OB_CORPUS_REQUIRED set but %s", msg)
		}
		t.Skipf("%s; skipping transform compile-lane gate", msg)
		return ""
	}
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		return absent("OB_SPEC_CORPUS not set")
	}
	base := root
	if filepath.Base(root) != "transforms" {
		base = filepath.Join(root, "transforms")
	}
	agree := filepath.Join(base, "agree")
	if st, err := os.Stat(agree); err != nil || !st.IsDir() {
		return absent("transforms agree corpus not found at " + agree)
	}
	return agree
}

// TestTransformCompileGate runs every agree-corpus expression through
// syntax.Validate — the exact surface validate.go ships for OBI-D-18 — and
// asserts acceptance.
func TestTransformCompileGate(t *testing.T) {
	dir := transformAgreeDir(t)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var gf transformGateFile
		if err := json.Unmarshal(b, &gf); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, c := range gf.Cases {
			total++
			t.Run(c.ID, func(t *testing.T) {
				if err := syntax.Validate(c.Expr); err != nil {
					t.Fatalf("syntax.Validate rejected historical expr %q: %v", c.Expr, err)
				}
			})
		}
	}
	if total == 0 {
		t.Fatalf("no agree cases loaded from %s", dir)
	}
	t.Logf("transform syntax regression: %d historical expressions accepted", total)
}
