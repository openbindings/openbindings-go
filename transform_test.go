package openbindings

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// stubParser stands in for the application's transform parser: it refuses
// the expressions in refuse, cannot decide those in undecided, and parses
// every other. What the pinned language accepts is the chosen parser's to
// decide, not the core's. Validation takes a parser alone, never an
// evaluator.
type stubParser struct {
	refuse    map[string]bool
	undecided map[string]bool
	parsed    *[]string
}

func (e stubParser) Parse(expression string) error {
	if e.parsed != nil {
		*e.parsed = append(*e.parsed, expression)
	}
	if e.undecided[expression] {
		return fmt.Errorf("the stub's size limit: %w", ErrTransformUndecided)
	}
	if e.refuse[expression] {
		return errors.New("the stub refuses " + strconv.Quote(expression))
	}
	return nil
}

const documentWithTransforms = `{"openbindings":"0.2.0","operations":{"op":{}},
	"sources":{"api":{"bindingSpec":"x@1"}},
	"transforms":{"bad":"(a + b","good":"x"},
	"bindings":{"b":{"operation":"op","source":"api","inputTransform":"items[","outputTransform":{"$ref":"#/transforms/good"}}}}`

// OBI-D-18 is decided by the parser validation is given, for every named and
// inline expression; a named-transform reference is not an expression.
func TestValidateDocument_TheGivenParserDecidesTransformSyntax(t *testing.T) {
	var parsed []string
	parser := stubParser{refuse: map[string]bool{"(a + b": true, "items[": true}, parsed: &parsed}
	_, report, err := ValidateDocument([]byte(documentWithTransforms), ValidateOptions{Transforms: parser})
	if !errors.As(err, new(*ValidationError)) || report.Evidence["OBI-D-18"] != EvidenceViolated {
		t.Fatalf("want OBI-D-18 violated, got %v and evidence %q", err, report.Evidence["OBI-D-18"])
	}
	var paths []string
	for _, finding := range report.Violations() {
		if finding.Rule == "OBI-D-18" {
			paths = append(paths, finding.Path)
			if !strings.Contains(finding.Message, "the stub refuses") {
				t.Errorf("the finding must carry the parser's reason: %q", finding.Message)
			}
		}
	}
	slices.Sort(paths)
	if want := []string{"/bindings/b/inputTransform", "/transforms/bad"}; !slices.Equal(paths, want) {
		t.Fatalf("OBI-D-18 findings at %v, want %v", paths, want)
	}
	slices.Sort(parsed)
	if want := []string{"(a + b", "items[", "x"}; !slices.Equal(parsed, want) {
		t.Fatalf("the parser parsed %q, want %q", parsed, want)
	}
}

// Without a parser, OBI-D-18 is inconclusive at every expression and never
// violated, so a document with transforms is undetermined, not
// non-conformant (§10.2).
func TestValidateDocument_WithoutAParserTransformSyntaxIsInconclusive(t *testing.T) {
	_, report, err := ValidateDocument([]byte(documentWithTransforms), ValidateOptions{})
	if err != nil {
		t.Fatalf("no violation is established without a parser: %v", err)
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

// A parser that cannot decide leaves OBI-D-18 inconclusive, never violated.
func TestValidateDocument_AnUndecidedParserIsInconclusive(t *testing.T) {
	parser := stubParser{undecided: map[string]bool{"(a + b": true, "items[": true}}
	_, report, err := ValidateDocument([]byte(documentWithTransforms), ValidateOptions{Transforms: parser})
	if err != nil || report.Evidence["OBI-D-18"] != EvidenceInconclusive {
		t.Fatalf("err %v, OBI-D-18 %q", err, report.Evidence["OBI-D-18"])
	}
}

// A document without transforms needs no parser to be conformant.
func TestValidateDocument_WithoutTransformsNoParserIsNeeded(t *testing.T) {
	_, report, err := ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{"op":{}}}`), ValidateOptions{})
	if err != nil || report.Conclusion != ConclusionConformant {
		t.Fatalf("want conformant, got %v and %q", err, report.Conclusion)
	}
}
