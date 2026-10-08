package openbindings

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/corpus"
)

// This file is the core module's corpus adapter. It executes every corpus
// case whose action needs no schema evaluator: validity fixtures,
// validate-document, resolve-operation, and check-dependency-kind. The
// value actions (validate-operation-values and check-examples) run in the
// schemaeval module's adapter, under the project's ECMA-262 evaluator; core's
// tests cannot import it. Each module records every case designated to it,
// executed or omitted with a reason, checks the count against the corpus
// manifest, and judges each answer as the corpus's Judging table states,
// under the capability profile it declares. The scenarios are in format @3,
// the corpus of the specification text this SDK applies.

type conformanceFixture struct {
	Rule        string            `json:"rule"`
	Section     string            `json:"section"`
	Description string            `json:"description"`
	Tests       []conformanceTest `json:"tests"`
}

type conformanceTest struct {
	Description    string          `json:"description"`
	Document       json.RawMessage `json:"document"`
	DocumentText   *string         `json:"documentText,omitempty"`
	DocumentBase64 string          `json:"documentBase64,omitempty"`
	Valid          bool            `json:"valid"`
	Violates       []string        `json:"violates,omitempty"`
	NotViolated    []string        `json:"notViolated,omitempty"`
}

// findConformanceCorpus locates the spec repo's conformance/ root: the
// directory OB_SPEC_CORPUS names, else a sibling spec checkout.
func findConformanceCorpus() string { return corpus.Locate(".") }

// rootProfile is the capability profile the core declares for the features
// the cases this module executes depend on.
var rootProfile = corpus.Profile{Features: map[string]bool{
	"repeated-member-detection": true,
	"exact-numbers":             true,
	// A Go string cannot carry an escaped lone surrogate, a capability limit
	// the package documentation declares, so ValidateDocument leaves a text
	// holding one conformance-undetermined (Reports and Verdicts).
	"exact-lone-surrogate-strings": false,
}}

// appliedTextRevision and appliedTextSHA256 bind the text this SDK applies:
// the revision of github.com/openbindings/spec it pins, which must be
// appliedRevision, and the sha256 of that revision's openbindings.md.
const appliedTextRevision = "1ee84b244bea0047882cc81a2e074eec460b874b"

const appliedTextSHA256 = "cd9915ebb68811a5a2f1d53558f9753d7ca37fd0df35b88e143b466df684fddf"

// appliedTextVerified verifies the text this SDK names against the bytes it
// pins (verifyAppliedText), for the history of the specification repository
// holding the corpus.
func appliedTextVerified(corpusDir string) (bool, string) {
	return verifyAppliedText(corpusDir, appliedRelease, appliedRevision, appliedTextRevision, appliedTextSHA256, openbindingsSchemaJSON)
}

var fullRevision = regexp.MustCompile(`^[0-9a-f]{40}$`)

// verifyAppliedText verifies a declared applied text: the release must be a
// SemVer 2.0.0 version, the revision a full 40-hex commit of the specification repository holding corpusDir (git
// rev-parse --verify REV^{commit} gives it back), the pinned hash must be
// bound to it, the openbindings.md at it must hash to the pinned sha256, and
// its schema must be the one this SDK embeds. Both are read from the history,
// never from its checkout or index, so the text beside the corpus plays no
// part and an unrelated specification commit cannot change the result. A
// release named alone, with no revision, is not verified: no verification
// against a release snapshot exists.
func verifyAppliedText(corpusDir, release, revision, pinnedRevision, pinnedSHA256 string, schema []byte) (bool, string) {
	switch {
	case !isValidSemver(release):
		return false, fmt.Sprintf("appliedRelease %q is not a SemVer 2.0.0 version", release)
	case revision == "":
		return false, "a release named alone, with no revision, is not verified: no verification against a release snapshot exists"
	case !fullRevision.MatchString(revision):
		return false, fmt.Sprintf("appliedRevision %q is not a full 40-hex commit", revision)
	case pinnedRevision != revision:
		return false, fmt.Sprintf("appliedTextSHA256 is the hash of the text at %s, but appliedRevision is %s: pin the named revision's hash", pinnedRevision, revision)
	}
	if commit, err := git(corpusDir, "rev-parse", "--verify", "--quiet", revision+"^{commit}"); err != nil || string(bytes.TrimSpace(commit)) != revision {
		return false, fmt.Sprintf("%s is not a commit of the specification history holding the corpus", revision)
	}
	text, err := git(corpusDir, "show", revision+":openbindings.md")
	if err != nil {
		return false, fmt.Sprintf("the specification history holding the corpus does not give the text at %s: %v", revision, err)
	}
	sum := sha256.Sum256(text)
	if got := hex.EncodeToString(sum[:]); got != pinnedSHA256 {
		return false, fmt.Sprintf("openbindings.md at %s hashes to %s, not the pinned %s", revision, got, pinnedSHA256)
	}
	pinnedSchema, err := git(corpusDir, "show", revision+":openbindings.schema.json")
	if err != nil {
		return false, fmt.Sprintf("the specification history holding the corpus does not give the schema at %s: %v", revision, err)
	}
	if !bytes.Equal(pinnedSchema, schema) {
		return false, fmt.Sprintf("openbindings.schema.json at %s is not the schema this SDK embeds", revision)
	}
	return true, ""
}

