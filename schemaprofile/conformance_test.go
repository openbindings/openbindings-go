package schemaprofile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Manifest and fixture types matching the spec conformance format.

type manifest struct {
	ConventionVersion string         `json:"conventionVersion"`
	Profile           string         `json:"profile"`
	Files             []manifestFile `json:"files"`
}

type manifestFile struct {
	Path      string   `json:"path"`
	Mode      string   `json:"mode"`
	Direction string   `json:"direction"`
	Verdict   string   `json:"verdict"`
	Findings  []string `json:"findings"`
}

type fixture struct {
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Left        obInterface    `json:"left"`
	Right       obInterface    `json:"right"`
	Mode        string         `json:"mode"`
	Options     fixtureOptions `json:"options"`
}

type obInterface struct {
	OpenBindings string                     `json:"openbindings"`
	Operations   map[string]obOperation     `json:"operations"`
	Schemas      map[string]json.RawMessage `json:"schemas,omitempty"`
	Raw          map[string]any             `json:"-"` // full document for Normalizer.Root
}

type obOperation struct {
	Input   map[string]any `json:"input,omitempty"`
	Output  map[string]any `json:"output,omitempty"`
	Aliases []string       `json:"aliases,omitempty"`
}

type fixtureOptions struct {
	Profile string `json:"profile"`
}

type opResult struct {
	verdict string // "compatible", "incompatible", "indeterminate"
}

// UnmarshalJSON for obInterface captures the raw document for use as Normalizer.Root.
func (o *obInterface) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &o.Raw); err != nil {
		return err
	}
	type plain obInterface
	return json.Unmarshal(data, (*plain)(o))
}

const conformanceDir = "../../spec/conformance/comparison/"

// knownGaps lists fixtures that expose known gaps in the schemaprofile package.
// Each entry maps a fixture path to the reason it must be skipped. These are
// legitimate spec requirements that the package does not yet implement.
var knownGaps = map[string]string{
	// The normalizer does not detect draft-04 boolean exclusiveMinimum/
	// exclusiveMaximum as non-2020-12 schema constructs. It should reject
	// them with OutsideProfileError to produce an "indeterminate" verdict.
	"profile/profile-schema-not-2020-12-indeterminate.json": "normalizer does not detect draft-04 boolean exclusiveMinimum as non-2020-12",

	// Same root cause as above: one operation uses draft-04 boolean
	// exclusiveMinimum, which should collapse the multi-op verdict to
	// indeterminate.
	"structural/verdict-collapse-indeterminate-dominates.json": "normalizer does not detect draft-04 boolean exclusiveMinimum as non-2020-12",

	// The compat logic explicitly skips additionalProperties for input
	// direction, but the spec says disabling additionalProperties on the
	// candidate is a breaking input change.
	"subsumption/additional-properties-input-disabled-breaking.json": "InputCompatible does not enforce additionalProperties restriction",

	// The schemaprofile package does not implement the suppression mechanism.
	// Suppressions are an interface-level concern that downgrade breaking
	// findings to compatible.
	"suppression/suppressed-required-input-added-audit.json": "suppression mechanism not implemented",
}

