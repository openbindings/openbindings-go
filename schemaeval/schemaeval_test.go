package schemaeval_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/openbindings/openbindings-go"
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
			"suite/draft2020-12/optional/ecmascript-regex.json#10 patterns always use unicode semantics with pattern":           propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#14 pattern with non-ASCII digits":                                propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#15 patterns always use unicode semantics with patternProperties": propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#19 patternProperties with non-ASCII digits":                      propertyEscape,
			"suite/draft2020-12/pattern.json#2 pattern with Unicode property escape requires unicode mode":                      propertyEscape,
			"suite/draft2020-12/patternProperties.json#5 patternProperties with Unicode property escape":                        propertyEscape,
			"adversarial/lazy-property-escape-not/1":                                                                            propertyEscape,
			"adversarial/lazy-pattern-properties/2":                                                                             propertyEscape,
			"adversarial/dynamic-ref-under-property-names":                                                                      "the library checks property names without the dynamic scope",
			"adversarial/numbers-in-values/0":                                                                                   "a value's number beyond 1e±10000, which the schema compares",
			"adversarial/numbers-in-values/1":                                                                                   "a value's number beyond 1e±10000, where the schema compares numbers",
		},
		Unlocated: map[string]string{
			"adversarial/type-and-const/0":        "the library stops at a failing type, reporting no other failing keyword of that schema",
			"adversarial/property-names-nested/1": "the library's location for a failed propertyNames below the top level is not reliable, so it is located at the value",
		},
	})
}

// A failed propertyNames keyword is one problem, located at the root of the
// value, every run, whatever objects it applies to and however the library
// groups their failures: the library's location for one below the top level
// is not reliable. Each invalid name is named once, in sorted order, though
// the library reports them in the order it walks each object.
func TestPropertyNamesLocatedAtTheValue(t *testing.T) {
	ctx := context.Background()
	invalid := func(name string) string {
		return "the member name " + strconv.Quote(name) + " is invalid: '" + name + "' does not match pattern '^[a-z]+$'"
	}
	for name, c := range map[string]struct {
		schema, value string
		want          []openbindings.SchemaProblem
	}{
		"below the top level, beside siblings": {
			`{"type":"object","properties":{"a":{"type":"integer"},"b":{"additionalProperties":{"propertyNames":{"pattern":"^[a-z]+$"}}},"c":{"type":"integer"}}}`,
			`{"a":1,"b":{"x":{"E<1>":1},"y":{"F<":1}},"c":2}`,
			[]openbindings.SchemaProblem{{InstanceLocation: "", Message: invalid("E<1>") + "; " + invalid("F<")}},
		},
		"two objects under one keyword, one holding two names": {
			`{"type":"object","properties":{"a":{"type":"integer"},"b":{"additionalProperties":{"propertyNames":{"pattern":"^[a-z]+$"}}},"c":{"type":"integer"}}}`,
			`{"a":1,"b":{"x":{"E<1>":1},"y":{"G<":1,"F<":2}},"c":2}`,
			[]openbindings.SchemaProblem{{InstanceLocation: "", Message: invalid("E<1>") + "; " + invalid("F<") + "; " + invalid("G<")}},
		},
		"one name in two objects": {
			`{"additionalProperties":{"propertyNames":{"pattern":"^[a-z]+$"}}}`,
			`{"x":{"E<1>":1,"a":2},"y":{"E<1>":1}}`,
			[]openbindings.SchemaProblem{{InstanceLocation: "", Message: invalid("E<1>")}},
		},
		"items of objects": {
			`{"items":{"propertyNames":{"pattern":"^[a-z]+$"}}}`,
			`[{"ab":1},{"G<":1,"E<1>":2},{"cd":3,"F<":4}]`,
			[]openbindings.SchemaProblem{{InstanceLocation: "", Message: invalid("E<1>") + "; " + invalid("F<") + "; " + invalid("G<")}},
		},
		"two keywords": {
			`{"properties":{"a":{"propertyNames":{"pattern":"^[a-z]+$"}},"b":{"propertyNames":{"maxLength":1}}}}`,
			`{"a":{"E<1>":1},"b":{"ab":1}}`,
			[]openbindings.SchemaProblem{{InstanceLocation: "", Message: invalid("E<1>")}, {InstanceLocation: "", Message: `the member name "ab" is invalid: maxLength: got 2, want 1`}},
		},
		"at the top level, several names": {
			`{"propertyNames":{"pattern":"^[a-z]+$"}}`,
			`{"G<":1,"E<1>":2,"a":3,"F<":4}`,
			[]openbindings.SchemaProblem{{InstanceLocation: "", Message: invalid("E<1>") + "; " + invalid("F<") + "; " + invalid("G<")}},
		},
	} {
		doc, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"op":{"input":` + c.schema + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		compiler, err := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
		if err != nil {
			t.Fatal(err)
		}
		contracts, err := compiler.Resolve(ctx, doc)
		if err != nil {
			t.Fatal(err)
		}
		contract, err := contracts.CompileInput(ctx, "op")
		if err != nil {
			t.Fatal(err)
		}
		for i := range 300 {
			var mismatch *openbindings.MismatchError
			if err := contract.ValidateJSON(ctx, []byte(c.value)); !errors.As(err, &mismatch) || !slices.Equal(mismatch.Problems, c.want) {
				t.Fatalf("%s, run %d: %v, want the problems %q", name, i, err, c.want)
			}
		}
	}
}
