package openbindings

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

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
	RequiresMinSupported string          `json:"requiresMinSupported,omitempty"`
	RequiresSupports     string          `json:"requiresSupports,omitempty"`
}

// conformanceSkip evaluates a test's version-gate annotations against this
// SDK's support declaration. A non-empty result means the test must not be
// administered to this SDK; the harness reports it via t.Skip. Skips are
// never failures; they surface separately in the test output. An annotation
// that fails to parse gates nothing (the test runs).
func conformanceSkip(tt conformanceTest) (reason string, skip bool) {
	if tt.RequiresMinSupported != "" {
		// Downward-refusal tests apply only when the lowest version this SDK
		// supports is at or above the annotation's value. The lowest is read
		// from the declaration, SupportedVersions, not from the refusal code
		// the test exercises.
		if annotated, err := parseSemverStrict(tt.RequiresMinSupported); err == nil && compareSemver(lowestSupported(), annotated) < 0 {
			return fmt.Sprintf("requires the lowest supported version to be at least %s", tt.RequiresMinSupported), true
		}
	}
	if tt.RequiresSupports != "" {
		// Administer the test only to tools whose OBI-T-04
		// version-acceptance predicate accepts the annotated version; for
		// this SDK that predicate is IsSupportedVersion. Anything the SDK
		// would refuse to process is a skip.
		accepted, err := IsSupportedVersion(tt.RequiresSupports)
		if err == nil && !accepted {
			return fmt.Sprintf("requires supported version %s", tt.RequiresSupports), true
		}
	}
	return "", false
}

func TestConformanceCorpus(t *testing.T) {
	corpusDir := findConformanceCorpus()

	if corpusDir == "" {
		// OB_CORPUS_REQUIRED (set in CI) turns a missing corpus into a hard
		// failure so a mis-wired path turns CI red instead of silently green;
		// unset (local dev) it still skips.
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatal("spec conformance corpus not found (OB_CORPUS_REQUIRED is set; set OB_SPEC_CORPUS to the spec repo's conformance dir)")
		}
		t.Skip("spec conformance corpus not found")
	}

	for _, subdir := range []string{"document", "tool"} {
		t.Run(subdir, func(t *testing.T) {
			runConformanceDir(t, filepath.Join(corpusDir, subdir))
		})
	}
	t.Run("scenarios", func(t *testing.T) {
		runCoreToolScenarioDir(t, filepath.Join(corpusDir, "scenarios"))
	})
}

// findConformanceCorpus locates the spec repo's conformance/ root. It honors
// OB_SPEC_CORPUS first, then falls back to the local-dev sibling path —
// mirroring the family/selection/comparison harnesses.
func findConformanceCorpus() string {
	candidates := make([]string, 0, 3)
	if env := os.Getenv("OB_SPEC_CORPUS"); env != "" {
		candidates = append(candidates, env)
	}
	candidates = append(candidates,
		filepath.Join("..", "spec", "conformance"),
		filepath.Join("spec", "conformance"),
	)
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
	}
	return ""
}

func runConformanceDir(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}

	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		var fix conformanceFixture
		if err := json.Unmarshal(data, &fix); err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}

		for _, tt := range fix.Tests {
			tt := tt
			name := fix.Rule + "/" + tt.Description
			t.Run(name, func(t *testing.T) {
				if reason, skip := conformanceSkip(tt); skip {
					t.Skip(reason)
				}

				documentBytes, inputErr := conformanceDocumentBytes(tt)
				if inputErr != nil {
					t.Fatalf("invalid fixture carriage: %v", inputErr)
				}
				iface, parseErr := ParseDocument(documentBytes)
				var validateErr error
				if parseErr == nil {
					_, validateErr = iface.Validate(ValidateOptions{})
				}
				actualValid := parseErr == nil && validateErr == nil

				if actualValid != tt.Valid {
					if tt.Valid {
						if parseErr != nil {
							t.Errorf("expected valid, got parse error: %v", parseErr)
						} else {
							t.Errorf("expected valid, got validate error: %v", validateErr)
						}
					} else {
						t.Errorf("expected invalid, but SDK accepted the document")
					}
				}
				assertReportAgreesWithFixture(t, documentBytes, tt)
			})
		}
	}
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