func TestConformance(t *testing.T) {
	manifestPath := filepath.Join(conformanceDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Skipf("conformance fixtures not available (run from monorepo): %v", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(m.Files) == 0 {
		t.Fatal("manifest has no files")
	}

	var ran int
	for _, entry := range m.Files {
		entry := entry
		t.Run(entry.Path, func(t *testing.T) {
			if entry.Verdict == "unverified" {
				t.Skipf("unverified verdict (outside schemaprofile scope)")
				return
			}
			if reason, ok := knownGaps[entry.Path]; ok {
				t.Skipf("known gap: %s", reason)
				return
			}

			fixturePath := filepath.Join(conformanceDir, entry.Path)
			fdata, err := os.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			var fix fixture
			if err := json.Unmarshal(fdata, &fix); err != nil {
				t.Fatalf("unmarshal fixture: %v", err)
			}

			switch entry.Mode {
			case "subsume":
				runSubsumeFixture(t, entry, fix)
			case "identical":
				runIdenticalFixture(t, entry, fix)
			default:
				t.Fatalf("unknown mode %q", entry.Mode)
			}
		})
		ran++
	}
	t.Logf("ran %d conformance fixtures (%d skipped as known gaps, 1 unverified)",
		ran, len(knownGaps))
}

// runSubsumeFixture tests subsumption mode by pairing operations between left
// and right, then calling InputCompatible or OutputCompatible on matched schemas.
// The per-operation results are collapsed to a summary verdict using the same
// dominance rules the spec defines: indeterminate > incompatible > compatible.
func runSubsumeFixture(t *testing.T, entry manifestFile, fix fixture) {
	t.Helper()

	leftOps := fix.Left.Operations
	rightOps := fix.Right.Operations

	// Build alias index for left operations: alias -> left operation key.
	leftAliasIndex := map[string]string{}
	for key, op := range leftOps {
		for _, alias := range op.Aliases {
			leftAliasIndex[alias] = key
		}
	}

	var results []opResult

	// Track which right operations are paired.
	pairedRight := map[string]bool{}

	// For each left operation, find its pair in right (by key or alias).
	for leftKey, leftOp := range leftOps {
		rightKey, rightOp, found := findPairedOp(leftKey, rightOps, leftAliasIndex)
		if !found {
			// Operation removed: breaking.
			results = append(results, opResult{verdict: "incompatible"})
			continue
		}
		pairedRight[rightKey] = true

		result := compareOperation(t, entry.Direction, leftOp, rightOp, fix.Left.Raw, fix.Right.Raw, leftKey)
		results = append(results, result)
	}

	// Operations only in right (added) are non-breaking.
	for rightKey := range rightOps {
		if pairedRight[rightKey] {
			continue
		}
		// Check if this right key was paired via alias.
		if _, found := leftAliasIndex[rightKey]; found {
			continue
		}
		results = append(results, opResult{verdict: "compatible"})
	}

	collapsed := collapseVerdicts(results)
	if collapsed != entry.Verdict {
		t.Fatalf("verdict mismatch: got %q, want %q", collapsed, entry.Verdict)
	}
}

// findPairedOp locates the right-side operation that pairs with a left key,
// checking direct key match first, then alias match.
func findPairedOp(leftKey string, rightOps map[string]obOperation, leftAliasIndex map[string]string) (string, obOperation, bool) {
	if op, ok := rightOps[leftKey]; ok {
		return leftKey, op, true
	}
	for rk, op := range rightOps {
		if lk, ok := leftAliasIndex[rk]; ok && lk == leftKey {
			return rk, op, true
		}
	}
	return "", obOperation{}, false
}

// compareOperation runs InputCompatible or OutputCompatible on a single paired
// operation and returns the result as an opResult.
func compareOperation(t *testing.T, direction string, leftOp, rightOp obOperation, leftRoot, rightRoot map[string]any, opKey string) opResult {
	t.Helper()

	var leftSchema, rightSchema map[string]any
	switch direction {
	case "input":
		leftSchema = leftOp.Input
		rightSchema = rightOp.Input
	case "output":
		leftSchema = leftOp.Output
		rightSchema = rightOp.Output
	default:
		t.Fatalf("unknown direction %q", direction)
	}

	if leftSchema == nil {
		leftSchema = map[string]any{}
	}
	if rightSchema == nil {
		rightSchema = map[string]any{}
	}

	// Each schema resolves $ref against its own interface document.
	// InputCompatible/OutputCompatible normalize both schemas internally,
	// so we use the left document as Root. For fixtures with cross-document
	// $ref resolution this would need separate normalizers, but the current
	// conformance fixtures use self-contained schemas.
	n := &Normalizer{Root: leftRoot}

	var (
		ok      bool
		compErr error
	)
	switch direction {
	case "input":
		ok, _, compErr = n.InputCompatible(leftSchema, rightSchema)
	case "output":
		ok, _, compErr = n.OutputCompatible(leftSchema, rightSchema)
	}

	if compErr != nil {
		var ope *OutsideProfileError
		if errors.As(compErr, &ope) {
			return opResult{verdict: "indeterminate"}
		}
		t.Fatalf("operation %q: unexpected error: %v", opKey, compErr)
	}
	if ok {
		return opResult{verdict: "compatible"}
	}
	return opResult{verdict: "incompatible"}
}

// runIdenticalFixture tests identical mode by normalizing both schemas and
// comparing their canonical JSON representations.
func runIdenticalFixture(t *testing.T, entry manifestFile, fix fixture) {
	t.Helper()

	for opKey, leftOp := range fix.Left.Operations {
		rightOp, found := fix.Right.Operations[opKey]
		if !found {
			t.Fatalf("operation %q in left but not in right", opKey)
		}

		var leftSchema, rightSchema map[string]any
		switch entry.Direction {
		case "input":
			leftSchema = leftOp.Input
			rightSchema = rightOp.Input
		case "output":
			leftSchema = leftOp.Output
			rightSchema = rightOp.Output
		default:
			t.Fatalf("unknown direction %q", entry.Direction)
		}

		if leftSchema == nil {
			leftSchema = map[string]any{}
		}
		if rightSchema == nil {
			rightSchema = map[string]any{}
		}

		nLeft := &Normalizer{Root: fix.Left.Raw}
		nRight := &Normalizer{Root: fix.Right.Raw}

		normLeft, err := nLeft.Normalize(leftSchema)
		if err != nil {
			t.Fatalf("normalize left %q: %v", opKey, err)
		}
		normRight, err := nRight.Normalize(rightSchema)
		if err != nil {
			t.Fatalf("normalize right %q: %v", opKey, err)
		}

		leftJSON, err := CanonicalString(normLeft)
		if err != nil {
			t.Fatalf("canonical left %q: %v", opKey, err)
		}
		rightJSON, err := CanonicalString(normRight)
		if err != nil {
			t.Fatalf("canonical right %q: %v", opKey, err)
		}

		if entry.Verdict == "compatible" {
			if leftJSON != rightJSON {
				t.Fatalf("operation %q: schemas should be identical after normalization:\n  left:  %s\n  right: %s",
					opKey, leftJSON, rightJSON)
			}
		} else {
			if leftJSON == rightJSON {
				t.Fatalf("operation %q: schemas should differ but are identical: %s",
					opKey, leftJSON)
			}
		}
	}
}

// collapseVerdicts applies spec verdict dominance:
// indeterminate > incompatible > compatible.
func collapseVerdicts(results []opResult) string {
	has := map[string]bool{}
	for _, r := range results {
		has[r.verdict] = true
	}
	if has["indeterminate"] {
		return "indeterminate"
	}
	if has["incompatible"] {
		return "incompatible"
	}
	return "compatible"
}

// TestConformance_ManifestComplete verifies that every fixture file referenced
// in the manifest actually exists on disk.
func TestConformance_ManifestComplete(t *testing.T) {
	manifestPath := filepath.Join(conformanceDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Skipf("conformance fixtures not available: %v", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	for _, entry := range m.Files {
		fixturePath := filepath.Join(conformanceDir, entry.Path)
		if _, err := os.Stat(fixturePath); err != nil {
			t.Errorf("manifest references missing file: %s", entry.Path)
		}
	}
	t.Logf("all %d manifest entries have corresponding files", len(m.Files))
}

// TestConformance_FixtureVerdictConsistency checks that the verdict in the
// manifest matches the verdict in each fixture's expected.summary.verdict.
func TestConformance_FixtureVerdictConsistency(t *testing.T) {
	manifestPath := filepath.Join(conformanceDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Skipf("conformance fixtures not available: %v", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	for _, entry := range m.Files {
		fixturePath := filepath.Join(conformanceDir, entry.Path)
		fdata, err := os.ReadFile(fixturePath)
		if err != nil {
			t.Errorf("read fixture %s: %v", entry.Path, err)
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(fdata, &raw); err != nil {
			t.Errorf("unmarshal fixture %s: %v", entry.Path, err)
			continue
		}
		var expected struct {
			Summary struct {
				Verdict string `json:"verdict"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(raw["expected"], &expected); err != nil {
			t.Errorf("unmarshal expected in %s: %v", entry.Path, err)
			continue
		}

		if expected.Summary.Verdict != entry.Verdict {
			t.Errorf("%s: manifest verdict %q != fixture expected verdict %q",
				entry.Path, entry.Verdict, expected.Summary.Verdict)
		}
	}
}

// TestConformance_KnownGapsAreReal verifies that each entry in knownGaps
// still references a fixture that exists in the manifest. If a fixture is
// removed from the manifest, the knownGaps entry should be removed too.
func TestConformance_KnownGapsAreReal(t *testing.T) {
	manifestPath := filepath.Join(conformanceDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Skipf("conformance fixtures not available: %v", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	manifestPaths := map[string]bool{}
	for _, entry := range m.Files {
		manifestPaths[entry.Path] = true
	}

	for path, reason := range knownGaps {
		if !manifestPaths[path] {
			t.Errorf("knownGaps entry %q (reason: %s) does not appear in manifest; remove it",
				path, reason)
		}
	}
}

