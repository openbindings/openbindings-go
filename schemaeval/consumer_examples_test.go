package schemaeval_test

// Consumer exercises for the core API (Loop C, stage C1, at 166b9b4) that
// need an evaluator, which the core module cannot import: the 0.2 CLI's
// value checks (ob-cli-surface-lab at f7a9d16: validate --operation,
// validate --examples, and invoke's checks), and an evaluator author's
// third-party evaluator run through openbindingstest. The other exercises
// are in the core module's consumer_examples_test.go. Comments marked
// "C1 item" name the report item an awkward step supports.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/openbindingstest"
	"github.com/openbindings/openbindings-go/schemaeval"
)

// valuesOBI is a shortened form of the CLI lab's sample document, with an
// operation whose input contract JSON Schema leaves undefined.
const valuesOBI = `{
  "openbindings": "0.2.0",
  "schemas": {
    "Task": {
      "type": "object",
      "properties": { "id": { "type": "string" }, "title": { "type": "string" }, "done": { "type": "boolean" } },
      "required": ["id", "title"]
    }
  },
  "operations": {
    "createTask": {
      "aliases": ["acme.tasks.createTask"],
      "input": { "type": "object", "properties": { "title": { "type": "string" } }, "required": ["title"] },
      "output": { "$ref": "#/schemas/Task" },
      "examples": {
        "basic": {
          "input": { "title": "Write the docs" },
          "output": { "id": "t_1", "title": "Write the docs", "done": false }
        },
        "untitled": { "input": {} }
      }
    },
    "events.deliver": {
      "input": { "type": "object" },
      "examples": { "ping": { "input": { "event": "ping" }, "output": null } }
    },
    "match": {
      "input": { "type": "string", "pattern": "(" }
    }
  }
}`

// cliValueCheck is `ob validate <obi> --operation <name> --input|--output
// <value>`, with the lab's exit statuses: 0 the value fits, 1 it does not,
// 2 usage (no such operation, not JSON), 3 refused, 4 no verdict.
func cliValueCheck(ctx context.Context, document []byte, operation, side string, value []byte) int {
	iface, err := openbindings.ParseDocument(document)
	var refusal *openbindings.VersionRefusalError
	switch {
	case errors.As(err, &refusal):
		fmt.Println("refused:", refusal.Reason)
		return 3
	case err != nil:
		fmt.Println("cannot read the document:", err)
		return 2
	}
	// C1 item four-calls: one value takes a compiler, a resolution, a
	// compile, and a validation.
	compiler, err := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	if err != nil {
		panic(err)
	}
	contracts, err := compiler.Resolve(ctx, iface)
	if err != nil {
		panic(err) // ParseDocument already refused what Resolve refuses
	}
	compile := contracts.CompileInput
	if side == "output" {
		compile = contracts.CompileOutput
	}
	contract, err := compile(ctx, operation)
	if errors.Is(err, openbindings.ErrOperationNotFound) {
		fmt.Printf("no operation named %q\n", operation)
		return 2
	} else if err != nil {
		panic(err) // the ctx's error
	}
	var mismatch *openbindings.MismatchError
	var noVerdict *openbindings.NoVerdictError
	switch err := contract.ValidateJSON(ctx, value); {
	case err == nil:
		fmt.Printf("the %s value fits %s\n", side, operation)
		return 0
	case errors.As(err, &mismatch):
		for _, problem := range mismatch.Problems {
			fmt.Printf("does not fit at %q\n", problem.InstanceLocation)
		}
		return 1
	case errors.Is(err, openbindings.ErrNoValueContract):
		fmt.Printf("%s specifies no %s contract, so there is nothing to check\n", operation, side)
		return 4
	case errors.Is(err, openbindings.ErrUndefined) && errors.As(err, &noVerdict):
		fmt.Printf("no verdict: JSON Schema leaves the result undefined, at %s\n", noVerdict.Location)
		return 4
	case errors.Is(err, openbindings.ErrNoVerdict):
		fmt.Println("no verdict:", err)
		return 4
	default:
		fmt.Println("not a JSON value:", err)
		return 2
	}
}

func Example_cliValueCheck() {
	ctx := context.Background()
	doc := []byte(valuesOBI)
	checks := []struct{ operation, side, value string }{
		{"acme.tasks.createTask", "input", `{"title":"Ship it"}`},
		{"createTask", "input", `{"title":5}`},
		{"createTask", "output", `{"id":"t_2"}`},
		{"events.deliver", "output", `{}`},
		{"match", "input", `"x"`},
		{"createTask", "input", `{"title":`},
		{"deleteTask", "input", `{}`},
	}
	for _, c := range checks {
		fmt.Println("exit", cliValueCheck(ctx, doc, c.operation, c.side, []byte(c.value)))
	}
	fmt.Println("exit", cliValueCheck(ctx, []byte(`{"openbindings":"0.3.0","operations":{}}`), "x", "input", []byte(`{}`)))
	// Output:
	// the input value fits acme.tasks.createTask
	// exit 0
	// does not fit at "/title"
	// exit 1
	// does not fit at ""
	// exit 1
	// events.deliver specifies no output contract, so there is nothing to check
	// exit 4
	// no verdict: JSON Schema leaves the result undefined, at #/operations/match/input
	// exit 4
	// not a JSON value: openbindings: the input is not JSON: unexpected end of JSON input
	// exit 2
	// no operation named "deleteTask"
	// exit 2
	// refused: document declares version "0.3.0", newer than the release line this implementation supports (0.2.x)
	// exit 3
}