// git runs a git command in the repository holding dir.
func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func TestConformanceCorpus(t *testing.T) {
	dir := findConformanceCorpus()
	if dir == "" {
		// OB_CORPUS_REQUIRED (set in CI) turns a missing corpus into a hard
		// failure so a mis-wired path turns CI red instead of silently green;
		// unset (local dev) it still skips.
		if corpus.Required() {
			t.Fatal("spec conformance corpus not found (OB_CORPUS_REQUIRED is set; set OB_SPEC_CORPUS to the spec repo's conformance dir)")
		}
		t.Skip("spec conformance corpus not found")
	}
	runRootCorpus(t, dir)
}

// runRootCorpus executes the cases designated to the core module, records
// each, and checks that every one was executed or omitted with a reason.
func runRootCorpus(t *testing.T, dir string) {
	c, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ledger corpus.Ledger
	for _, cs := range c.Cases {
		if module, _ := corpus.Designate(cs); module != corpus.ModuleRoot {
			continue // the schemaeval module records it
		}
		t.Run(cs.ID+"/"+cs.Description, func(t *testing.T) {
			j := judgeRootCase(cs)
			ledger.Record(cs.ID, j)
			keyed, expected := expectedFailures[cs.ID]
			switch failed, message := caseReport(j, keyed, expected); {
			case failed:
				t.Error(message)
			case j.Category == corpus.Omitted:
				t.Skip(message)
			default:
				t.Log(message)
			}
		})
	}
	problems, summary := c.Reconcile(corpus.ModuleRoot, &ledger)
	t.Logf("corpus %s\n%s", dir, summary)
	for _, p := range problems {
		t.Error(p)
	}
}

// caseReport says whether a case's judgment fails the run, and what to
// report. A FAIL fails it unless the case is keyed to fail with that very
// signature; a SHORTFALL fails it, since rootProfile is the core's own
// declaration; a keyed case that no longer fails fails it too, so its entry
// is removed.
func caseReport(j corpus.Judgment, keyed keyedFailure, expected bool) (failed bool, message string) {
	switch {
	case expected && j.Category == corpus.Fail && j.Detail == keyed.signature:
		return false, fmt.Sprintf("keyed expected failure (%s): %s", keyed.reason, j.Detail)
	case expected && j.Category == corpus.Fail:
		return true, fmt.Sprintf("%s (the keyed expected failure's signature is %q)", j.Detail, keyed.signature)
	case expected:
		return true, fmt.Sprintf("the keyed expected failure is now %s (%s): remove its expectedFailures entry", j.Category, j.Detail)
	case j.Category == corpus.Fail:
		return true, j.Detail
	case j.Category == corpus.Shortfall:
		return true, "SHORTFALL against the core's declared profile: " + j.Detail
	}
	return false, j.Category + ": " + j.Detail
}

func fail(format string, a ...any) corpus.Judgment {
	return corpus.Judgment{Category: corpus.Fail, Detail: fmt.Sprintf(format, a...)}
}

func pass(detail string) corpus.Judgment {
	return corpus.Judgment{Category: corpus.Pass, Detail: detail}
}

