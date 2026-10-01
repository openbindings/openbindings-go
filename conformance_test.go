package openbindings

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/corpus"
)

// This file is the core module's corpus adapter. It executes every corpus
// case whose action needs no schema evaluator: validity fixtures,
// validate-document, resolve-operation, conclude-conformance, and
// check-dependency-kind. The value actions (validate-operation-values,
// check-examples, and format @1's resolve-schema-cycle) run in the schemaeval
// module's adapter, under the project's ECMA-262 evaluator; core's tests
// cannot import it. derive-form has no executor here. Each module records
// every case designated to it, executed or omitted with a reason, and checks
// the count against the corpus manifest.
//
// It reads both scenario formats: @1, the corpus of the specification
// revision this SDK applies, and @2.

type conformanceFixture struct {
	Rule        string            `json:"rule"`
	Section     string            `json:"section"`
	Description string            `json:"description"`
	Tests       []conformanceTest `json:"tests"`
}

type conformanceTest struct {
	Description          string          `json:"description"`
	Document             json.RawMessage `json:"document"`
	DocumentText         *string         `json:"documentText,omitempty"`
	DocumentBase64       string          `json:"documentBase64,omitempty"`
	Valid                bool            `json:"valid"`
	Violates             []string        `json:"violates,omitempty"`
	NotViolated          []string        `json:"notViolated,omitempty"`
	RequiresMinSupported string          `json:"requiresMinSupported,omitempty"`
	RequiresSupports     string          `json:"requiresSupports,omitempty"`
}

// findConformanceCorpus locates the spec repo's conformance/ root: the
// directory OB_SPEC_CORPUS names, else a sibling spec checkout.
func findConformanceCorpus() string { return corpus.Locate(".") }

// sdkDeclaration is this SDK's support declaration, read from
// SupportedVersions and the prereleases it names, never from its version
// decision: gates derived from the code under test could gate its own
// defects out.
func sdkDeclaration() corpus.Declaration {
	line := supportedLine.major
	if supportedLine.major == "0" {
		line += "." + supportedLine.minor
	}
	return corpus.Declaration{Lines: []string{line}, Prereleases: slices.Clone(supportedPrereleases)}
}

// appliedTextRevision and appliedTextSHA256 bind the text this SDK applies:
// the revision of github.com/openbindings/spec it pins, which must be
// appliedRevision, and the sha256 of that revision's openbindings.md.
const appliedTextRevision = "98127021a7e2fa08a8c7b2e6bead1c847c9b6e1f"