type coreToolScenarioFile struct {
	Rule      string            `json:"rule"`
	Scenarios []json.RawMessage `json:"scenarios"`
}

type coreToolScenarioHeader struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Action      string `json:"action"`
}

func runCoreToolScenarioDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading tool scenarios: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		var file coreToolScenarioFile
		if err := json.Unmarshal(data, &file); err != nil {
			t.Fatalf("parsing %s: %v", entry.Name(), err)
		}
		for _, raw := range file.Scenarios {
			var header coreToolScenarioHeader
			if err := json.Unmarshal(raw, &header); err != nil {
				t.Fatalf("parsing scenario header in %s: %v", entry.Name(), err)
			}
			raw := raw
			t.Run(file.Rule+"/"+header.ID+"/"+header.Description, func(t *testing.T) {
				switch header.Action {
				case "resolve-operation":
					testResolveOperationScenario(t, raw)
				case "resolve-schema-cycle":
					testSchemaCycleScenario(t, raw)
				case "validate-operation-values":
					testValidateValuesScenario(t, raw)
				case "conclude-conformance":
					testConcludeConformanceScenario(t, raw)
				default:
					t.Fatalf("unsupported scenario action %q", header.Action)
				}
			})
		}
	}
}

func testResolveOperationScenario(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var scenario struct {
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
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	iface, _, err := ValidateDocument(scenario.Given.Document, ValidateOptions{})
	if err != nil {
		t.Fatalf("scenario document: %v", err)
	}
	key, _, found := ResolveOperation(iface, scenario.Given.Name)
	if scenario.Expected.Outcome == "not-found" {
		if found {
			t.Fatalf("resolved to %q; expected not-found", key)
		}
		return
	}
	if !found || key != scenario.Expected.OperationKey {
		t.Fatalf("resolved (%q, %v); expected %q", key, found, scenario.Expected.OperationKey)
	}
	var bindings []string
	for bindingKey, binding := range iface.Bindings {
		if binding.Operation == key {
			bindings = append(bindings, bindingKey)
		}
	}
	sort.Strings(bindings)
	expected := append([]string(nil), scenario.Expected.BindingKeys...)
	sort.Strings(expected)
	if !slices.Equal(bindings, expected) {
		t.Fatalf("binding keys %v; expected %v", bindings, expected)
	}
}

func testSchemaCycleScenario(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var scenario struct {
		Given struct {
			Document  json.RawMessage `json:"document"`
			Operation string          `json:"operation"`
			Side      string          `json:"side"`
			Value     any             `json:"value"`
		} `json:"given"`
		Expected struct {
			AllowedOutcomes []string `json:"allowedOutcomes"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	iface, _, err := ValidateDocument(scenario.Given.Document, ValidateOptions{})
	if err != nil {
		t.Fatalf("scenario document: %v", err)
	}
	operationKey, operation, found := ResolveOperation(iface, scenario.Given.Operation)
	if !found {
		t.Fatalf("operation %q not found", scenario.Given.Operation)
	}
	schema := operation.Input
	if scenario.Given.Side == "output" {
		schema = operation.Output
	}
	if schema == nil {
		t.Fatal("operation side has no schema")
	}
	outcome := make(chan string, 1)
	go func() {
		var err error
		if scenario.Given.Side == "output" {
			err = ValidateOperationOutput(scenario.Given.Value, iface, operationKey)
		} else {
			err = ValidateOperationInput(scenario.Given.Value, iface, operationKey)
		}
		outcome <- contractOutcome(err, "resolver-error")
	}()
	select {
	case got := <-outcome:
		if !slices.Contains(scenario.Expected.AllowedOutcomes, got) {
			t.Fatalf("outcome %q not in permitted set %v", got, scenario.Expected.AllowedOutcomes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("schema-cycle resolution did not terminate within 2 seconds")
	}
}

func testValidateValuesScenario(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var scenario struct {
		Given struct {
			Document  json.RawMessage `json:"document"`
			Operation string          `json:"operation"`
			Side      string          `json:"side"`
			Values    []any           `json:"values"`
		} `json:"given"`
		Expected struct {
			Results []string `json:"results"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	iface, _, err := ValidateDocument(scenario.Given.Document, ValidateOptions{})
	if err != nil {
		t.Fatalf("scenario document: %v", err)
	}
	operationKey, operation, found := ResolveOperation(iface, scenario.Given.Operation)
	if !found {
		t.Fatalf("operation %q not found", scenario.Given.Operation)
	}
	schema := operation.Input
	if scenario.Given.Side == "output" {
		schema = operation.Output
	}
	if schema == nil {
		t.Fatal("operation side has no schema")
	}
	actual := make([]string, 0, len(scenario.Given.Values))
	for _, value := range scenario.Given.Values {
		var err error
		if scenario.Given.Side == "output" {
			err = ValidateOperationOutput(value, iface, operationKey)
		} else {
			err = ValidateOperationInput(value, iface, operationKey)
		}
		actual = append(actual, contractOutcome(err, "graph-unavailable"))
	}
	if !slices.Equal(actual, scenario.Expected.Results) {
		t.Fatalf("results %v; expected %v", actual, scenario.Expected.Results)
	}
}

func testConcludeConformanceScenario(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var scenario struct {
		Given struct {
			Evidence map[string]RuleEvidenceStatus `json:"evidence"`
		} `json:"given"`
		Expected struct {
			Conclusion   string   `json:"conclusion"`
			Violated     []string `json:"violated"`
			Inconclusive []string `json:"inconclusive"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	report := ConcludeConformance(scenario.Given.Evidence)
	expectedViolated := append([]string(nil), scenario.Expected.Violated...)
	expectedInconclusive := append([]string(nil), scenario.Expected.Inconclusive...)
	sort.Strings(expectedViolated)
	sort.Strings(expectedInconclusive)
	if string(report.Conclusion) != scenario.Expected.Conclusion ||
		!slices.Equal(report.Violated, expectedViolated) ||
		!slices.Equal(report.Inconclusive, expectedInconclusive) {
		t.Fatalf("report %#v; expected conclusion=%q violated=%v inconclusive=%v", report, scenario.Expected.Conclusion, expectedViolated, expectedInconclusive)
	}
}

// TestConformanceRequiresSupportsGate exercises the requiresSupports
// annotation with a synthetic fixture, independent of the live spec corpus
// (which need not carry the annotation; an absent annotation gates nothing).
//
// Contract: `requiresSupports: "X.Y.Z"` — administer this test only to tools
// whose OBI-T-04 version-acceptance predicate accepts X.Y.Z; otherwise skip
// and report the skip separately (skips are never failures). For this SDK
// the predicate is IsSupportedVersion.
//
// Annotation versions are derived from the SDK's own constants so the test
// stays correct across version bumps.
func TestConformanceRequiresSupportsGate(t *testing.T) {
	// Always outside acceptance: the next major is refused pre- and
	// post-1.0 alike. Always inside acceptance: the authoring version, and a
	// higher patch of the supported line.
	authoring, _ := parseSemverStrict(AuthoringVersion)
	nextMajor := successor(authoring.major) + ".0.0"
	higherPatch := authoring.major + "." + authoring.minor + "." + successor(authoring.patch)

	cases := []struct {
		annotation string
		wantSkip   bool
	}{
		{AuthoringVersion, false}, // supported → administer
		{higherPatch, false},      // supported → administer
		{nextMajor, true},         // refused major → skip
	}
	if authoring.major == "0" {
		// While pre-1.0, the next minor is refused too.
		nextMinor := "0." + successor(authoring.minor) + ".0"
		cases = append(cases, struct {
			annotation string
			wantSkip   bool
		}{nextMinor, true})
	}
	for _, tc := range cases {
		reason, skip := conformanceSkip(conformanceTest{RequiresSupports: tc.annotation})
		if skip != tc.wantSkip {
			t.Errorf("requiresSupports %s: skip = %v, want %v (reason %q)",
				tc.annotation, skip, tc.wantSkip, reason)
		}
	}

	// End-to-end through the harness with a fixture file in a temp dir. The
	// out-of-acceptance test is poisoned: {} is an invalid document but the
	// fixture claims valid, so it FAILS if administered. Passing therefore
	// proves the annotation was parsed from the file and honored as a skip.
	// The in-acceptance test is administered and passes ({} is correctly
	// rejected).
	fixture := fmt.Sprintf(`{
		"rule": "synthetic-requires-supports",
		"section": "harness-self-test",
		"description": "requiresSupports gating",
		"tests": [
			{
				"description": "outside acceptance is skipped (poisoned: fails if administered)",
				"document": {},
				"valid": true,
				"requiresSupports": %q
			},
			{
				"description": "inside acceptance is administered",
				"document": {},
				"valid": false,
				"requiresSupports": %q
			}
		]
	}`, nextMajor, higherPatch)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requires-supports.json"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("writing synthetic fixture: %v", err)
	}
	runConformanceDir(t, dir)
}

// assertReportAgreesWithFixture holds ValidateDocument's report to the
// same fixture the gate is held to. A conforming case establishes no
// violation (it may still be undetermined: inconclusive is not non-conformant).
// A violating case is either refused (OBI-T-04) or non-conformant, with every
// document rule the fixture names recorded as violated.
func assertReportAgreesWithFixture(t *testing.T, documentBytes []byte, tt conformanceTest) {
	t.Helper()
	_, report, err := ValidateDocument(documentBytes, ValidateOptions{})
	var refusal *VersionRefusalError
	refused := errors.As(err, &refusal)
	var violation *ValidationError
	if err != nil && !refused && !errors.As(err, &violation) {
		t.Errorf("ValidateDocument: unexpected error %v", err)
		return
	}
	if (violation != nil) != (report.Conclusion == ConclusionNonConformant) {
		t.Errorf("ValidateDocument error %v disagrees with its report's conclusion %s", err, report.Conclusion)
	}
	if tt.Valid {
		if refused {
			t.Errorf("ValidateDocument refused a conforming case: %v", err)
		} else if report.Conclusion == ConclusionNonConformant {
			t.Errorf("ValidateDocument established violations %v for a conforming case: %+v", report.Violated, report.Violations())
		}
		return
	}
	if !refused && report.Conclusion != ConclusionNonConformant {
		t.Errorf("ValidateDocument concluded %s for a violating case; findings %+v", report.Conclusion, report.Findings)
	}
	for _, rule := range tt.Violates {
		switch {
		case rule == "OBI-T-04":
			if !refused {
				t.Errorf("expected an OBI-T-04 version refusal; report concluded %s", report.Conclusion)
			}
		case strings.HasPrefix(rule, "OBI-D-") && !refused:
			if report.Evidence[rule] != EvidenceViolated {
				t.Errorf("expected %s violated; its evidence is %q and the violations are %+v", rule, report.Evidence[rule], report.Violations())
			}
		}
	}
}

// successor returns the SemVer numeric identifier one greater than n.
func successor(n string) string {
	value, _ := new(big.Int).SetString(n, 10)
	return value.Add(value, big.NewInt(1)).String()
}

// lowestSupported is the lowest version SupportedVersions declares: the
// first release of the supported line.
func lowestSupported() semver {
	return semver{major: supportedLine.major, minor: cmp.Or(supportedLine.minor, "0"), patch: "0"}
}

// contractOutcome names the outcome of validating a value against an
// operation's contract in the corpus's terms, read from the error's type
// alone: OBI-T-08 keeps a mismatch and an unavailable graph distinct, so what
// a scenario allows never decides which one an error is. unavailable is the
// scenario's name for an unavailable graph.
func contractOutcome(err error, unavailable string) string {
	var mismatch *SchemaValidationError
	var graph *SchemaGraphUnavailableError
	switch {
	case err == nil:
		return "valid"
	case errors.As(err, &mismatch):
		return "instance-mismatch"
	case errors.As(err, &graph):
		return unavailable
	}
	return fmt.Sprintf("an unexpected error: %v", err)
}

func TestContractOutcome(t *testing.T) {
	for want, err := range map[string]error{
		"valid":             nil,
		"instance-mismatch": &SchemaValidationError{},
		"resolver-error":    &SchemaGraphUnavailableError{},
		"an unexpected error: operation not found": errors.New("operation not found"),
	} {
		if got := contractOutcome(err, "resolver-error"); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}
