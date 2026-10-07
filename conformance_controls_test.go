package openbindings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/corpus"
)

// The applied text is verified only against a full commit of the history
// holding the corpus, by the pinned hash and the embedded schema: an empty,
// symbolic, abbreviated, or foreign revision is not verified, and neither the
// index nor the working tree plays a part.
func TestAppliedTextControls(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...).Output()
		if err != nil {
			t.Fatal(args, err)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write("openbindings.md", "the applied text")
	write("openbindings.schema.json", string(openbindingsSchemaJSON))
	run("add", ".")
	run("commit", "-q", "-m", "applied")
	commit := run("rev-parse", "HEAD")
	digest := sha256.Sum256([]byte("the applied text"))
	pinned := hex.EncodeToString(digest[:])
	write("openbindings.md", "a later text")
	run("commit", "-q", "-am", "later")
	write("openbindings.md", "staged text")
	run("add", ".")
	write("openbindings.md", "working text")
	corpusDir := filepath.Join(repo, "conformance")
	if err := os.Mkdir(corpusDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name                     string
		revision, pinnedRevision string
		sum                      string
		schema                   []byte
		verified                 bool
		why                      string
	}{
		{"the pinned commit", commit, commit, pinned, openbindingsSchemaJSON, true, ""},
		{"an empty release", commit, commit, pinned, openbindingsSchemaJSON, false, "not a SemVer"},
		{"an empty revision (a release named alone)", "", "", pinned, openbindingsSchemaJSON, false, "release snapshot"},
		{"a symbolic revision", "HEAD", "HEAD", pinned, openbindingsSchemaJSON, false, "not a full 40-hex commit"},
		{"an abbreviated revision", commit[:7], commit[:7], pinned, openbindingsSchemaJSON, false, "not a full 40-hex commit"},
		{"a hash pinned for another revision", commit, strings.Repeat("a", 40), pinned, openbindingsSchemaJSON, false, "pin the named revision's hash"},
		{"a revision outside the history", strings.Repeat("c", 40), strings.Repeat("c", 40), pinned, openbindingsSchemaJSON, false, "not a commit"},
		{"a blob as the revision", run("rev-parse", commit+":openbindings.md"), run("rev-parse", commit+":openbindings.md"), pinned, openbindingsSchemaJSON, false, "not a commit"},
		{"another text", commit, commit, strings.Repeat("0", 64), openbindingsSchemaJSON, false, "hashes to"},
		{"another schema", commit, commit, pinned, []byte("{}"), false, "not the schema this SDK embeds"},
	} {
		release := "0.2.0"
		if c.name == "an empty release" {
			release = ""
		}
		verified, why := verifyAppliedText(corpusDir, release, c.revision, c.pinnedRevision, c.sum, c.schema)
		if verified != c.verified || !strings.Contains(why, c.why) {
			t.Errorf("%s: %v, %q", c.name, verified, why)
		}
	}
}

// The core declares exact-lone-surrogate-strings unsupported: a text holding
// an escaped lone surrogate concludes conformance-undetermined, a decline
// that passes a validate-document case depending on that feature. Declared
// supported, the same decline would be a SHORTFALL, and so it is on a
// fixture, which carries no dependsOn.
func TestRootProfileControls(t *testing.T) {
	text := `{"openbindings":"0.2.0","description":"\ud800","operations":{}}`
	scenario, _ := json.Marshal(map[string]any{"given": map[string]any{"documentText": text},
		"expected": map[string]any{"outcome": "conformant", "dependsOn": []string{"exact-lone-surrogate-strings"}}})
	if j := judgeDocument(corpus.Case{ID: "CONFORMANCE-06", Action: "validate-document", Raw: scenario}, rootProfile); j.Category != corpus.Pass {
		t.Errorf("under the core's profile: %+v", j)
	}
	supporting := corpus.Profile{Features: map[string]bool{"exact-lone-surrogate-strings": true}}
	if j := judgeDocument(corpus.Case{ID: "CONFORMANCE-06", Action: "validate-document", Raw: scenario}, supporting); j.Category != corpus.Shortfall {
		t.Errorf("under a profile declaring the feature supported: %+v", j)
	}
	fixture, _ := json.Marshal(map[string]any{"description": "d", "documentText": text, "valid": true})
	if j := judgeFixture(corpus.Case{ID: "document/x.json#/tests/0", Action: corpus.ActionValidity, Raw: fixture}); j.Category != corpus.Shortfall {
		t.Errorf("a conforming fixture the core declines: %+v", j)
	}
}

