package schemaeval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/internal/corpus"
	"github.com/openbindings/openbindings-go/schemaeval"
)

// This file is the schemaeval module's corpus adapter. It executes the
// corpus's value actions, validate-operation-values and check-examples, with
// the core's value contracts under this module's evaluator, which reads
// patterns as ECMA-262 regular expressions with Unicode semantics (§5.2),
// and judges each answer as the corpus's Judging table states. The core
// module's adapter executes every other action; core's tests cannot import
// this module.

// valueProfile is the capability profile the core with this evaluator
// declares for the features the corpus's value cases depend on.
var valueProfile = corpus.Profile{Features: map[string]bool{
	"recursive-references":            true,
	"document-resource-dynamic-scope": true,
	"supplied-resources":              true,
	// Go's Unicode tables trail the JavaScript engines', a capability limit
	// the core's package doc declares, so a pattern holding a property
	// escape gets no verdict.
	"ecma262-unicode-property-escapes": false,
	"repeated-member-detection":        true,
	"draft-07-dialect":                 false,
	"exact-numbers":                    true,
	// A Go string cannot carry an escaped lone surrogate, another declared
	// capability limit, so a value holding one gets no verdict.
	"exact-lone-surrogate-strings": false,
}}

// caseBound bounds one case, so a case that does not terminate fails the
// run rather than hanging it; the corpus's recursive cases are finite.
const caseBound = 10 * time.Second

func TestConformanceCorpusValues(t *testing.T) {
	dir := corpus.Locate("..")
	if dir == "" {
		if corpus.Required() {
			t.Fatal("spec conformance corpus not found (OB_CORPUS_REQUIRED is set; set OB_SPEC_CORPUS to the spec repo's conformance dir)")
		}
		t.Skip("spec conformance corpus not found")
	}
	c, err := corpus.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var ledger corpus.Ledger
	evaluator := schemaeval.New(schemaeval.Options{})
	for _, cs := range c.Cases {
		if module, _ := corpus.Designate(cs); module != corpus.ModuleSchemaeval {
			continue // the core module records it
		}
		t.Run(cs.ID+"/"+cs.Description, func(t *testing.T) {
			j := bounded(func() corpus.Judgment { return judgeValueCase(cs, evaluator) })
			ledger.Record(cs.ID, j)
			switch j.Category {
			case corpus.Pass:
				t.Logf("pass: %s", j.Detail)
			case corpus.Fail:
				t.Error(j.Detail)
			case corpus.Shortfall:
				// valueProfile is this executor's own declaration: a decline
				// where it declares every feature the case depends on
				// supported is its defect.
				t.Errorf("SHORTFALL against this executor's declared profile: %s", j.Detail)
			default:
				t.Skipf("%s: %s", j.Category, j.Detail)
			}
		})
	}
	problems, summary := c.Reconcile(corpus.ModuleSchemaeval, &ledger)
	t.Logf("corpus %s\n%s", dir, summary)
	for _, p := range problems {
		t.Error(p)
	}
}

func bounded(f func() corpus.Judgment) corpus.Judgment {
	done := make(chan corpus.Judgment, 1)
	go func() { done <- f() }()
	select {
	case j := <-done:
		return j
	case <-time.After(caseBound + 2*time.Second):
		return corpus.Judgment{Category: corpus.Fail, Detail: fmt.Sprintf("did not finish within %s", caseBound)}
	}
}

func fail(format string, a ...any) corpus.Judgment {
	return corpus.Judgment{Category: corpus.Fail, Detail: fmt.Sprintf(format, a...)}
}

func judgeValueCase(cs corpus.Case, e openbindings.SchemaEvaluator) corpus.Judgment {
	switch cs.Action {
	case "validate-operation-values":
		return judgeValues(cs, e)
	case "check-examples":
		return judgeExamples(cs, e)
	}
	return fail("action %q has no executor in this module", cs.Action)
}

type valueScenario struct {
	Given struct {
		Document  json.RawMessage   `json:"document"`
		Operation string            `json:"operation"`
		Side      string            `json:"side"`
		Values    []json.RawMessage `json:"values"`
		Resources []struct {
			URI      string          `json:"uri"`
			Document json.RawMessage `json:"document"`
		} `json:"resources"`
	} `json:"given"`
	Expected json.RawMessage `json:"expected"`
}