func judgeRootCase(cs corpus.Case) corpus.Judgment {
	switch cs.Action {
	case corpus.ActionValidity:
		return judgeFixture(cs)
	case "validate-document":
		return judgeDocument(cs, rootProfile)
	case "resolve-operation":
		return judgeResolve(cs)
	case "check-dependency-kind":
		return judgeKind(cs)
	}
	return fail("action %q has no executor in this module", cs.Action)
}

func conformanceDocumentBytes(tt conformanceTest) ([]byte, error) {
	switch {
	case tt.Document != nil:
		return []byte(tt.Document), nil
	case tt.DocumentText != nil:
		return []byte(*tt.DocumentText), nil
	case tt.DocumentBase64 != "":
		data, err := base64.StdEncoding.DecodeString(tt.DocumentBase64)
		if err != nil {
			return nil, fmt.Errorf("decode documentBase64: %w", err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("fixture supplies no document carriage")
	}
}

func isRefusal(err error) bool { return errors.As(err, new(*VersionRefusalError)) }

// documentAnswer is ValidateDocument's answer about a text, in the corpus's
// terms: conformant, non-conformant, or a decline for
// conformance-undetermined, with the rules it reports violated. Every case
// declares the 0.2 line or no version, so a refusal is a problem, and so is
// an error that disagrees with the report's conclusion.
func documentAnswer(data []byte) (answer corpus.DocumentAnswer, problems []string) {
	_, report, err := ValidateDocument(data)
	var violation *ValidationError
	switch {
	case isRefusal(err):
		return answer, []string{fmt.Sprintf("refused a text the 0.2 line's rules govern: %v", err)}
	case err != nil && !errors.As(err, &violation):
		return answer, []string{fmt.Sprintf("ValidateDocument: unexpected error %v", err)}
	case (violation != nil) != (report.Conclusion == ConclusionNonConformant):
		problems = append(problems, fmt.Sprintf("ValidateDocument's error %v disagrees with its report's conclusion %s", err, report.Conclusion))
	}
	switch report.Conclusion {
	case ConclusionConformant:
		answer.Conclusion = corpus.Conformant
	case ConclusionNonConformant:
		answer.Conclusion = corpus.NonConformant
	default:
		answer.Conclusion = corpus.Decline
	}
	answer.Violated = report.Violated
	return answer, problems
}

// judgeFixture holds a validity fixture to ValidateDocument's report, as the
// Judging table states, and holds ParseDocument followed by
// Document.Validate to the same conclusion where the report decides one: a
// conforming text is accepted, and a violating one is not.
func judgeFixture(cs corpus.Case) corpus.Judgment {
	var tt conformanceTest
	if err := json.Unmarshal(cs.Raw, &tt); err != nil {
		return fail("unreadable fixture: %v", err)
	}
	data, err := conformanceDocumentBytes(tt)
	if err != nil {
		return fail("invalid fixture carriage: %v", err)
	}
	answer, problems := documentAnswer(data)
	if answer.Conclusion != corpus.Decline && len(problems) == 0 {
		doc, err := ParseDocument(data)
		if err == nil {
			_, err = doc.Validate()
		}
		switch accepted := err == nil; {
		case accepted && answer.Conclusion == corpus.NonConformant:
			problems = append(problems, "ParseDocument and Document.Validate accepted a text ValidateDocument concludes non-conformant")
		case !accepted && answer.Conclusion == corpus.Conformant:
			problems = append(problems, fmt.Sprintf("ParseDocument and Document.Validate refused a text ValidateDocument concludes conformant: %v", err))
		}
	}
	if len(problems) > 0 {
		return fail("%s", strings.Join(problems, "; "))
	}
	return corpus.JudgeFixture(cs.Raw, answer, rootProfile)
}

type documentCarriage struct {
	Document       json.RawMessage `json:"document"`
	DocumentText   *string         `json:"documentText"`
	DocumentBase64 string          `json:"documentBase64"`
}

func (g documentCarriage) bytes() ([]byte, error) {
	return conformanceDocumentBytes(conformanceTest{Document: g.Document, DocumentText: g.DocumentText, DocumentBase64: g.DocumentBase64})
}

// judgeDocument executes validate-document through ValidateDocument, under
// profile p.
func judgeDocument(cs corpus.Case, p corpus.Profile) corpus.Judgment {
	var s struct {
		Given    documentCarriage `json:"given"`
		Expected json.RawMessage  `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	data, err := s.Given.bytes()
	if err != nil {
		return fail("%v", err)
	}
	answer, problems := documentAnswer(data)
	if len(problems) > 0 {
		return fail("%s", strings.Join(problems, "; "))
	}
	return corpus.JudgeDocument(s.Expected, answer, p)
}

// scenarioDocument decodes a scenario's document with ValidateDocument, as a
// caller holding the text would. Every such document declares the 0.2 line
// and conforms, so a refusal, or a document the model does not carry, is a
// failure.
func scenarioDocument(document json.RawMessage) (*Document, error) {
	doc, _, err := ValidateDocument(document)
	switch {
	case isRefusal(err):
		return nil, fmt.Errorf("refused a document the 0.2 line's rules govern: %v", err)
	case doc == nil:
		return nil, fmt.Errorf("the model does not carry the document: %v", err)
	}
	return doc, nil
}

// judgeResolve executes resolve-operation: the model ValidateDocument
// returns, Document.ResolveOperation, and Document.OperationBindings.
func judgeResolve(cs corpus.Case) corpus.Judgment {
	var s struct {
		Given struct {
			Document json.RawMessage `json:"document"`
			Name     string          `json:"name"`
		} `json:"given"`
		Expected struct {
			Outcome      string   `json:"outcome"`
			OperationKey string   `json:"operationKey"`
			BindingKeys  []string `json:"bindingKeys"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	doc, err := scenarioDocument(s.Given.Document)
	if err != nil {
		return fail("%v", err)
	}
	key, _, found := doc.ResolveOperation(s.Given.Name)
	switch s.Expected.Outcome {
	case "not-found":
		if found {
			return fail("resolved to %q; expected not-found", key)
		}
		return pass("not-found")
	case "resolved":
	default:
		return fail("unknown expected outcome %q", s.Expected.Outcome)
	}
	if !found {
		return fail("not-found; expected %q", s.Expected.OperationKey)
	}
	if key != s.Expected.OperationKey {
		return fail("resolved to %q; expected %q", key, s.Expected.OperationKey)
	}
	bindings := doc.OperationBindings(key)
	want := slices.Clone(s.Expected.BindingKeys)
	sort.Strings(want)
	if !slices.Equal(bindings, want) && !(len(bindings) == 0 && len(want) == 0) {
		return fail("binding keys %v; expected %v", bindings, want)
	}
	return pass("resolved " + key)
}

// keyedFailure is a case this core is expected to fail: the failure's
// signature and why.
type keyedFailure struct{ signature, reason string }

// expectedFailures keys the cases of the specification's corpus this core is
// expected to fail: the corpus decides them and the core does not yet follow.
// A keyed case that fails with its signature is reported and does not fail
// the run, so the module is green at this baseline and any other failure
// turns it red; a keyed case that fails otherwise, or no longer fails, fails
// the run, telling the reader to remove its entry. None is keyed: the core
// follows the corpus of the text it applies.
var expectedFailures = map[string]keyedFailure{}

// judgeKind executes check-dependency-kind: the model ValidateDocument
// returns and Dependency.AcceptsKind.
func judgeKind(cs corpus.Case) corpus.Judgment {
	var s struct {
		Given struct {
			Document   json.RawMessage `json:"document"`
			Dependency string          `json:"dependency"`
			Binding    string          `json:"binding"`
		} `json:"given"`
		Expected struct {
			Outcome string `json:"outcome"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	doc, err := scenarioDocument(s.Given.Document)
	if err != nil {
		return fail("%v", err)
	}
	dependency, declared := doc.Dependencies[s.Given.Dependency]
	binding, bound := doc.Bindings[s.Given.Binding]
	if !declared || !bound {
		return fail("the document has no dependency %q or no binding %q", s.Given.Dependency, s.Given.Binding)
	}
	got := "does-not-meet"
	if dependency.AcceptsKind(doc.Sources[binding.Source].Kind) {
		got = "meets"
	}
	if got != s.Expected.Outcome {
		return fail("%s; expected %s", got, s.Expected.Outcome)
	}
	return pass(got)
}