// The core fails a run on a SHORTFALL as on a FAIL: rootProfile is its own
// declaration. A keyed expected failure passes only with its signature.
func TestCaseReportControls(t *testing.T) {
	keyed := keyedFailure{signature: "sig", reason: "why"}
	for _, c := range []struct {
		name     string
		j        corpus.Judgment
		expected bool
		failed   bool
	}{
		{"a pass", corpus.Judgment{Category: corpus.Pass}, false, false},
		{"an omission", corpus.Judgment{Category: corpus.Omitted, Detail: "why"}, false, false},
		{"a FAIL", corpus.Judgment{Category: corpus.Fail, Detail: "x"}, false, true},
		{"a SHORTFALL", corpus.Judgment{Category: corpus.Shortfall, Detail: "x"}, false, true},
		{"a keyed FAIL with its signature", corpus.Judgment{Category: corpus.Fail, Detail: "sig"}, true, false},
		{"a keyed FAIL with another", corpus.Judgment{Category: corpus.Fail, Detail: "other"}, true, true},
		{"a keyed case that passes", corpus.Judgment{Category: corpus.Pass}, true, true},
	} {
		if failed, message := caseReport(c.j, keyed, c.expected); failed != c.failed {
			t.Errorf("%s: failed %v (%s), want %v", c.name, failed, message, c.failed)
		}
	}
}

// The adapter runs end to end on a corpus of its own, so its loading,
// designation, judging, and reconciling run without the specification's
// corpus: each root action passes, and the value case is left to the
// schemaeval module.
func TestRootCorpusEndToEnd(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"document/OBI-06.json": `{"rule": "OBI-06", "section": "10", "description": "d", "tests": [
			{"description": "conforms", "document": {"openbindings": "0.2.0", "operations": {"op": {}}}, "valid": true},
			{"description": "an unknown operation", "document": {"openbindings": "0.2.0", "operations": {}, "sources": {"s": {"kind": "k"}},
				"bindings": {"b": {"operation": "gone", "source": "s"}}}, "valid": false, "violates": ["OBI-06"], "notViolated": ["OBI-07"]}]}`,
		"document/section-12.json": `{"section": "12", "description": "d", "tests": [
			{"description": "an extension", "document": {"openbindings": "0.2.0", "x-a": 1, "operations": {}}, "valid": true}]}`,
		"scenarios/10-conformance.json": `{"format": "openbindings.core-scenarios@3", "section": "10", "description": "d", "scenarios": [
			{"id": "CONFORMANCE-01", "description": "d", "action": "validate-document", "given": {"document": {"openbindings": "0.2.5", "operations": {}}}, "expected": {"outcome": "conformant"}},
			{"id": "CONFORMANCE-02", "description": "d", "action": "validate-document", "given": {"documentText": "{\"openbindings\":\"0.2.0\",\"description\":\"\\ud800\",\"operations\":{}}"},
				"expected": {"outcome": "conformant", "dependsOn": ["exact-lone-surrogate-strings"]}}]}`,
		"scenarios/5.1-names.json": `{"format": "openbindings.core-scenarios@3", "section": "5.1", "description": "d", "scenarios": [
			{"id": "NAMES-01", "description": "d", "action": "resolve-operation", "given": {"document": {"openbindings": "0.2.0", "operations": {"op": {"aliases": ["alias"]}},
				"sources": {"s": {"kind": "k"}}, "bindings": {"b": {"operation": "op", "source": "s"}}}, "name": "alias"},
				"expected": {"outcome": "resolved", "operationKey": "op", "bindingKeys": ["b"]}},
			{"id": "NAMES-02", "description": "d", "action": "resolve-operation", "given": {"document": {"openbindings": "0.2.0", "operations": {"op": {}}}, "name": "OP"},
				"expected": {"outcome": "not-found"}}]}`,
		"scenarios/6-kinds.json": `{"format": "openbindings.core-scenarios@3", "section": "6", "description": "d", "scenarios": [
			{"id": "KINDS-01", "description": "d", "action": "check-dependency-kind", "given": {"document": {"openbindings": "0.2.0", "operations": {"op": {}},
				"sources": {"s": {"kind": "a@1"}}, "bindings": {"b": {"operation": "op", "source": "s"}}, "dependencies": {"d": {"operation": "op", "kinds": ["A@1"]}}},
				"dependency": "d", "binding": "b"}, "expected": {"outcome": "does-not-meet"}}]}`,
		"scenarios/5.2-value-contracts.json": `{"format": "openbindings.core-scenarios@3", "section": "5.2", "description": "d", "scenarios": [
			{"id": "VALUES-01", "description": "d", "action": "validate-operation-values", "given": {"document": {"openbindings": "0.2.0", "operations": {"op": {"input": {"type": "string"}}}},
				"operation": "op", "side": "input", "values": ["x"]}, "expected": {"results": ["satisfies"]}}]}`,
	}
	type entry struct {
		Path      string `json:"path"`
		Tests     int    `json:"tests,omitempty"`
		Scenarios int    `json:"scenarios,omitempty"`
	}
	manifest := map[string][]entry{"files": {{Path: "document/OBI-06.json", Tests: 2}, {Path: "document/section-12.json", Tests: 1}},
		"scenarioFiles": {{Path: "scenarios/10-conformance.json", Scenarios: 2}, {Path: "scenarios/5.1-names.json", Scenarios: 2},
			{Path: "scenarios/6-kinds.json", Scenarios: 1}, {Path: "scenarios/5.2-value-contracts.json", Scenarios: 1}}}
	data, _ := json.Marshal(manifest)
	files["manifest.json"] = string(data)
	for name, content := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runRootCorpus(t, dir)
}