// contractsOf decodes the document into the model and resolves its value
// contracts, so ValueContractCompiler.Resolve's own version decision is the
// one exercised. Every value case's document declares the 0.2 line and
// conforms, so a document the model does not carry, a refusal, or any other
// error resolving it fails the case.
func contractsOf(document json.RawMessage, e openbindings.SchemaEvaluator, resources []openbindings.Resource) (*openbindings.ValueContracts, *openbindings.Document, error) {
	var doc openbindings.Document
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, nil, fmt.Errorf("the model does not carry the document: %v", err)
	}
	compiler, err := openbindings.NewValueContractCompiler(e, resources...)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	contracts, err := compiler.Resolve(ctx, &doc)
	if err != nil {
		return nil, nil, fmt.Errorf("Resolve: %v", err)
	}
	return contracts, &doc, nil
}

// answer names a value contract's answer as the corpus does: satisfies for
// a valid value, fails for an established mismatch, and a decline for any
// no-verdict, whatever its cause, which the corpus does not test.
func answer(err error) (string, error) {
	mismatch, noVerdict := errors.Is(err, openbindings.ErrMismatch), errors.Is(err, openbindings.ErrNoVerdict)
	switch {
	case err == nil:
		return corpus.Satisfies, nil
	case mismatch && noVerdict:
		return "", fmt.Errorf("an answer matches both ErrMismatch and ErrNoVerdict: %v", err)
	case mismatch:
		return corpus.Fails, nil
	case noVerdict:
		return corpus.Decline, nil
	}
	return "", fmt.Errorf("an answer that is neither outcome: %v", err)
}

func judgeValues(cs corpus.Case, e openbindings.SchemaEvaluator) corpus.Judgment {
	var s valueScenario
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	var resources []openbindings.Resource
	for _, r := range s.Given.Resources {
		resources = append(resources, openbindings.Resource{URI: r.URI, Document: r.Document})
	}
	contracts, _, err := contractsOf(s.Given.Document, e, resources)
	if err != nil {
		return fail("%v", err)
	}
	compile := contracts.CompileInput
	if s.Given.Side == "output" {
		compile = contracts.CompileOutput
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	contract, err := compile(ctx, s.Given.Operation)
	if err != nil {
		return fail("compiling %s's %s contract: %v", s.Given.Operation, s.Given.Side, err)
	}
	var answers []string
	for i, v := range s.Given.Values {
		a, err := answer(contract.ValidateJSON(ctx, v))
		if err != nil {
			return fail("value %d: %v", i, err)
		}
		answers = append(answers, a)
	}
	return corpus.JudgeValues(s.Expected, answers, valueProfile)
}

// judgeExamples checks an operation's examples by composing value
// validation: the core has no example checker. Each value an example
// supplies is validated against the corresponding value contract, where the
// operation states none as where it does, and the claim's truth is judged as
// that value's answer (§5.1).
func judgeExamples(cs corpus.Case, e openbindings.SchemaEvaluator) corpus.Judgment {
	var s struct {
		Given struct {
			Document  json.RawMessage `json:"document"`
			Operation string          `json:"operation"`
		} `json:"given"`
		Expected json.RawMessage `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	contracts, model, err := contractsOf(s.Given.Document, e, nil)
	if err != nil {
		return fail("%v", err)
	}
	key, operation, found := model.ResolveOperation(s.Given.Operation)
	if !found {
		return fail("operation %q not found", s.Given.Operation)
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	answers := map[string]map[string]string{}
	for name, example := range operation.Examples {
		answers[name] = map[string]string{}
		for _, side := range []struct {
			name    string
			value   json.RawMessage
			compile func(context.Context, string) (*openbindings.ValueContract, error)
		}{{"input", example.Input, contracts.CompileInput}, {"output", example.Output, contracts.CompileOutput}} {
			if side.value == nil {
				continue
			}
			contract, err := side.compile(ctx, key)
			if err != nil {
				return fail("compiling %s's %s contract: %v", key, side.name, err)
			}
			a, err := answer(contract.ValidateJSON(ctx, side.value))
			if err != nil {
				return fail("example %s %s: %v", name, side.name, err)
			}
			answers[name][side.name] = a
		}
	}
	return corpus.JudgeExamples(s.Expected, answers, valueProfile)
}
