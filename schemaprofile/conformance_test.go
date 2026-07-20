package schemaprofile

// Conformance corpus adapter: runs this package's normalization and
// directional compatibility checks against the interfaces repository's
// schema-comparison corpus unmodified (conformance/comparison — the
// schema-comparison profile's fail-closed boundary, normalization
// equivalence, directional subsumption, boolean schema forms, and the
// unspecified-schema suppression rule).
//
// The corpus is located via OB_INTERFACES_CORPUS or the local-dev sibling
// path (openbindings/interfaces next to openbindings/openbindings-go); the
// tests skip when it is absent.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Manifest and fixture types matching the corpus conventions
// (interfaces/conformance/comparison/manifest.schema.json and
// fixture.schema.json).

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

// obOperation carries schema slots as `any` because fixtures may use either
// JSON Schema form: an object, or a boolean (§5.2). Absent slots decode to
// nil, which means unspecified.
type obOperation struct {
	Input   any      `json:"input,omitempty"`
	Output  any      `json:"output,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
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

// comparisonCorpusDir locates the interfaces conformance corpus: the
// OB_INTERFACES_CORPUS environment variable, or the local-dev sibling
// checkout. Same convention as selection_corpus_test.go at the module root.
func comparisonCorpusDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("OB_INTERFACES_CORPUS")
	if dir == "" {
		dir = filepath.Join("..", "..", "interfaces", "conformance")
	}
	if _, err := os.Stat(dir); err != nil {
		corpusUnavailable(t, "interfaces conformance corpus not found at %s (set OB_INTERFACES_CORPUS)", dir)
	}
	return dir
}

// corpusUnavailable skips a corpus-backed test when the corpus is absent —
// unless OB_CORPUS_REQUIRED is set (CI), in which case absence is a hard
// failure so a mis-wired path turns CI red instead of silently green. Unset
// (local dev) absence still skips, exactly as before.
func corpusUnavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("OB_CORPUS_REQUIRED") != "" {
		t.Fatalf(format+" (OB_CORPUS_REQUIRED is set)", args...)
	}
	t.Skipf(format, args...)
}

// fixtureSchemaObjectForm maps a fixture schema value to the object form the
// profile compares: an object schema as itself, boolean `true` as `{}`, and
// boolean `false` as `{"not": {}}` — the same equivalent spellings the SDK's
// SchemaObjectForm applies at the compatibility layer (duplicated here
// because an in-package test cannot import the root module without a cycle).
// ok is false when v is neither an object nor a boolean.
func fixtureSchemaObjectForm(v any) (map[string]any, bool) {
	switch s := v.(type) {
	case map[string]any:
		return s, true
	case bool:
		if s {
			return map[string]any{}, true
		}
		return map[string]any{"not": map[string]any{}}, true
	default:
		return nil, false
	}
}

func directionSchema(t *testing.T, direction string, op obOperation) any {
	t.Helper()
	switch direction {
	case "input":
		return op.Input
	case "output":
		return op.Output
	default:
		t.Fatalf("unknown direction %q", direction)
		return nil
	}
}

func TestConformance(t *testing.T) {
	dir := filepath.Join(comparisonCorpusDir(t), "comparison")
	manifestPath := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		corpusUnavailable(t, "comparison corpus not available at %s: %v", dir, err)
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
			fixturePath := filepath.Join(dir, entry.Path)
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
	t.Logf("ran %d conformance fixtures", ran)
}

// runSubsumeFixture tests subsumption mode by pairing operations between left
// and right, then calling InputCompatible or OutputCompatible on matched schemas.
// The per-operation results are collapsed to a summary verdict using the
// profile's dominance rules: indeterminate > incompatible > compatible.
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

	leftRaw := directionSchema(t, direction, leftOp)
	rightRaw := directionSchema(t, direction, rightOp)

	// An absent schema is unspecified, not Top: the slot's comparison is
	// skipped and reports no finding — the profile's suppression rule, the
	// same treatment CheckInterfaceCompatibility applies.
	if leftRaw == nil || rightRaw == nil {
		return opResult{verdict: "compatible"}
	}

	leftSchema, ok := fixtureSchemaObjectForm(leftRaw)
	if !ok {
		t.Fatalf("operation %q: left %s schema is neither object nor boolean", opKey, direction)
	}
	rightSchema, ok := fixtureSchemaObjectForm(rightRaw)
	if !ok {
		t.Fatalf("operation %q: right %s schema is neither object nor boolean", opKey, direction)
	}

	// Each schema resolves $ref against its OWN interface document: the
	// left schema normalizes against the left document root, the right
	// schema against the right document root, and the pre-normalized pair
	// runs the package-level directional check — the same per-side rooting
	// CheckInterfaceCompatibility applies.
	nLeft := &Normalizer{Root: leftRoot}
	nRight := &Normalizer{Root: rightRoot}

	var (
		compatible bool
		rightNorm  map[string]any
	)
	leftNorm, compErr := nLeft.Normalize(leftSchema)
	if compErr == nil {
		rightNorm, compErr = nRight.Normalize(rightSchema)
	}
	if compErr == nil {
		switch direction {
		case "input":
			compatible, _, compErr = InputCompatible(leftNorm, rightNorm)
		case "output":
			compatible, _, compErr = OutputCompatible(leftNorm, rightNorm)
		}
	}

	if compErr != nil {
		var ope *OutsideProfileError
		if errors.As(compErr, &ope) {
			return opResult{verdict: "indeterminate"}
		}
		t.Fatalf("operation %q: unexpected error: %v", opKey, compErr)
	}
	if compatible {
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

		leftSchema, ok := fixtureSchemaObjectForm(directionSchema(t, entry.Direction, leftOp))
		if !ok {
			leftSchema = map[string]any{}
		}
		rightSchema, ok := fixtureSchemaObjectForm(directionSchema(t, entry.Direction, rightOp))
		if !ok {
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

// collapseVerdicts applies the profile's verdict dominance:
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
	dir := filepath.Join(comparisonCorpusDir(t), "comparison")
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		corpusUnavailable(t, "comparison corpus not available: %v", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	for _, entry := range m.Files {
		fixturePath := filepath.Join(dir, entry.Path)
		if _, err := os.Stat(fixturePath); err != nil {
			t.Errorf("manifest references missing file: %s", entry.Path)
		}
	}
	t.Logf("all %d manifest entries have corresponding files", len(m.Files))
}

// TestConformance_FixtureVerdictConsistency checks that the verdict in the
// manifest matches the verdict in each fixture's expected.summary.verdict.
func TestConformance_FixtureVerdictConsistency(t *testing.T) {
	dir := filepath.Join(comparisonCorpusDir(t), "comparison")
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		corpusUnavailable(t, "comparison corpus not available: %v", err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	for _, entry := range m.Files {
		fixturePath := filepath.Join(dir, entry.Path)
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
