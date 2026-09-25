package openbindings

import (
	"slices"
	"testing"
)

// Expression syntax is no document rule (§5.5): an expression that does not
// parse fails when a tool evaluates it, so a document holding one, named or
// inline, is judged on every other rule and can be conformant.
func TestValidateDocument_TransformSyntaxIsNotJudged(t *testing.T) {
	_, report, err := ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{"op":{}},
		"transforms":{"broken":"$.(","wide":"1e400"},
		"sources":{"s":{"bindingSpec":"x@1"}},
		"bindings":{"b":{"operation":"op","source":"s","inputTransform":"{ a: }","outputTransform":{"$ref":"#/transforms/broken"}}}}`), ValidateOptions{})
	if err != nil || report.Conclusion != ConclusionConformant {
		t.Fatalf("want conformant, got %v, %q; findings %+v", err, report.Conclusion, report.Findings)
	}
	if slices.Contains(DocumentRules(), "OBI-D-18") {
		t.Fatal("OBI-D-18 is retired and is no document rule")
	}
}

// A transforms entry that is not a string is a document-schema violation
// (OBI-D-02), not a judgment of expression syntax.
func TestValidateDocument_ATransformThatIsNotAStringViolatesTheSchema(t *testing.T) {
	report := mustValidateDocument(t, `{"openbindings":"0.2.0","operations":{"op":{}},"transforms":{"n":7}}`)
	if report.Evidence["OBI-D-02"] != EvidenceViolated {
		t.Fatalf("OBI-D-02 %q; findings %+v", report.Evidence["OBI-D-02"], report.Findings)
	}
}