// `ob validate <obi> --examples` checks every example value against its
// operation's contract (OBI-T-11: a mismatch is a false claim, never an
// exception to the schema). An absent example member is not checked; a
// present null is a value like any other.
func Example_cliValidateExamples() {
	ctx := context.Background()
	iface, err := openbindings.ParseDocument([]byte(valuesOBI))
	if err != nil {
		panic(err)
	}
	compiler, _ := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	contracts, err := compiler.Resolve(ctx, iface)
	if err != nil {
		panic(err)
	}
	misfits, unchecked := 0, 0
	for _, key := range slices.Sorted(maps.Keys(iface.Operations)) {
		operation := iface.Operations[key]
		if len(operation.Examples) == 0 {
			continue
		}
		input, _ := contracts.CompileInput(ctx, key)
		output, _ := contracts.CompileOutput(ctx, key)
		for _, name := range slices.Sorted(maps.Keys(operation.Examples)) {
			example := operation.Examples[name]
			var parts []string
			for _, side := range []struct {
				name     string
				value    json.RawMessage
				contract *openbindings.ValueContract
			}{{"input", example.Input, input}, {"output", example.Output, output}} {
				if side.value == nil {
					continue // absent
				}
				switch err := side.contract.ValidateJSON(ctx, side.value); {
				case err == nil:
					parts = append(parts, side.name+" fits")
				case errors.Is(err, openbindings.ErrMismatch):
					parts = append(parts, side.name+" does not fit")
					misfits++
				case errors.Is(err, openbindings.ErrNoValueContract):
					parts = append(parts, side.name+" not checked (no "+side.name+" contract)")
					unchecked++
				default:
					parts = append(parts, side.name+" no verdict")
					unchecked++
				}
			}
			fmt.Printf("%s example %q: %s\n", key, name, strings.Join(parts, ", "))
		}
	}
	fmt.Println("misfits:", misfits, "unchecked:", unchecked)
	// Output:
	// createTask example "basic": input fits, output fits
	// createTask example "untitled": input does not fit
	// events.deliver example "ping": input fits, output not checked (no output contract)
	// misfits: 1 unchecked: 1
}

// `ob invoke` checks a stream of input values one at a time (invariant 1:
// each value separately, never the sequence), refusing before anything is
// sent when the first value does not fit, and failing the exchange when a
// later one does not.
func Example_cliInvokeChecks() {
	ctx := context.Background()
	iface, err := openbindings.ParseDocument([]byte(valuesOBI))
	if err != nil {
		panic(err)
	}
	compiler, _ := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	contracts, err := compiler.Resolve(ctx, iface)
	if err != nil {
		panic(err)
	}
	input, err := contracts.CompileInput(ctx, "createTask")
	if err != nil {
		panic(err)
	}
	output, err := contracts.CompileOutput(ctx, "createTask")
	if err != nil {
		panic(err)
	}
	// A binding implementation that decodes outputs into Go values checks
	// them as they are, read as encoding/json encodes them.
	type task struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	fmt.Println("output fits:", output.Validate(ctx, task{ID: "t_9", Title: "a"}) == nil, output.Validate(ctx, map[string]any{"id": "t_9"}) == nil)
	stdin := strings.NewReader("{\"title\":\"a\"}\n{\"title\":\"b\"}\n{\"title\":5}\n{\"title\":\"d\"}\n")
	decoder := json.NewDecoder(stdin)
	for sent := 0; ; sent++ {
		var value json.RawMessage
		if err := decoder.Decode(&value); err == io.EOF {
			fmt.Println("input closed after", sent, "values; exit 0")
			return
		} else if err != nil {
			panic(err)
		}
		if err := input.ValidateJSON(ctx, value); errors.Is(err, openbindings.ErrMismatch) {
			if sent == 0 {
				fmt.Println("refused before sending anything; exit 3")
			} else {
				fmt.Printf("value %d does not fit after %d were sent: ERR_OPERATION_VALIDATION_FAILED; exit 1\n", sent+1, sent)
			}
			return
		}
		fmt.Println("sent", string(value))
	}
	// Output:
	// output fits: true false
	// sent {"title":"a"}
	// sent {"title":"b"}
	// value 3 does not fit after 2 were sent: ERR_OPERATION_VALIDATION_FAILED; exit 1
}

