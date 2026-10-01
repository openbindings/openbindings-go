package openbindings

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// The name a conclusion gives is compared always, before verification: a
// wrong name fails even when the pinned bytes are verified.
func TestJudgeNamingControls(t *testing.T) {
	const rev = appliedRevision
	report := func(version, revision string) ValidationReport {
		return ValidationReport{Conclusion: ConclusionConformant, Version: version, Revision: revision}
	}
	for _, c := range []struct {
		name               string
		report             ValidationReport
		verified, required bool
		category           string
		detail             string
	}{
		{"the declared name, verified", report("0.2.0", rev), true, true, corpus.Pass, ""},
		{"a wrong revision despite verified bytes", report("0.2.0", ""), true, true, corpus.Fail, "names"},
		{"a wrong release despite verified bytes", report("0.2.1", rev), true, true, corpus.Fail, "names"},
		{"a wrong name, unverified", report("0.2.0", ""), false, false, corpus.Fail, "names"},
		{"the declared name, unverified, required", report("0.2.0", rev), false, true, corpus.Fail, "UNVERIFIED"},
		{"the declared name, unverified", report("0.2.0", rev), false, false, corpus.Unverified, "why"},
	} {
		j := judgeNaming(c.report, "0.2.0", rev, c.verified, "why", c.required)
		if j.Category != c.category || !strings.Contains(j.Detail, c.detail) {
			t.Errorf("%s: %+v", c.name, j)
		}
	}
}

// The core declares it continues with every non-conformant document the
// model carries, so no document back fails, for a non-conformant document as
// for a conformant one.
func TestNoDocument(t *testing.T) {
	cause := errors.New("cause")
	if j := noDocument([]string{"OBI-D-02"}, cause); j.Category != corpus.Fail || !strings.Contains(j.Detail, "continues with every non-conformant document") {
		t.Errorf("a non-conformant case: %+v", j)
	}
	if j := noDocument(nil, cause); j.Category != corpus.Fail || !strings.Contains(j.Detail, "conformant document") {
		t.Errorf("a conformant case: %+v", j)
	}
}

// conclude-conformance's conformant admits conformance-undetermined, as
// validate-document's does (OBI-T-09 only prohibits), and every other
// conclusion is expected exactly: non-conformant from evidence that
// establishes no violation fails.
func TestJudgeConcludeControls(t *testing.T) {
	satisfied := allRules(EvidenceSatisfied)
	partial := allRules(EvidenceSatisfied)
	delete(partial, "OBI-D-12")
	scenario := func(evidence map[string]RuleEvidenceStatus, expected string) corpus.Case {
		raw, _ := json.Marshal(map[string]any{"given": map[string]any{"evidence": evidence}, "expected": map[string]any{"conclusion": expected}})
		return corpus.Case{ID: "SYNTHETIC-01", Format: corpus.FormatV2, Action: "conclude-conformance", Raw: raw}
	}
	for _, c := range []struct {
		name     string
		evidence map[string]RuleEvidenceStatus
		expected string
		category string
	}{
		{"conformant, concluded conformant", satisfied, "conformant", corpus.Pass},
		{"conformant, concluded undetermined", partial, "conformant", corpus.Pass},
		{"undetermined, concluded undetermined", partial, "conformance-undetermined", corpus.Pass},
		{"undetermined, concluded conformant", satisfied, "conformance-undetermined", corpus.Fail},
		{"non-conformant without a violation", partial, "non-conformant", corpus.Fail},
	} {
		if j := judgeConclude(scenario(c.evidence, c.expected)); j.Category != c.category {
			t.Errorf("%s: %+v", c.name, j)
		}
	}
}

// A case whose retrieval sentinels cannot start fails: an adapter that cannot
// observe retrieval cannot judge it, and skipping it would hide that.
func TestSentinelsThatCannotStart(t *testing.T) {
	defer func(previous func([]string) (*sentinels, error)) { sentinelStarter = previous }(sentinelStarter)
	sentinelStarter = func([]string) (*sentinels, error) { return nil, errors.New("no FIFOs on this platform") }
	raw := `{"given": {"document": {"openbindings": "0.2.0", "operations": {"op": {}}, "sources": {"s": {"kind": "{retrieval-sentinel:file}"}},
		"bindings": {"b": {"operation": "op", "source": "s"}}, "dependencies": {"d": {"operation": "op", "kinds": ["a@1"]}}},
		"dependency": "d", "binding": "b", "retrievalSentinels": ["file"]}, "expected": {"outcome": "does-not-meet"}}`
	if j := judgeKind(corpus.Case{ID: "T01-S-15", Action: "check-dependency-kind", Raw: []byte(raw)}); j.Category != corpus.Fail || !strings.Contains(j.Detail, "cannot start") {
		t.Errorf("a case whose sentinels cannot start: %+v", j)
	}
}
