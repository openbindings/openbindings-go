package schemaeval_test

import (
	"testing"

	"github.com/openbindings/openbindings-go/openbindingstest"
	"github.com/openbindings/openbindings-go/schemaeval"
)

// propertyEscape is why a pattern holding a Unicode property escape gets no
// verdict here: Go's Unicode tables trail the JavaScript engines' (the
// 2026-09-29 ruling), so the engine's matches are not known to be ECMA-262's.
const propertyEscape = "a Unicode property escape, whose tables this evaluator does not match to ECMA-262's"

func TestConformance(t *testing.T) {
	openbindingstest.TestSchemaEvaluator(t, schemaeval.New(schemaeval.Options{}), openbindingstest.Options{
		Undecided: map[string]string{
			"suite/draft2020-12/optional/ecmascript-regex.json#10": propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#14": propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#15": propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#19": propertyEscape,
			"suite/draft2020-12/pattern.json#2":                    propertyEscape,
			"suite/draft2020-12/patternProperties.json#5":          propertyEscape,
			"adversarial/lazy-property-escape-not/1":               propertyEscape,
			"adversarial/lazy-pattern-properties/2":                propertyEscape,
			"adversarial/dynamic-ref-under-property-names":         "the library checks property names without the dynamic scope",
			"adversarial/numbers-in-values/0":                      "a value's number beyond 1e±10000, which the schema compares",
			"adversarial/numbers-in-values/1":                      "a value's number beyond 1e±10000, where the schema compares numbers",
		},
	})
}
