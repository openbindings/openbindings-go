package synthesisscenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/processorscenarios"
	"github.com/openbindings/openbindings-go/synthesize"
)

// The runner refuses a corpus revision it does not implement rather than
// running it silently. `parseSynthesisScenarioFile` in @openbindings/sdk is the
// twin, pinned by the same three inputs.
func TestLoadRefusesAnUnimplementedCorpusRevision(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "binding-specs", "synthesis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := func(format string) string {
		return `{` + format + `"bindingSpec":"openbindings.openapi@1","family":"openapi",` +
			`"description":"one scenario","scenarios":[{"id":"OAPI-SS-99","description":"x",` +
			`"source":{"bindingSpec":"openbindings.openapi@1","content":{}},` +
			`"expected":{"outcome":"refused","rules":["OAPI-P-03"]}}]}`
	}
	for _, unsupported := range []string{
		`"format":"openbindings.binding-spec-synthesis-scenarios@2",`,
		`"format":"openbindings.binding-spec-synthesis-scenarios@4",`,
		``,
	} {
		if err := os.WriteFile(filepath.Join(dir, "openapi.json"), []byte(body(unsupported)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "openapi"); err == nil {
			t.Fatalf("format %q was accepted; an unimplemented revision must be refused", unsupported)
		} else if !strings.Contains(err.Error(), "unsupported synthesis scenario format") {
			t.Fatalf("format %q: err = %v", unsupported, err)
		}
	}

	if err := os.WriteFile(
		filepath.Join(dir, "openapi.json"),
		[]byte(body(`"format":"`+Format+`",`)),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	file, err := Load(root, "openapi")
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1", len(file.Scenarios))
	}
}

// Fixed is the guard the six self-contained families run behind; the seventh
// serves resources for real. `fixedSynthesizer` in @openbindings/sdk is the
// twin, with the same message.
func TestFixedRefusesCompanionResources(t *testing.T) {
	synth := synthesize.CoverageSynthesizer(nil)
	factory := Fixed(synth)

	for _, scenario := range []Scenario{
		{ID: "OAPI-SS-99"},
		{ID: "OAPI-SS-99", Resources: map[string]any{}},
	} {
		if _, err := factory(scenario); err != nil {
			t.Fatalf("a scenario declaring no companion resources must pass through: %v", err)
		}
	}

	_, err := factory(Scenario{
		ID:        "OAPI-SS-99",
		Resources: map[string]any{"https://companion.example/library.yaml": map[string]any{}},
	})
	if err == nil {
		t.Fatal("a scenario declaring companion resources must be refused")
	}
	const want = "OAPI-SS-99: declares companion resources, which this family's runner does not serve"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

func probeInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"probe": {
				Input: map[string]any{
					"properties": map[string]any{
						"issued": map[string]any{"example": "2020-01-01T12:00:00Z"},
					},
				},
			},
		},
	}
}

// assertionsFromJSON decodes assertions the way the corpus does. The
// Assertion type tracks operator presence through UnmarshalJSON so that
// `equals: null` is distinguishable from an absent `equals`, so a struct
// literal built in Go carries no operator at all.
func assertionsFromJSON(t *testing.T, encoded string) []processorscenarios.Assertion {
	t.Helper()
	var out []processorscenarios.Assertion
	if err := json.Unmarshal([]byte(encoded), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func synthesizedScenario(assertions []processorscenarios.Assertion) Scenario {
	return Scenario{
		ID:          "OAPI-SS-98",
		Description: "emitted-document assertion seam",
		Expected: Expected{
			Outcome:    "synthesized",
			Operations: []string{"probe"},
			Bindings:   []BindingIdentity{},
			Assertions: assertions,
			Coverage: ExpectedCoverage{
				Exhaustive:       true,
				FullyRepresented: true,
				Entries:          []CoverageEntry{},
			},
		},
	}
}

func TestMatchEvaluatesEmittedDocumentAssertions(t *testing.T) {
	result := &synthesize.SynthesizeResult{Interface: probeInterface()}
	result.Coverage.Exhaustive = true
	result.Coverage.FullyRepresented = true

	satisfied := synthesizedScenario(assertionsFromJSON(t,
		`[{"path":"/operations/probe/input/properties/issued/example","equals":"2020-01-01T12:00:00Z"}]`))
	if err := Match(satisfied, result); err != nil {
		t.Fatalf("a satisfied assertion must pass: %v", err)
	}

	violated := synthesizedScenario(assertionsFromJSON(t,
		`[{"path":"/operations/probe/input/properties/issued/example","equals":{}}]`))
	err := Match(violated, result)
	if err == nil {
		t.Fatal("a violated assertion must fail the scenario")
	}
	if !strings.Contains(err.Error(), "emitted-document assertion failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "/operations/probe/input/properties/issued/example") {
		t.Fatalf("the failure must name the pointer: %v", err)
	}
}

// Assertions are evaluated against the emitted document; they are not part of
// the identity surface the deep comparison decides the scenario on.
func TestAssertionsStayOutOfTheComparedIdentitySurface(t *testing.T) {
	result := &synthesize.SynthesizeResult{Interface: probeInterface()}
	result.Coverage.Exhaustive = true
	result.Coverage.FullyRepresented = true

	scenario := synthesizedScenario(assertionsFromJSON(t,
		`[{"path":"/openbindings","equals":"0.2.0"}]`))
	if err := Match(scenario, result); err != nil {
		t.Fatalf("an assertion list must not enter the identity comparison: %v", err)
	}
}
