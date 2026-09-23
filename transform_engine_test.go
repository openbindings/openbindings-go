package openbindings

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// stubEngine stands in for the application's transform engine: it refuses
// the expressions in refuse and parses every other. What the pinned language
// accepts is the chosen engine's to decide, not the core's.
type stubEngine struct {
	refuse map[string]bool
	parsed *[]string
}

func (e stubEngine) Parse(expression string) error {
	if e.parsed != nil {
		*e.parsed = append(*e.parsed, expression)
	}
	if e.refuse[expression] {
		return errors.New("the stub refuses " + strconv.Quote(expression))
	}
	return nil
}

func (stubEngine) Evaluate(context.Context, string, any, map[string]any) (any, error) {
	return nil, errors.New("the stub does not evaluate")
}

const documentWithTransforms = `{"openbindings":"0.2.0","operations":{"op":{}},
	"sources":{"api":{"bindingSpec":"x@1","location":"https://api.example.com/api.json"}},
	"transforms":{"bad":"(a + b","good":"x"},
	"bindings":{"b":{"operation":"op","source":"api","inputTransform":"items[","outputTransform":{"$ref":"#/transforms/good"}}}}`

// OBI-D-18 is decided by the engine validation is given, for every named and
// inline expression; a named-transform reference is not an expression.
func TestValidateDocument_TheGivenEngineDecidesTransformSyntax(t *testing.T) {
	var parsed []string
	engine := stubEngine{refuse: map[string]bool{"(a + b": true, "items[": true}, parsed: &parsed}
	_, report, err := ValidateDocument([]byte(documentWithTransforms), ValidateOptions{Transforms: engine})
	if !errors.As(err, new(*ValidationError)) || report.Evidence["OBI-D-18"] != EvidenceViolated {
		t.Fatalf("want OBI-D-18 violated, got %v and evidence %q", err, report.Evidence["OBI-D-18"])
	}
	var paths []string
	for _, finding := range report.Violations() {
		if finding.Rule == "OBI-D-18" {
			paths = append(paths, finding.Path)
			if !strings.Contains(finding.Message, "the stub refuses") {
				t.Errorf("the finding must carry the engine's reason: %q", finding.Message)
			}
		}
	}
	slices.Sort(paths)
	if want := []string{"/bindings/b/inputTransform", "/transforms/bad"}; !slices.Equal(paths, want) {
		t.Fatalf("OBI-D-18 findings at %v, want %v", paths, want)
	}
	slices.Sort(parsed)
	if want := []string{"(a + b", "items[", "x"}; !slices.Equal(parsed, want) {
		t.Fatalf("the engine parsed %q, want %q", parsed, want)
	}
}

// Without an engine, OBI-D-18 is inconclusive at every expression and never
// violated, so a document with transforms is undetermined, not
// non-conformant (§10.2).
func TestValidateDocument_WithoutAnEngineTransformSyntaxIsInconclusive(t *testing.T) {
	_, report, err := ValidateDocument([]byte(documentWithTransforms), ValidateOptions{})
	if err != nil {
		t.Fatalf("no violation is established without an engine: %v", err)
	}
	if report.Evidence["OBI-D-18"] != EvidenceInconclusive || report.Conclusion != ConclusionConformanceUndetermined {
		t.Fatalf("OBI-D-18 %q, conclusion %q", report.Evidence["OBI-D-18"], report.Conclusion)
	}
	count := 0
	for _, finding := range report.InconclusiveChecks() {
		if finding.Rule == "OBI-D-18" {
			count++
		}
	}
	if count != 3 {
		t.Fatalf("want one inconclusive OBI-D-18 finding per expression, got %d", count)
	}
}

// A document without transforms needs no engine to be conformant.
func TestValidateDocument_WithoutTransformsNoEngineIsNeeded(t *testing.T) {
	_, report, err := ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{"op":{}}}`), ValidateOptions{})
	if err != nil || report.Conclusion != ConclusionConformant {
		t.Fatalf("want conformant, got %v and %q", err, report.Conclusion)
	}
}
