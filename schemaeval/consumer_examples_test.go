package schemaeval_test

// Consumer exercises for the core API that need an evaluator, which the
// core module cannot import: the 0.2 CLI's value checks
// (ob-cli-surface-lab at f7a9d16: validate --operation, validate
// --examples, and invoke's checks), and an evaluator author's third-party
// evaluator run through openbindingstest. The other exercises are in the
// core module's consumer_examples_test.go.

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
    },
    "ping": {
      "output": { "type": "string" }
    }
  }
}`

// cliValueCheck is `ob validate <obi> --operation <name> --input|--output
// <value>`, with the lab's exit statuses: 0 the value fits, 1 it does not,
// 2 usage (no such operation, not JSON, an unreadable document or supplied
// schema), 3 refused, 4 no verdict, 130 cancelled. resources are schemas the
// user supplied for references the document does not embed; core fetches
// nothing.
func cliValueCheck(ctx context.Context, document []byte, operation, side string, value []byte, resources ...openbindings.Resource) int {
	cancelled := func(err error) bool {
		return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	}
	doc, err := openbindings.ParseDocument(document)
	var refusal *openbindings.VersionRefusalError
	switch {
	case errors.As(err, &refusal):
		fmt.Println("refused:", refusal.Reason)
		return 3
	case err != nil:
		fmt.Println("cannot read the document:", err)
		return 2
	}
	// One value takes a compiler, a resolution, a compile, and a
	// validation; a service keeps each step's result as long as it serves.
	compiler, err := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}), resources...)
	if err != nil {
		fmt.Println("a supplied schema cannot be used:", err)
		return 2
	}
	contracts, err := compiler.Resolve(ctx, doc)
	switch {
	case cancelled(err):
		fmt.Println("cancelled")
		return 130
	case errors.As(err, &refusal):
		// ParseDocument refuses these first; a document from elsewhere could
		// still reach here.
		fmt.Println("refused:", refusal.Reason)
		return 3
	case err != nil:
		fmt.Println("cannot resolve the document:", err)
		return 2
	}
	compile := contracts.CompileInput
	if side == "output" {
		compile = contracts.CompileOutput
	}
	contract, err := compile(ctx, operation)
	switch {
	case errors.Is(err, openbindings.ErrOperationNotFound):
		fmt.Printf("no operation named %q\n", operation)
		return 2
	case cancelled(err):
		fmt.Println("cancelled")
		return 130
	case err != nil:
		fmt.Println("cannot compile:", err)
		return 2
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
	case cancelled(err):
		// A done ctx is a no-verdict holding the ctx's error, so this comes
		// before the no-verdict cases.
		fmt.Println("cancelled")
		return 130
	case errors.Is(err, openbindings.ErrNoValueContract):
		fmt.Printf("%s specifies no %s contract, so there is nothing to check\n", operation, side)
		return 4
	case errors.Is(err, openbindings.ErrUndefined) && errors.As(err, &noVerdict):
		fmt.Printf("no verdict: JSON Schema leaves the result undefined, at %s\n", noVerdict.Location)
		return 4
	case errors.As(err, &noVerdict) && noVerdict.Location != "":
		fmt.Printf("no verdict: core refused at %s: %v\n", noVerdict.Location, noVerdict.Cause)
		return 4
	case errors.As(err, &noVerdict):
		fmt.Println("no verdict:", noVerdict.Cause)
		return 4
	default:
		// ValidateJSON's only other error: the value is not a JSON value,
		// so there is nothing to judge.
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
		{"createTask", "input", `{"title":"a","title":"b"}`},
		{"deleteTask", "input", `{}`},
	}
	for _, c := range checks {
		fmt.Println("exit", cliValueCheck(ctx, doc, c.operation, c.side, []byte(c.value)))
	}
	fmt.Println("exit", cliValueCheck(ctx, []byte(`{"openbindings":"0.3.0","operations":{}}`), "x", "input", []byte(`{}`)))

	// The evaluator's own refusal: schemaeval does not match a Unicode
	// property escape as ECMA-262 does, so it gives no verdict where
	// evaluation reaches the pattern.
	backslash := string(rune(92))
	letters := []byte(`{"openbindings":"0.2.0","operations":{"name":{"input":{"type":"string","pattern":"^` + backslash + backslash + `p{L}+$"}}}}`)
	fmt.Println("exit", cliValueCheck(ctx, letters, "name", "input", []byte(`"Ada"`)))
	fmt.Println("exit", cliValueCheck(ctx, letters, "name", "input", []byte(`7`)))

	// Ctrl-C before the check.
	done, cancel := context.WithCancel(ctx)
	cancel()
	fmt.Println("exit", cliValueCheck(done, doc, "createTask", "input", []byte(`{"title":"x"}`)))
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
	// no verdict: the value cannot be read exactly: the object at "" repeats the member name "title"
	// exit 4
	// no operation named "deleteTask"
	// exit 2
	// refused: document declares version "0.3.0", newer than the release line this implementation supports (0.2.x)
	// exit 3
	// no verdict: schemaeval: evaluation reached what this evaluator cannot decide: the pattern "^\\p{L}+$" is one this evaluator does not match as ECMA-262 does: a Unicode property escape, whose tables this SDK does not match to ECMA-262's
	// exit 4
	// does not fit at ""
	// exit 1
	// cancelled
	// exit 130
}