const appliedTextSHA256 = "e70cbc8b3b6d4096fd83f694dc093d3ce6d6c87319aeb97b8ae9ec12ebe63a4f"

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
// release named alone (OBI-T-09/c3a) is not verified: no verification against
// a release snapshot exists.
func verifyAppliedText(corpusDir, release, revision, pinnedRevision, pinnedSHA256 string, schema []byte) (bool, string) {
	switch {
	case !isValidSemver(release):
		return false, fmt.Sprintf("appliedRelease %q is not a SemVer 2.0.0 version", release)
	case revision == "":
		return false, "a release named alone (OBI-T-09/c3a) is not verified: no verification against a release snapshot exists"
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
	runRootCorpus(t, dir, true)
}

// runRootCorpus executes the cases designated to the core module, records
// each, and checks that every one was executed or omitted with a reason.
// For the specification's corpus, it reports stale corpusDefects entries on
// their own.
func runRootCorpus(t *testing.T, dir string, specCorpus bool) {
	c, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	verified, why := appliedTextVerified(dir)
	run := rootRun{declaration: sdkDeclaration(), verified: verified, unverified: why}
	var ledger corpus.Ledger
	for _, cs := range c.Cases {
		module, reason := corpus.Designate(cs)
		switch module {
		case corpus.ModuleSchemaeval:
			continue // the schemaeval module records it
		case corpus.ModuleNone:
			ledger.Record(cs.ID, corpus.Judgment{Category: corpus.Omitted, Detail: reason})
			continue
		}
		t.Run(cs.ID+"/"+cs.Description, func(t *testing.T) {
			j := run.judge(cs)
			ledger.Record(cs.ID, j)
			keyed, expected := expectedFailures[cs.ID]
			switch {
			case expected && j.Category == corpus.Fail && j.Detail == keyed.signature:
				t.Logf("keyed expected failure (%s): %s", keyed.reason, j.Detail)
			case expected && j.Category == corpus.Fail:
				t.Errorf("%s (the keyed expected failure's signature is %q)", j.Detail, keyed.signature)
			case expected:
				t.Errorf("the keyed expected failure %s is now %s (%s): remove its expectedFailures entry", cs.ID, j.Category, j.Detail)
			case j.Category == corpus.Pass:
				t.Logf("pass: %s", j.Detail)
			case j.Category == corpus.Advisory:
				t.Logf("ADVISORY (never a failure): %s", j.Detail)
			case j.Category == corpus.Fail:
				t.Error(j.Detail)
			default:
				t.Skipf("%s: %s", j.Category, j.Detail)
			}
		})
	}
	if specCorpus {
		for _, stale := range staleDefects(corpusDefects, c.Cases) {
			t.Error(stale)
		}
	}
	problems, summary := c.Reconcile(corpus.ModuleRoot, &ledger)
	t.Logf("corpus %s\n%s", dir, summary)
	for _, p := range problems {
		t.Error(p)
	}
}

type rootRun struct {
	declaration corpus.Declaration
	verified    bool
	unverified  string
}

func fail(format string, a ...any) corpus.Judgment {
	return corpus.Judgment{Category: corpus.Fail, Detail: fmt.Sprintf(format, a...)}
}

func pass(detail string) corpus.Judgment {
	return corpus.Judgment{Category: corpus.Pass, Detail: detail}
}

func omit(detail string) corpus.Judgment {
	return corpus.Judgment{Category: corpus.Omitted, Detail: detail}
}

func (r rootRun) judge(cs corpus.Case) corpus.Judgment {
	if reason, skip := corpus.Gate(cs.Gates, r.declaration); skip {
		return omit(reason)
	}
	switch cs.Action {
	case corpus.ActionValidity:
		return judgeFixture(cs)
	case "validate-document":
		return r.judgeDocument(cs)
	case "resolve-operation":
		return judgeResolve(cs)
	case "conclude-conformance":
		return judgeConclude(cs)
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

// refusalResidue lists what a version refusal came with: a refusal is
// exclusive of a document, a report, and an established violation.
func refusalResidue(doc *Document, report ValidationReport, err error) []string {
	var out []string
	if doc != nil {
		out = append(out, "a document")
	}
	if !reflect.DeepEqual(report, ValidationReport{}) {
		out = append(out, "a report")
	}
	if errors.As(err, new(*ValidationError)) {
		out = append(out, "a *ValidationError in its error chain")
	}
	return out
}

func isRefusal(err error) bool { return errors.As(err, new(*VersionRefusalError)) }

// judgeFixture holds a validity fixture to ParseDocument followed by
// Document.Validate, and to ValidateDocument's report: a conforming case
// establishes no violation (inconclusive is not non-conformant); a violating
// case is refused (format @1's OBI-T-04) or non-conformant, with every rule
// violates names violated and none notViolated names.
func judgeFixture(cs corpus.Case) corpus.Judgment {
	var tt conformanceTest
	if err := json.Unmarshal(cs.Raw, &tt); err != nil {
		return fail("unreadable fixture: %v", err)
	}
	data, err := conformanceDocumentBytes(tt)
	if err != nil {
		return fail("invalid fixture carriage: %v", err)
	}
	var problems []string
	doc, parseErr := ParseDocument(data)
	var report ValidationReport
	var validateErr error
	if parseErr == nil {
		report, validateErr = doc.Validate()
	}
	// This validator decides every document rule the corpus exercises, so a
	// rule a failing fixture names that comes back inconclusive is a
	// regression, not a capability it lacks.
	if !tt.Valid && parseErr == nil && validateErr == nil {
		for _, rule := range tt.Violates {
			if report.Evidence[rule] == EvidenceInconclusive {
				problems = append(problems, rule+" was left inconclusive")
			}
		}
	}
	switch actual := parseErr == nil && validateErr == nil; {
	case actual == tt.Valid:
	case tt.Valid && parseErr != nil:
		problems = append(problems, fmt.Sprintf("expected valid, got parse error: %v", parseErr))
	case tt.Valid:
		problems = append(problems, fmt.Sprintf("expected valid, got validate error: %v", validateErr))
	default:
		problems = append(problems, "expected invalid, but the SDK accepted the document")
	}
	problems = append(problems, reportAgreesWithFixture(data, tt)...)
	if len(problems) > 0 {
		return fail("%s", strings.Join(problems, "; "))
	}
	return pass("")
}

// reportAgreesWithFixture holds ValidateDocument's report to the same
// fixture.
func reportAgreesWithFixture(data []byte, tt conformanceTest) []string {
	doc, report, err := ValidateDocument(data)
	refused := isRefusal(err)
	var violation *ValidationError
	if err != nil && !refused && !errors.As(err, &violation) {
		return []string{fmt.Sprintf("ValidateDocument: unexpected error %v", err)}
	}
	var problems []string
	if refused {
		if residue := refusalResidue(doc, report, err); len(residue) > 0 {
			problems = append(problems, "ValidateDocument's version refusal came with "+strings.Join(residue, ", "))
		}
	} else if (violation != nil) != (report.Conclusion == ConclusionNonConformant) {
		problems = append(problems, fmt.Sprintf("ValidateDocument error %v disagrees with its report's conclusion %s", err, report.Conclusion))
	}
	if tt.Valid {
		if refused {
			problems = append(problems, fmt.Sprintf("ValidateDocument refused a conforming case: %v", err))
		} else if report.Conclusion == ConclusionNonConformant {
			problems = append(problems, fmt.Sprintf("ValidateDocument established violations %v for a conforming case", report.Violated))
		}
		return problems
	}
	if !refused && report.Conclusion != ConclusionNonConformant {
		problems = append(problems, fmt.Sprintf("ValidateDocument concluded %s for a violating case", report.Conclusion))
	}
	// A version refusal is no validity verdict. Only format @1's refusal
	// fixtures, which list OBI-T-04, expect one; any other violating case
	// expects a non-conformant conclusion.
	if refused && !slices.Contains(tt.Violates, "OBI-T-04") {
		problems = append(problems, fmt.Sprintf("ValidateDocument refused a violating case the fixture holds to document rules: %v", err))
	}
	for _, rule := range tt.Violates {
		switch {
		case rule == "OBI-T-04": // format @1's refusal fixtures
			if !refused {
				problems = append(problems, fmt.Sprintf("expected an OBI-T-04 version refusal; the report concluded %s", report.Conclusion))
			}
		case strings.HasPrefix(rule, "OBI-D-") && !refused:
			if report.Evidence[rule] != EvidenceViolated {
				problems = append(problems, fmt.Sprintf("expected %s violated; its evidence is %q (violated: %v)", rule, report.Evidence[rule], report.Violated))
			}
		}
	}
	if !refused {
		for _, rule := range tt.NotViolated {
			if report.Evidence[rule] == EvidenceViolated {
				problems = append(problems, fmt.Sprintf("%s reported violated; the fixture lists it notViolated (violated: %v)", rule, report.Violated))
			}
		}
	}
	return problems
}

type documentCarriage struct {
	Document       json.RawMessage `json:"document"`
	DocumentText   *string         `json:"documentText"`
	DocumentBase64 string          `json:"documentBase64"`
}

func (g documentCarriage) bytes() ([]byte, error) {
	return conformanceDocumentBytes(conformanceTest{Document: g.Document, DocumentText: g.DocumentText, DocumentBase64: g.DocumentBase64})
}

// judgeDocument executes validate-document through ValidateDocument, reading
// its whole response: a refusal is exclusive, at every entry point that
// refuses; a conclusion agrees with its error; and a conclusion names the
// text this SDK applies.
func (r rootRun) judgeDocument(cs corpus.Case) corpus.Judgment {
	var s struct {
		Given    documentCarriage `json:"given"`
		Expected struct {
			Outcome          string            `json:"outcome"`
			Violates         []string          `json:"violates"`
			NamesAppliedText bool              `json:"namesAppliedText"`
			DuplicateBlind   map[string]string `json:"duplicateBlind"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	data, err := s.Given.bytes()
	if err != nil {
		return fail("%v", err)
	}
	// This SDK detects repeated member names, so duplicateBlind, which
	// states what a tool that cannot detect them must do, does not apply.
	doc, report, err := ValidateDocument(data)
	if isRefusal(err) {
		if residue := refusalResidue(doc, report, err); len(residue) > 0 {
			return fail("ValidateDocument's version refusal came with %s", strings.Join(residue, ", "))
		}
		if s.Expected.Outcome != "version-refusal" {
			return fail("version-refusal; expected %s", s.Expected.Outcome)
		}
		// Every entry point that refuses refuses exclusively.
		if parsed, perr := ParseDocument(data); !isRefusal(perr) || parsed != nil || errors.As(perr, new(*ValidationError)) {
			return fail("ParseDocument does not refuse exclusively: document %v, error %v", parsed != nil, perr)
		}
		var model Document
		if json.Unmarshal(data, &model) == nil {
			if mreport, merr := model.Validate(); !isRefusal(merr) || !reflect.DeepEqual(mreport, ValidationReport{}) || errors.As(merr, new(*ValidationError)) {
				return fail("Document.Validate does not refuse exclusively: report %+v, error %v", mreport, merr)
			}
		}
		return pass("version-refusal")
	}
	if s.Expected.Outcome == "version-refusal" {
		return fail("concluded %s; expected version-refusal", report.Conclusion)
	}
	var violation *ValidationError
	if err != nil && !errors.As(err, &violation) {
		return fail("ValidateDocument: unexpected error %v", err)
	}
	if (violation != nil) != (report.Conclusion == ConclusionNonConformant) {
		return fail("the error %v disagrees with the conclusion %s", err, report.Conclusion)
	}
	switch want := s.Expected.Outcome; {
	case want == "interpreted":
		if report.Conclusion == "" {
			return fail("no conclusion")
		}
	case want == "conformant" && report.Conclusion == ConclusionConformanceUndetermined:
	case string(report.Conclusion) != want:
		return fail("concluded %s; expected %s", report.Conclusion, want)
	}
	for _, rule := range s.Expected.Violates {
		if report.Evidence[rule] != EvidenceViolated {
			return fail("%s is %q, not violated (violated: %v)", rule, report.Evidence[rule], report.Violated)
		}
	}
	if s.Expected.NamesAppliedText {
		if declared, ok := declaredVersion(data); ok && includedPrerelease(declared) {
			// A conclusion on a document declaring an explicitly included
			// prerelease names that prerelease (OBI-T-09). The corpus holds
			// no text for a draft, so there is no text to verify.
			if named, err := parseSemverStrict(report.Version); err != nil || compareSemver(named, mustSemver(declared)) != 0 {
				return fail("names %q; a conclusion on a document declaring the included prerelease %q names that prerelease", report.Version, declared)
			}
			return pass(string(report.Conclusion))
		}
		return judgeNaming(report, appliedRelease, appliedRevision, r.verified, r.unverified, corpus.Required())
	}
	return pass(string(report.Conclusion))
}

// judgeNaming holds a conclusion to the applied text this SDK declares: the
// name it gives is compared first, always, and then the text it names must
// have been verified.
func judgeNaming(report ValidationReport, release, revision string, verified bool, unverified string, required bool) corpus.Judgment {
	if report.Version != release || report.Revision != revision {
		return fail("names %q@%q; the applied text is %q@%q", report.Version, report.Revision, release, revision)
	}
	if !verified {
		if required {
			return fail("UNVERIFIED applied text: %s", unverified)
		}
		return corpus.Judgment{Category: corpus.Unverified, Detail: unverified}
	}
	return pass(string(report.Conclusion))
}

// includedPrerelease reports whether v is a prerelease this SDK names
// explicitly in supportedPrereleases.
func includedPrerelease(v string) bool {
	parsed, err := parseSemverStrict(v)
	if err != nil || len(parsed.preRelease) == 0 {
		return false
	}
	return slices.ContainsFunc(supportedPrereleases, func(p string) bool {
		return compareSemver(parsed, mustSemver(p)) == 0
	})
}

func mustSemver(v string) semver {
	parsed, err := parseSemverStrict(v)
	if err != nil {
		panic(err)
	}
	return parsed
}

// continuesWithNonConformant is this adapter's declaration for the core:
// ValidateDocument returns the document with a non-conformant report
// whenever the model carries it (its fifth result shape), so a tool built on
// it continues with every non-conformant document it can carry (§10.3).
// Holding the core to that declaration, an omission because no document
// came back fails unless its case is keyed in uncarried.
const continuesWithNonConformant = true

// uncarried keys the cases whose non-conformant document the model cannot
// carry, so the core does not continue with it, each with why. None does.
var uncarried = map[string]string{}

// uncarriedOmission judges a case for which ValidateDocument returned no
// document: a failure for a conformant document; for a non-conformant one,
// an omission only when the case is keyed in uncarried.
func uncarriedOmission(id string, nonConformant []string, err error) corpus.Judgment {
	if len(nonConformant) == 0 {
		return fail("the model does not carry a conformant document (%v)", err)
	}
	if why, keyed := uncarried[id]; keyed || !continuesWithNonConformant {
		return omit("the model cannot carry this non-conformant document, so this SDK does not continue with it: " + why)
	}
	return fail("ValidateDocument returned no document for this non-conformant document, though the core declares it continues with every non-conformant document the model carries (key the case in uncarried if the model cannot carry it): %v", err)
}

// staleUncarried fails a case keyed in uncarried whose document came back.
func staleUncarried(id string) (corpus.Judgment, bool) {
	if _, keyed := uncarried[id]; keyed {
		return fail("the keyed uncarried case %s now gets a document: remove its uncarried entry", id), true
	}
	return corpus.Judgment{}, false
}

// judgeResolve executes resolve-operation: the model ValidateDocument
// returns, Document.ResolveOperation, and Document.OperationBindings.
func judgeResolve(cs corpus.Case) corpus.Judgment {
	var s struct {
		Given struct {
			Document      json.RawMessage `json:"document"`
			NonConformant []string        `json:"nonConformant"`
			Name          string          `json:"name"`
		} `json:"given"`
		Expected struct {
			Outcome      string   `json:"outcome"`
			OperationKey string   `json:"operationKey"`
			BindingKeys  []string `json:"bindingKeys"`
			KeyMatch     string   `json:"keyMatch"`
			AliasMatch   string   `json:"aliasMatch"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	doc, report, err := ValidateDocument(s.Given.Document)
	if isRefusal(err) {
		if residue := refusalResidue(doc, report, err); len(residue) > 0 {
			return fail("the version refusal came with %s", strings.Join(residue, ", "))
		}
		if s.Expected.Outcome == "version-refusal" {
			return pass("version-refusal")
		}
		return fail("version-refusal; expected %s", s.Expected.Outcome)
	}
	if s.Expected.Outcome == "version-refusal" {
		return fail("interpreted; expected version-refusal")
	}
	if doc == nil {
		return uncarriedOmission(cs.ID, s.Given.NonConformant, err)
	}
	if j, stale := staleUncarried(cs.ID); stale {
		return j
	}
	key, _, found := doc.ResolveOperation(s.Given.Name)
	switch s.Expected.Outcome {
	case "collision":
		role := "no single resolution"
		switch {
		case found && key == s.Expected.KeyMatch:
			role = "key match " + key
		case found && key == s.Expected.AliasMatch:
			role = "alias match " + key
		case found:
			return fail("resolved to %q, neither candidate", key)
		}
		return corpus.Judgment{Category: corpus.Advisory, Detail: role}
	case "not-found":
		if found {
			return fail("resolved to %q; expected not-found", key)
		}
		return pass("not-found")
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
// the run, telling the reader to remove its entry.
var expectedFailures = map[string]keyedFailure{
	"document/OBI-D-10.json#/tests/22": {
		signature: "OBI-D-13 reported violated; the fixture lists it notViolated (violated: [OBI-D-10 OBI-D-13])",
		reason:    "needs G: S3, only an $anchor matching JSON Schema Core section 8.2.2's grammar declares a plain name (section 7.3)",
	},
	"document/OBI-D-12.json#/tests/40": {
		signature: `expected OBI-D-12 violated; its evidence is "satisfied" (violated: [OBI-D-10])`,
		reason:    "needs G: S3, only an $anchor matching JSON Schema Core section 8.2.2's grammar declares a plain name (section 7.3)",
	},
}

// corpusDefect is a defect a conclude-conformance scenario can have, which
// makes its expected conclusion one the specification text contradicts.
type corpusDefect struct {
	// condition says what makes a scenario have the defect.
	condition string
	// found reports whether a scenario has the defect, and how it has it.
	found func(evidence map[string]RuleEvidenceStatus, expected string) (how string, has bool)
	// corrected is the conclusion the text requires while the defect stands.
	corrected ConformanceConclusion
}

// expectsConformantWithoutEveryRule is a scenario expecting conformant from
// evidence that omits a document rule: OBI-T-09 permits a conformant
// conclusion only when every applicable document rule has been established
// with no violation, and every document rule applies, so the text requires
// conformance undetermined.
var expectsConformantWithoutEveryRule = corpusDefect{
	condition: "the scenario expects conformant from evidence that omits a document rule",
	found: func(evidence map[string]RuleEvidenceStatus, expected string) (string, bool) {
		var missing []string
		for _, rule := range DocumentRules() {
			if _, given := evidence[rule]; !given {
				missing = append(missing, rule)
			}
		}
		if expected != string(ConclusionConformant) || len(missing) == 0 {
			return "", false
		}
		return fmt.Sprintf("it expects conformant, but its evidence omits %s; OBI-T-09 permits a conformant conclusion only when every applicable document rule has been established with no violation", strings.Join(missing, ", ")), true
	},
	corrected: ConclusionConformanceUndetermined,
}

// corpusDefects are the format @1 scenarios, in the corpus of the
// specification revision this SDK applies, whose expected conclusion the
// specification text contradicts, keyed by scenario ID, each with its defect.
// While a defect holds, its scenario is held to the corrected conclusion and
// then reported as a keyed expected failure, a skip, never a pass. They
// concern format @1 only: a format @2 scenario is judged by its own
// expectation. An entry whose defect no longer holds, or whose @1 scenario
// is gone, is reported on its own (staleDefects), never in place of a
// scenario's judgment. Retiring a defect is removing its entry, nothing
// else.
var corpusDefects = map[string]corpusDefect{
	// One entry per line: a scenario ID and its defect.
	"T09-S-01": expectsConformantWithoutEveryRule,
}

// concludeExpectation returns the conclusion a format @1 conclude-conformance
// scenario must reach under defects: the corrected one, with why, for a keyed
// corpus defect that holds, and otherwise the scenario's own.
func concludeExpectation(defects map[string]corpusDefect, id string, evidence map[string]RuleEvidenceStatus, expected string) (want ConformanceConclusion, defect string) {
	if keyed, known := defects[id]; known {
		if how, has := keyed.found(evidence, expected); has {
			return keyed.corrected, how
		}
	}
	return ConformanceConclusion(expected), ""
}

// staleDefects reports each entry of defects that names no format @1
// scenario of the corpus, or one whose defect no longer holds. For a corpus
// holding no format @1 scenario, the entries do not apply.
func staleDefects(defects map[string]corpusDefect, cases []corpus.Case) []string {
	v1 := map[string]corpus.Case{}
	for _, cs := range cases {
		if cs.Format == corpus.FormatV1 {
			v1[cs.ID] = cs
		}
	}
	if len(v1) == 0 {
		return nil
	}
	var out []string
	for _, id := range slices.Sorted(maps.Keys(defects)) {
		cs, held := v1[id]
		if !held {
			out = append(out, fmt.Sprintf("corpusDefects names %s, which the corpus does not hold in format @1", id))
			continue
		}
		evidence, expected, err := concludeGiven(cs)
		if err != nil {
			out = append(out, fmt.Sprintf("corpusDefects names %s: %v", id, err))
			continue
		}
		if _, has := defects[id].found(evidence, expected); !has {
			out = append(out, fmt.Sprintf("the keyed corpus defect %s no longer holds (its condition: %s): remove its corpusDefects entry", id, defects[id].condition))
		}
	}
	return out
}

func concludeGiven(cs corpus.Case) (map[string]RuleEvidenceStatus, string, error) {
	var s struct {
		Given struct {
			Evidence map[string]RuleEvidenceStatus `json:"evidence"`
		} `json:"given"`
		Expected struct {
			Conclusion string `json:"conclusion"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return nil, "", fmt.Errorf("unreadable scenario: %v", err)
	}
	return s.Given.Evidence, s.Expected.Conclusion, nil
}

// judgeConclude executes conclude-conformance through ConcludeConformance.
// Format @2's conformant admits conformance-undetermined, as
// validate-document's does: OBI-T-09 only prohibits. Format @1 expects its
// conclusion exactly, with the keyed corpus defects.
func judgeConclude(cs corpus.Case) corpus.Judgment { return judgeConcludeUnder(corpusDefects, cs) }

// judgeConcludeUnder is judgeConclude under defects.
func judgeConcludeUnder(defects map[string]corpusDefect, cs corpus.Case) corpus.Judgment {
	evidence, expected, err := concludeGiven(cs)
	if err != nil {
		return fail("%v", err)
	}
	report := ConcludeConformance(evidence)
	if cs.Format != corpus.FormatV1 {
		if string(report.Conclusion) != expected && !(expected == string(ConclusionConformant) && report.Conclusion == ConclusionConformanceUndetermined) {
			return fail("concluded %s; expected %s", report.Conclusion, expected)
		}
		return pass(string(report.Conclusion))
	}
	want, defect := concludeExpectation(defects, cs.ID, evidence, expected)
	if report.Conclusion != want {
		return fail("concluded %s; expected %s", report.Conclusion, want)
	}
	if defect != "" {
		return omit(fmt.Sprintf("keyed corpus defect %s: %s; this SDK concludes the corrected %q", cs.ID, defect, want))
	}
	return pass(string(report.Conclusion))
}

// judgeKind executes check-dependency-kind: the model ValidateDocument
// returns and Dependency.AcceptsKind, with the kind compared never retrieved:
// a retrieval sentinel's channel is observed for the whole action, loading
// included.
func judgeKind(cs corpus.Case) corpus.Judgment {
	var s struct {
		Given struct {
			Document      json.RawMessage `json:"document"`
			NonConformant []string        `json:"nonConformant"`
			Dependency    string          `json:"dependency"`
			Binding       string          `json:"binding"`
			Sentinels     []string        `json:"retrievalSentinels"`
		} `json:"given"`
		Expected struct {
			Outcome string `json:"outcome"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	data := []byte(s.Given.Document)
	var observe *sentinels
	if len(s.Given.Sentinels) > 0 {
		var err error
		observe, err = startSentinels(s.Given.Sentinels)
		if err != nil {
			return omit(err.Error())
		}
		data = observe.substitute(data)
	}
	meets, refused, carried, err := func() (bool, bool, bool, error) {
		doc, _, err := ValidateDocument(data)
		if isRefusal(err) {
			return false, true, false, nil
		}
		if doc == nil {
			return false, false, false, err
		}
		binding := doc.Bindings[s.Given.Binding]
		return doc.Dependencies[s.Given.Dependency].AcceptsKind(doc.Sources[binding.Source].Kind), false, true, nil
	}()
	if observe != nil {
		if seen := observe.stop(); seen != "" {
			return fail("%s", seen)
		}
	}
	switch {
	case refused:
		return fail("version-refusal; expected %s", s.Expected.Outcome)
	case !carried:
		return uncarriedOmission(cs.ID, s.Given.NonConformant, err)
	}
	if j, stale := staleUncarried(cs.ID); stale {
		return j
	}
	got := "does-not-meet"
	if meets {
		got = "meets"
	}
	if got != s.Expected.Outcome {
		return fail("%s; expected %s", got, s.Expected.Outcome)
	}
	return pass(got)
}

// successor returns the SemVer numeric identifier one greater than n.
func successor(n string) string {
	value, _ := new(big.Int).SetString(n, 10)
	return value.Add(value, big.NewInt(1)).String()
}

// The gates are judged against SupportedVersions' declaration: the authoring
// version and a later patch of its line are administered; the next major,
// and while pre-1.0 the next minor, are not. End to end, a poisoned case
// gated out is never run, and a gated-in one is.
func TestConformanceGates(t *testing.T) {
	authoring, _ := parseSemverStrict(AuthoringVersion)
	nextMajor := successor(authoring.major) + ".0.0"
	higherPatch := authoring.major + "." + authoring.minor + "." + successor(authoring.patch)
	declaration := sdkDeclaration()
	for v, want := range map[string]bool{AuthoringVersion: true, higherPatch: true, nextMajor: false, AuthoringVersion + "-rc.1": false} {
		if got := declaration.Supports(v); got != want {
			t.Errorf("declaration supports %s: %v, want %v", v, got, want)
		}
		// The declaration and the version decision agree on each.
		if refused := isRefusal(CheckVersion(v)); refused == want {
			t.Errorf("CheckVersion(%s) refused %v, but the declaration supports it %v", v, refused, want)
		}
	}
	if authoring.major == "0" {
		if nextMinor := "0." + successor(authoring.minor) + ".0"; declaration.Supports(nextMinor) {
			t.Errorf("declaration supports %s", nextMinor)
		}
	}

	dir := t.TempDir()
	fixture := fmt.Sprintf(`{"rule": "OBI-D-02", "section": "10.2", "description": "gates", "tests": [
		{"description": "outside the declaration (poisoned: fails if administered)", "document": {}, "valid": true, "requiresSupports": %q},
		{"description": "inside the declaration", "document": {}, "valid": false, "requiresSupports": %q}]}`, nextMajor, higherPatch)
	manifest := `{"files": [{"path": "document/gates.json", "tests": 2}], "scenarioFiles": []}`
	if err := os.MkdirAll(filepath.Join(dir, "document"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"document/gates.json": fixture, "manifest.json": manifest} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runRootCorpus(t, dir, false)
}

// A keyed corpus defect holds a format @1 scenario to the corrected
// conclusion while the defect stands. Once the corpus is corrected, the
// scenario is judged by its own expectation and the entry is reported stale
// on its own, never in place of the judgment; a format @2 scenario of the
// same ID is judged by its own expectation, and the entries do not apply to
// a corpus with no format @1 scenario. The mechanism is tested on synthetic
// scenarios, so retiring a live entry needs no edit here.
func TestCorpusDefects_Retire(t *testing.T) {
	defects := map[string]corpusDefect{"SYNTHETIC-01": expectsConformantWithoutEveryRule}
	evidence := allRules(EvidenceSatisfied)
	evidence["OBI-D-11"] = EvidenceNotApplicable
	defective := maps.Clone(evidence)
	delete(defective, "OBI-D-12")
	delete(defective, "OBI-D-13")
	want, defect := concludeExpectation(defects, "SYNTHETIC-01", defective, "conformant")
	if want != ConclusionConformanceUndetermined || !strings.Contains(defect, "omits OBI-D-12, OBI-D-13") {
		t.Fatalf("the defect as it stands: %q, %q", want, defect)
	}
	scenario := func(format string, evidence map[string]RuleEvidenceStatus, expected string) corpus.Case {
		raw, _ := json.Marshal(map[string]any{"id": "SYNTHETIC-01", "given": map[string]any{"evidence": evidence}, "expected": map[string]any{"conclusion": expected}})
		return corpus.Case{ID: "SYNTHETIC-01", Format: format, Action: "conclude-conformance", Raw: raw}
	}
	if j := judgeConcludeUnder(defects, scenario(corpus.FormatV1, defective, "conformant")); j.Category != corpus.Omitted {
		t.Errorf("the defective @1 scenario: %+v", j)
	}
	if stale := staleDefects(defects, []corpus.Case{scenario(corpus.FormatV1, defective, "conformant")}); len(stale) != 0 {
		t.Errorf("a defect that holds is reported stale: %v", stale)
	}
	corrected := scenario(corpus.FormatV1, evidence, "conformant")
	if j := judgeConcludeUnder(defects, corrected); j.Category != corpus.Pass {
		t.Errorf("a corrected @1 scenario is judged by its own expectation: %+v", j)
	}
	if stale := staleDefects(defects, []corpus.Case{corrected}); len(stale) != 1 || !strings.Contains(stale[0], "its condition: the scenario expects conformant") || !strings.Contains(stale[0], "remove its corpusDefects entry") {
		t.Errorf("a corrected @1 scenario's entry: %v", stale)
	}
	if stale := staleDefects(defects, []corpus.Case{scenario(corpus.FormatV1, defective, "conformant"), {ID: "T01-S-01", Format: corpus.FormatV1}}); len(stale) != 0 {
		t.Errorf("stale: %v", stale)
	}
	if stale := staleDefects(defects, []corpus.Case{{ID: "T01-S-01", Format: corpus.FormatV1}}); len(stale) != 1 || !strings.Contains(stale[0], "does not hold in format @1") {
		t.Errorf("an @1 corpus without the scenario: %v", stale)
	}
	v2 := scenario(corpus.FormatV2, defective, "conformant")
	if j := judgeConcludeUnder(defects, v2); j.Category != corpus.Pass {
		t.Errorf("an @2 scenario is judged by its own expectation (conformant admits conformance-undetermined): %+v", j)
	}
	if stale := staleDefects(defects, []corpus.Case{v2}); len(stale) != 0 {
		t.Errorf("the entries do not apply to a corpus with no @1 scenario: %v", stale)
	}
	if j := judgeConcludeUnder(defects, scenario(corpus.FormatV2, defective, "non-conformant")); j.Category != corpus.Fail {
		t.Errorf("an @2 scenario expecting non-conformant from evidence with no violation: %+v", j)
	}
}
