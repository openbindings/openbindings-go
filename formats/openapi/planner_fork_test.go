package openapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This package carries COPIES of openapi-client's planner. The client keeps its
// planner unexported — `openapi-client/go/media.go` has 120 unexported functions
// and zero exported ones — so this package cannot import it and copied it
// instead. Synthesis runs through these copies while invocation runs through the
// client, which is why a planner fix has to land twice.
//
// Eliminating the duplication needs either a new published module both sides
// depend on or a public planner API on openapi-client, and that is a permanent
// module-boundary decision rather than a refactor. Until it is taken, this test
// removes the part that actually bites: silent divergence. A copy that drifts
// from its origin fails here instead of surviving as two subtly different
// planners that agree on every scenario anyone thought to write.
//
// Adding a deliberate delta is not forbidden; it is recorded, in maxDelta below,
// with the reason. An UNRECORDED delta is the failure.
type plannerCopy struct {
	file     string
	maxDelta int    // differing lines permitted against the client's copy, package line excluded
	why      string // why any delta at all is permitted here
}

var plannerCopies = []plannerCopy{
	{"oas_confinement.go", 0, "byte-identical to the client's; no delta is legitimate"},
	{"ref_metadata.go", 0, "byte-identical to the client's; no delta is legitimate"},
	{"schema_dialect.go", 0, "byte-identical to the client's; no delta is legitimate"},
	{"target_restriction.go", 0, "byte-identical to the client's; no delta is legitimate"},
	{"external_closure.go", 4, "one comment cites this package's own section numbering"},
	{"schema_overlay.go", 42, "synthesis-side overlay differences, pinned at the current width"},
	{"ref_siblings.go", 127, "diverged before this gate existed; pinned at its exact current width"},
	{"acceptance_floor.go", 168, "diverged before this gate existed; pinned at its exact current width"},
	{"media.go", 270, "diverged before this gate existed; pinned at its exact current width"},
}

func clientPlannerDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("OB_OPENAPI_CLIENT_SRC")
	if root == "" {
		for _, candidate := range []string{
			filepath.Join("..", "..", "..", "openapi-client", "go"),
			filepath.Join("..", "..", "openapi-client", "go"),
		} {
			if _, err := os.Stat(filepath.Join(candidate, "media.go")); err == nil {
				return candidate
			}
		}
		root = filepath.Join("..", "..", "..", "openapi-client", "go")
	}
	if _, err := os.Stat(filepath.Join(root, "media.go")); err != nil {
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatalf("openapi-client source not found at %s (OB_CORPUS_REQUIRED is set; set OB_OPENAPI_CLIENT_SRC)", root)
		}
		t.Skipf("openapi-client source not found at %s (set OB_OPENAPI_CLIENT_SRC)", root)
	}
	return root
}

// countDelta reports how many lines differ between two files, ignoring the
// package clause, using a longest-common-subsequence-free line multiset compare.
// A multiset compare is deliberate: it does not care whether a block moved, only
// whether the CONTENT diverged, which is the property this gate is about.
func countDelta(left, right []string) int {
	seen := map[string]int{}
	for _, line := range left {
		seen[strings.TrimSpace(line)]++
	}
	delta := 0
	for _, line := range right {
		key := strings.TrimSpace(line)
		if seen[key] > 0 {
			seen[key]--
			continue
		}
		delta++
	}
	for _, remaining := range seen {
		delta += remaining
	}
	return delta
}

func readPlannerFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(string(data), "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "package ") {
			continue
		}
		kept = append(kept, line)
	}
	return kept
}

func TestPlannerCopiesHaveNotDrifted(t *testing.T) {
	clientDir := clientPlannerDir(t)
	for _, copied := range plannerCopies {
		t.Run(copied.file, func(t *testing.T) {
			ours := readPlannerFile(t, copied.file)
			theirs := readPlannerFile(t, filepath.Join(clientDir, copied.file))
			delta := countDelta(ours, theirs)
			if delta > copied.maxDelta {
				t.Fatalf("%s has drifted from openapi-client's copy: %d differing lines, %d recorded (%s).\n"+
					"Either land the same change in openapi-client/go/%s, or widen maxDelta in plannerCopies with the reason.",
					copied.file, delta, copied.maxDelta, copied.why, copied.file)
			}
			if delta < copied.maxDelta && copied.maxDelta > 0 {
				t.Logf("%s: %d differing lines against a recorded ceiling of %d — the ceiling can be tightened",
					copied.file, delta, copied.maxDelta)
			}
		})
	}
}