// `ob validate --operation` on a document that references a schema it does
// not embed. Core fetches nothing: without the schema the contract gets no
// verdict, located at the reference; supplied as a Resource, it is checked.
func Example_cliValueResources() {
	ctx := context.Background()
	doc := []byte(`{"openbindings":"0.2.0","operations":{"ship":{"input":{"type":"object","properties":{"to":{"$ref":"https://schemas.example.com/address.json"}}}}}}`)
	address := openbindings.Resource{URI: "https://schemas.example.com/address.json", Document: json.RawMessage(`{"type":"object","required":["street"]}`)}
	fmt.Println("exit", cliValueCheck(ctx, doc, "ship", "input", []byte(`{"to":{}}`)))
	fmt.Println("exit", cliValueCheck(ctx, doc, "ship", "input", []byte(`{"to":{}}`), address))
	fmt.Println("exit", cliValueCheck(ctx, doc, "ship", "input", []byte(`{"to":{"street":"1 Main St"}}`), address))
	relative := openbindings.Resource{URI: "schemas/address.json", Document: address.Document}
	fmt.Println("exit", cliValueCheck(ctx, doc, "ship", "input", []byte(`{}`), relative))
	// Output:
	// no verdict: core refused at #/operations/ship/input/properties/to: "https://schemas.example.com/address.json" names https://schemas.example.com/address.json, a resource the document does not embed and the application did not supply (§7.4)
	// exit 4
	// does not fit at "/to"
	// exit 1
	// the input value fits ship
	// exit 0
	// a supplied schema cannot be used: openbindings: a resource's URI must be an absolute URI with no fragment; got "schemas/address.json"
	// exit 2
}