// ------------------------------------------------- an evaluator author

// budget is a third-party evaluator: it decorates another evaluator and
// gives no verdict for a value larger than a node budget, a resource limit
// of the application's own (§9 lists unbounded size among the exposures).
// It answers as the contract asks: the inner evaluator's answers pass
// through unchanged, and its own refusal is a fresh error matching none of
// core's sentinels.
type budget struct {
	inner    openbindings.SchemaEvaluator
	maxNodes int
}

func (b budget) Compile(ctx context.Context, bundle openbindings.SchemaBundle) (openbindings.CompiledSchema, error) {
	compiled, err := b.inner.Compile(ctx, bundle)
	if err != nil {
		return nil, err
	}
	return budgeted{compiled, b.maxNodes}, nil
}

type budgeted struct {
	inner    openbindings.CompiledSchema
	maxNodes int
}

func (b budgeted) Validate(ctx context.Context, value any) error {
	if n := nodes(value, b.maxNodes); n > b.maxNodes {
		return fmt.Errorf("budget: the value holds more than %d nodes", b.maxNodes)
	}
	return b.inner.Validate(ctx, value)
}

// nodes counts a JSON value's nodes, stopping past limit.
func nodes(value any, limit int) int {
	n := 1
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if n += nodes(item, limit-n); n > limit {
				return n
			}
		}
	case map[string]any:
		for _, member := range v {
			if n += nodes(member, limit-n); n > limit {
				return n
			}
		}
	}
	return n
}

// The evaluator in use: a value over the budget gets no verdict, with the
// evaluator's error as the cause.
func Example_evaluatorAuthor() {
	ctx := context.Background()
	inner := schemaeval.New(schemaeval.Options{PatternMatchTimeout: 50 * time.Millisecond})
	compiler, err := openbindings.NewValueContractCompiler(budget{inner: inner, maxNodes: 4})
	if err != nil {
		panic(err)
	}
	iface, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"sum":{"input":{"type":"array","items":{"type":"integer"}}}}}`))
	if err != nil {
		panic(err)
	}
	contracts, _ := compiler.Resolve(ctx, iface)
	input, _ := contracts.CompileInput(ctx, "sum")
	for _, value := range []string{`[1,2,3]`, `[1,"2"]`, `[1,2,3,4,5]`} {
		err := input.ValidateJSON(ctx, []byte(value))
		var noVerdict *openbindings.NoVerdictError
		switch {
		case err == nil:
			fmt.Println(value, "valid")
		case errors.Is(err, openbindings.ErrMismatch):
			fmt.Println(value, "mismatch")
		case errors.As(err, &noVerdict):
			fmt.Printf("%s no verdict, location %q: %v\n", value, noVerdict.Location, noVerdict.Cause)
		}
	}
	// Output:
	// [1,2,3] valid
	// [1,"2"] mismatch
	// [1,2,3,4,5] no verdict, location "": budget: the value holds more than 4 nodes
}

// The kit run an evaluator author writes. A decorator inherits its inner
// evaluator's exemptions, and with a budget no case reaches, only those.
// C1 item kit-exemptions: schemaeval's exemptions live in its own test file
// (TestConformance), so a decorator copies them.
func TestBudgetEvaluatorConformance(t *testing.T) {
	const propertyEscape = "a Unicode property escape, whose tables the inner evaluator does not match to ECMA-262's"
	openbindingstest.TestSchemaEvaluator(t, budget{inner: schemaeval.New(schemaeval.Options{}), maxNodes: 1 << 20}, openbindingstest.Options{
		Undecided: map[string]string{
			"suite/draft2020-12/optional/ecmascript-regex.json#10 patterns always use unicode semantics with pattern":           propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#14 pattern with non-ASCII digits":                                propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#15 patterns always use unicode semantics with patternProperties": propertyEscape,
			"suite/draft2020-12/optional/ecmascript-regex.json#19 patternProperties with non-ASCII digits":                      propertyEscape,
			"suite/draft2020-12/pattern.json#2 pattern with Unicode property escape requires unicode mode":                      propertyEscape,
			"suite/draft2020-12/patternProperties.json#5 patternProperties with Unicode property escape":                        propertyEscape,
			"adversarial/lazy-property-escape-not/1":                                                                            propertyEscape,
			"adversarial/lazy-pattern-properties/2":                                                                             propertyEscape,
			"adversarial/dynamic-ref-under-property-names":                                                                      "the inner library checks property names without the dynamic scope",
			"adversarial/numbers-in-values/0":                                                                                   "a value's number beyond the inner evaluator's limits, which the schema compares",
			"adversarial/numbers-in-values/1":                                                                                   "a value's number beyond the inner evaluator's limits, where the schema compares numbers",
		},
		Unlocated: map[string]string{
			"adversarial/type-and-const/0": "the inner library stops at a failing type",
		},
	})
}
