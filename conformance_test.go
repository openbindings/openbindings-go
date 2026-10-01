package openbindings

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
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

// appliedTextSHA256 is the sha256 of openbindings.md at appliedRevision, the
// text this SDK applies. With the embedded schema it verifies the identity
// the SDK's conclusions name against the corpus's own specification text.
const appliedTextSHA256 = "e70cbc8b3b6d4096fd83f694dc093d3ce6d6c87319aeb97b8ae9ec12ebe63a4f"

// appliedTextVerified reports whether the specification beside the corpus is
// the text this SDK applies: its openbindings.md hashes to appliedTextSHA256
// and its schema is the one embedded here.
func appliedTextVerified(corpusDir string) (bool, string) {
	text, err := os.ReadFile(filepath.Join(corpusDir, "..", "openbindings.md"))
	if err != nil {
		return false, fmt.Sprintf("the corpus's openbindings.md is unreadable: %v", err)
	}
	schema, err := os.ReadFile(filepath.Join(corpusDir, "..", "openbindings.schema.json"))
	if err != nil {
		return false, fmt.Sprintf("the corpus's openbindings.schema.json is unreadable: %v", err)
	}
	textSum := sha256.Sum256(text)
	if got := hex.EncodeToString(textSum[:]); got != appliedTextSHA256 {
		return false, fmt.Sprintf("the corpus's openbindings.md (sha256 %s) is not the text appliedRevision %s names (sha256 %s)", got, appliedRevision, appliedTextSHA256)
	}
	if string(schema) != string(openbindingsSchemaJSON) {
		return false, "the corpus's openbindings.schema.json is not the schema this SDK embeds"
	}
	return true, ""
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
// For the specification's corpus, every corpusDefects entry must name a case
// the corpus holds.
func runRootCorpus(t *testing.T, dir string, specCorpus bool) {
	c, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	verified, why := appliedTextVerified(dir)
	run := rootRun{declaration: sdkDeclaration(), verified: verified, unverified: why}
	var ledger corpus.Ledger
	seen := map[string]bool{}
	for _, cs := range c.Cases {
		module, reason := corpus.Designate(cs)
		switch module {
		case corpus.ModuleSchemaeval:
			continue // the schemaeval module records it
		case corpus.ModuleNone:
			ledger.Record(cs.ID, corpus.Judgment{Category: corpus.Omitted, Detail: reason})
			continue
		}
		seen[cs.ID] = true
		t.Run(cs.ID+"/"+cs.Description, func(t *testing.T) {
			j := run.judge(cs)
			ledger.Record(cs.ID, j)
			switch j.Category {
			case corpus.Pass:
				t.Logf("pass: %s", j.Detail)
			case corpus.Advisory:
				t.Logf("ADVISORY (never a failure): %s", j.Detail)
			case corpus.Fail:
				t.Error(j.Detail)
			default:
				t.Skipf("%s: %s", j.Category, j.Detail)
			}
		})
	}
	for _, id := range slices.Sorted(maps.Keys(corpusDefects)) {
		if specCorpus && !seen[id] {
			t.Errorf("corpusDefects names %s, which the corpus does not hold", id)
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
		if report.Version != appliedRelease || report.Revision != appliedRevision {
			return fail("names %q@%q; the applied text is %q@%q", report.Version, report.Revision, appliedRelease, appliedRevision)
		}
		if !r.verified {
			if corpus.Required() {
				return fail("UNVERIFIED applied text: %s", r.unverified)
			}
			return corpus.Judgment{Category: corpus.Unverified, Detail: r.unverified}
		}
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
		if len(s.Given.NonConformant) == 0 {
			return fail("the model does not carry a conformant document (%v)", err)
		}
		return omit("the model cannot carry this non-conformant document, so this SDK does not continue with it")
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

// corpusDefects are the scenarios, in the corpus of the specification
// revision this SDK applies, whose expected conclusion the specification
// text contradicts, keyed by scenario ID, each with its defect. While a
// defect holds, its scenario is held to the corrected conclusion and then
// reported as a keyed expected failure, a skip, never a pass. An entry fails
// once its defect no longer holds, telling the reader to remove it, and once
// its scenario is gone. Retiring a defect is removing its entry, nothing
// else.
var corpusDefects = map[string]corpusDefect{
	// One entry per line: a scenario ID and its defect.
	"T09-S-01": expectsConformantWithoutEveryRule,
}

// concludeExpectation returns the conclusion a conclude-conformance scenario
// must reach under defects: the corrected one, with why, for a keyed corpus
// defect that still holds, and otherwise the scenario's own. It returns an
// error for a keyed defect that no longer holds.
func concludeExpectation(defects map[string]corpusDefect, id string, evidence map[string]RuleEvidenceStatus, expected string) (want ConformanceConclusion, defect string, err error) {
	keyed, known := defects[id]
	if !known {
		return ConformanceConclusion(expected), "", nil
	}
	how, has := keyed.found(evidence, expected)
	if !has {
		return "", "", fmt.Errorf("the keyed corpus defect %s no longer holds (its condition: %s): remove its corpusDefects entry", id, keyed.condition)
	}
	return keyed.corrected, how, nil
}

// judgeConclude executes conclude-conformance through ConcludeConformance,
// in either format.
func judgeConclude(cs corpus.Case) corpus.Judgment {
	var s struct {
		Given struct {
			Evidence map[string]RuleEvidenceStatus `json:"evidence"`
		} `json:"given"`
		Expected struct {
			Conclusion string `json:"conclusion"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	report := ConcludeConformance(s.Given.Evidence)
	want, defect, err := concludeExpectation(corpusDefects, cs.ID, s.Given.Evidence, s.Expected.Conclusion)
	if err != nil {
		return fail("%v", err)
	}
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
	case !carried && len(s.Given.NonConformant) == 0:
		return fail("the model does not carry a conformant document (%v)", err)
	case !carried:
		return omit("the model cannot carry this non-conformant document, so this SDK does not continue with it")
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

// trapTransport counts http requests instead of sending them.
type trapTransport struct{ attempts *int }

func (t trapTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	*t.attempts++
	return nil, fmt.Errorf("retrieval of %s is observed and refused by the corpus adapter", r.URL)
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

// A keyed corpus defect holds the scenario to the corrected conclusion while
// the defect stands, and tells the reader to remove its entry once the corpus
// is corrected, whatever the corrected scenario expects. The mechanism is
// tested on a synthetic scenario, so retiring a live entry needs no edit
// here.
func TestCorpusDefects_Retire(t *testing.T) {
	defects := map[string]corpusDefect{"SYNTHETIC-01": expectsConformantWithoutEveryRule}
	evidence := allRules(EvidenceSatisfied)
	evidence["OBI-D-11"] = EvidenceNotApplicable
	defective := maps.Clone(evidence)
	delete(defective, "OBI-D-12")
	delete(defective, "OBI-D-13")
	want, defect, err := concludeExpectation(defects, "SYNTHETIC-01", defective, "conformant")
	if err != nil || want != ConclusionConformanceUndetermined || !strings.Contains(defect, "omits OBI-D-12, OBI-D-13") {
		t.Fatalf("the defect as it stands: %q, %q, %v", want, defect, err)
	}
	for _, expected := range []string{"conformant", "conformance-undetermined"} {
		corrected := evidence
		if expected != "conformant" {
			corrected = defective
		}
		if _, _, err := concludeExpectation(defects, "SYNTHETIC-01", corrected, expected); err == nil || !strings.Contains(err.Error(), "its condition: the scenario expects conformant") || !strings.Contains(err.Error(), "remove its corpusDefects entry") {
			t.Errorf("a corrected scenario expecting %s: %v", expected, err)
		}
	}
	if want, defect, err := concludeExpectation(defects, "SYNTHETIC-02", defective, "conformance-undetermined"); want != ConclusionConformanceUndetermined || defect != "" || err != nil {
		t.Errorf("a scenario no entry names: %q, %q, %v", want, defect, err)
	}
}