// A service reloads a document. A resolved snapshot, and every contract
// compiled from it, keep the document as it was when resolved; resolving
// again picks up the change.
func Example_valueContractSnapshot() {
	ctx := context.Background()
	doc, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"rename":{"input":{"type":"string"}}}}`))
	if err != nil {
		panic(err)
	}
	compiler, _ := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	before, err := compiler.Resolve(ctx, doc)
	if err != nil {
		panic(err)
	}
	kept, _ := before.CompileInput(ctx, "rename")

	operation := doc.Operations["rename"]
	operation.Input = map[string]any{"type": "integer"}
	doc.Operations["rename"] = operation
	doc.Operations["purge"] = openbindings.Operation{Input: true}

	recompiled, _ := before.CompileInput(ctx, "rename")
	_, purgeErr := before.CompileInput(ctx, "purge")
	after, err := compiler.Resolve(ctx, doc)
	if err != nil {
		panic(err)
	}
	fresh, _ := after.CompileInput(ctx, "rename")
	verdict := func(err error) string {
		switch {
		case err == nil:
			return "valid"
		case errors.Is(err, openbindings.ErrMismatch):
			return "mismatch"
		}
		return "no verdict"
	}
	fmt.Println(`kept contract: "x"`, verdict(kept.ValidateJSON(ctx, []byte(`"x"`))))
	fmt.Println(`old snapshot, compiled after the change: "x"`, verdict(recompiled.ValidateJSON(ctx, []byte(`"x"`))))
	fmt.Println("old snapshot knows purge:", !errors.Is(purgeErr, openbindings.ErrOperationNotFound))
	fmt.Println(`new snapshot: "x"`, verdict(fresh.ValidateJSON(ctx, []byte(`"x"`))), "7", verdict(fresh.ValidateJSON(ctx, []byte(`7`))))
	// Output:
	// kept contract: "x" valid
	// old snapshot, compiled after the change: "x" valid
	// old snapshot knows purge: false
	// new snapshot: "x" mismatch 7 valid
}

// `ob validate <obi> --examples` checks every example value against its
// operation's contract (§5.1, Examples: a value that fails it makes the
// claim false, and an example never changes the value contract its schema
// states). An absent example member is not checked; a present null is a
// value like any other.
func Example_cliValidateExamples() {
	ctx := context.Background()
	doc, err := openbindings.ParseDocument([]byte(valuesOBI))
	if err != nil {
		panic(err)
	}
	compiler, _ := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	contracts, err := compiler.Resolve(ctx, doc)
	if err != nil {
		panic(err)
	}
	misfits, unchecked := 0, 0
	for _, key := range slices.Sorted(maps.Keys(doc.Operations)) {
		operation := doc.Operations[key]
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

// invokeChecks is how `ob invoke` checks a stream of input values before it
// sends each one, one value at a time (invariant 1: each value separately,
// never the sequence). The spec leaves runtime validation, and what to do
// without a verdict, to the tool (§1.1, invariant 2), so the policy is
// stated here: the lab's where it gives one, the exercise's where it does
// not.
//   - A value that does not fit is refused before anything is sent if it is
//     the first (exit 3), and otherwise ends the exchange after the earlier
//     values were sent (exit 1). This is the lab's rule.
//   - With no input contract there is nothing to check: the value is sent.
//   - Any other no-verdict (an undefined result, a capability the evaluator
//     lacks, a value that cannot be read exactly) is not sent: exit 4 if it
//     is the first, 1 later. This is the exercise's choice; a tool may send
//     such a value instead.
//   - Input that is not JSON is a usage error (exit 2).
//   - Cancellation ends the exchange (exit 130), whatever was sent.
func invokeChecks(ctx context.Context, input *openbindings.ValueContract, stdin io.Reader, send func(json.RawMessage)) (exit int, why string) {
	decoder := json.NewDecoder(stdin)
	for sent := 0; ; sent++ {
		if err := ctx.Err(); err != nil {
			return 130, fmt.Sprintf("cancelled after %d sent", sent)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err == io.EOF {
			return 0, fmt.Sprintf("input closed after %d sent", sent)
		} else if err != nil {
			return 2, fmt.Sprintf("the input is not JSON after %d sent: %v", sent, err)
		}
		err := input.ValidateJSON(ctx, value)
		switch {
		case err == nil, errors.Is(err, openbindings.ErrNoValueContract):
			send(value)
			continue
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return 130, fmt.Sprintf("cancelled after %d sent", sent)
		}
		problem, first := "does not fit", 3
		switch {
		case errors.Is(err, openbindings.ErrMismatch):
		case errors.Is(err, openbindings.ErrNoVerdict):
			problem, first = "has no verdict", 4
		default:
			// ValidateJSON's only other error: the value is not a JSON value.
			return 2, fmt.Sprintf("value %d is not a JSON value: %v", sent+1, err)
		}
		if sent == 0 {
			return first, "the first value " + problem + "; nothing was sent"
		}
		return 1, fmt.Sprintf("value %d %s after %d were sent: ERR_OPERATION_VALIDATION_FAILED", sent+1, problem, sent)
	}
}

func Example_cliInvokeChecks() {
	ctx := context.Background()
	doc, err := openbindings.ParseDocument([]byte(valuesOBI))
	if err != nil {
		panic(err)
	}
	compiler, _ := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
	contracts, err := compiler.Resolve(ctx, doc)
	if err != nil {
		panic(err)
	}
	compile := func(operation string) *openbindings.ValueContract {
		contract, err := contracts.CompileInput(ctx, operation)
		if err != nil {
			panic(err)
		}
		return contract
	}
	run := func(name string, ctx context.Context, contract *openbindings.ValueContract, stdin string, cancelAfter int, cancel context.CancelFunc) {
		var sent []string
		exit, why := invokeChecks(ctx, contract, strings.NewReader(stdin), func(value json.RawMessage) {
			sent = append(sent, string(value))
			if len(sent) == cancelAfter {
				cancel()
			}
		})
		fmt.Printf("%s: exit %d, %s; sent %v\n", name, exit, why, sent)
	}
	stream := "{\"title\":\"a\"}\n{\"title\":\"b\"}\n{\"title\":5}\n{\"title\":\"d\"}\n"
	run("late misfit", ctx, compile("createTask"), stream, 0, nil)
	run("first misfit", ctx, compile("createTask"), `{"title":5}`, 0, nil)
	run("repeated member name", ctx, compile("createTask"), `{"title":"a","title":"b"}`, 0, nil)
	run("undefined contract", ctx, compile("match"), `"x"`, 0, nil)
	run("no input contract", ctx, compile("ping"), `"x" "y"`, 0, nil)
	run("not JSON", ctx, compile("createTask"), `{"title":"a"} {"title":`, 0, nil)
	stopped, cancel := context.WithCancel(ctx)
	run("Ctrl-C after one", stopped, compile("createTask"), stream, 1, cancel)

	// A binding implementation that decodes outputs into Go values checks
	// them as they are, read as encoding/json encodes them.
	output, err := contracts.CompileOutput(ctx, "createTask")
	if err != nil {
		panic(err)
	}
	type task struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	fmt.Println("output fits:", output.Validate(ctx, task{ID: "t_9", Title: "a"}) == nil, output.Validate(ctx, map[string]any{"id": "t_9"}) == nil)
	// Output:
	// late misfit: exit 1, value 3 does not fit after 2 were sent: ERR_OPERATION_VALIDATION_FAILED; sent [{"title":"a"} {"title":"b"}]
	// first misfit: exit 3, the first value does not fit; nothing was sent; sent []
	// repeated member name: exit 4, the first value has no verdict; nothing was sent; sent []
	// undefined contract: exit 4, the first value has no verdict; nothing was sent; sent []
	// no input contract: exit 0, input closed after 2 sent; sent ["x" "y"]
	// not JSON: exit 2, the input is not JSON after 1 sent: unexpected EOF; sent [{"title":"a"}]
	// Ctrl-C after one: exit 130, cancelled after 1 sent; sent [{"title":"a"}]
	// output fits: true false
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
	doc, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"sum":{"input":{"type":"array","items":{"type":"integer"}}}}}`))
	if err != nil {
		panic(err)
	}
	contracts, _ := compiler.Resolve(ctx, doc)
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
// schemaeval's exemptions live in its own test file (TestConformance), so a
// decorator copies them, and must follow them when they change.
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
			"adversarial/type-and-const/0":        "the inner library stops at a failing type",
			"adversarial/property-names-nested/1": "the inner evaluator locates a failed propertyNames below the top level at the value",
		},
	})
}
