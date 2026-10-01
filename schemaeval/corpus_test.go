package schemaeval_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/internal/corpus"
	"github.com/openbindings/openbindings-go/schemaeval"
)

// This file is the schemaeval module's corpus adapter. It executes the
// corpus's value actions, validate-operation-values and check-examples (and
// format @1's resolve-schema-cycle), with the core's value contracts under
// this module's evaluator, which reads patterns as ECMA-262 regular
// expressions with Unicode semantics (OBI-T-08). The core module's adapter
// executes every other action; core's tests cannot import this module.

// valueProfile is the capability profile the core with this evaluator
// declares for the features the corpus's value cases depend on.
var valueProfile = corpus.Profile{Features: map[string]bool{
	"recursive-references":            true,
	"document-resource-dynamic-scope": true,
	"supplied-resources":              true,
	// Go's Unicode tables trail the JavaScript engines' (the 2026-09-29
	// ruling), so a pattern holding a property escape gets no verdict.
	"ecma262-unicode-property-escapes": false,
	"repeated-member-detection":        true,
	"draft-07-dialect":                 false,
	"exact-numbers":                    true,
}}

// caseBound bounds one case: OBI-T-06 leaves termination strategy to the
// tool, and the corpus's recursive cases are finite.
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
				// OBI-T-08 permits declining, but valueProfile is this
				// executor's own declaration: no verdict where it declares
				// every feature the case depends on supported is its defect.
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
	case "validate-operation-values", "resolve-schema-cycle":
		return judgeValues(cs, e)
	case "check-examples":
		return judgeExamples(cs, e)
	}
	return fail("action %q has no executor in this module", cs.Action)
}

type valueScenario struct {
	Given struct {
		Document      json.RawMessage   `json:"document"`
		NonConformant []string          `json:"nonConformant"`
		Operation     string            `json:"operation"`
		Side          string            `json:"side"`
		Values        []json.RawMessage `json:"values"`
		Value         json.RawMessage   `json:"value"` // format @1's resolve-schema-cycle
		Resources     []struct {
			URI      string          `json:"uri"`
			Document json.RawMessage `json:"document"`
		} `json:"resources"`
	} `json:"given"`
	Expected json.RawMessage `json:"expected"`
}

// uncarried keys the value cases whose non-conformant document the model
// cannot carry, each with why. None does.
var uncarried = map[string]string{}

// contractsOf decodes the document into the model and resolves its value
// contracts, so ValueContractCompiler.Resolve's own version decision is the
// one exercised. carried is false when the model cannot carry the document.
func contractsOf(document json.RawMessage, e openbindings.SchemaEvaluator, resources []openbindings.Resource) (contracts *openbindings.ValueContracts, model *openbindings.Document, carried bool, err error) {
	var doc openbindings.Document
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, nil, false, err
	}
	compiler, err := openbindings.NewValueContractCompiler(e, resources...)
	if err != nil {
		return nil, &doc, true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	contracts, err = compiler.Resolve(ctx, &doc)
	return contracts, &doc, true, err
}

// observe names a value contract's answer as the corpus does, with the
// no-verdict reason the answer carries.
func observe(err error) (corpus.Observed, error) {
	mismatch, noVerdict := errors.Is(err, openbindings.ErrMismatch), errors.Is(err, openbindings.ErrNoVerdict)
	switch {
	case err == nil:
		return corpus.Observed{Verdict: "valid"}, nil
	case mismatch && noVerdict:
		return corpus.Observed{}, fmt.Errorf("an answer matches both ErrMismatch and ErrNoVerdict: %v", err)
	case mismatch:
		return corpus.Observed{Verdict: "instance-mismatch"}, nil
	case noVerdict:
		reason := ""
		switch {
		case errors.Is(err, openbindings.ErrNoValueContract):
			reason = "no-contract"
		case errors.Is(err, openbindings.ErrUndefined):
			reason = "undefined-result"
		}
		return corpus.Observed{Verdict: "no-verdict", Reason: reason}, nil
	}
	return corpus.Observed{}, fmt.Errorf("an answer that is neither outcome: %v", err)
}

func judgeValues(cs corpus.Case, e openbindings.SchemaEvaluator) corpus.Judgment {
	var s valueScenario
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	values := s.Given.Values
	if cs.Action == "resolve-schema-cycle" {
		values = []json.RawMessage{s.Given.Value}
	}
	var resources []openbindings.Resource
	for _, r := range s.Given.Resources {
		resources = append(resources, openbindings.Resource{URI: r.URI, Document: r.Document})
	}
	contracts, _, carried, err := contractsOf(s.Given.Document, e, resources)
	switch {
	case !carried && len(s.Given.NonConformant) == 0:
		return fail("the model does not carry a conformant document: %v", err)
	case !carried && uncarried[cs.ID] != "":
		return corpus.Judgment{Category: corpus.Omitted, Detail: "the model cannot carry this non-conformant document, so this SDK does not continue with it: " + uncarried[cs.ID]}
	case !carried:
		// The model's decoding is the core's own: it continues with every
		// non-conformant document the model carries, so a document it cannot
		// carry is a case to key, never a silent omission.
		return fail("the model does not carry this non-conformant document (key the case in uncarried if it cannot): %v", err)
	case uncarried[cs.ID] != "":
		return fail("the keyed uncarried case %s is now carried: remove its uncarried entry", cs.ID)
	case errors.As(err, new(*openbindings.VersionRefusalError)):
		exclusive := contracts == nil && !errors.As(err, new(*openbindings.ValidationError))
		return corpus.JudgeValues(cs.Format, s.Expected, true, exclusive, nil, valueProfile)
	case err != nil:
		return fail("Resolve: %v", err)
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
	var observed []corpus.Observed
	for i, v := range values {
		o, err := observe(contract.ValidateJSON(ctx, v))
		if err != nil {
			return fail("value %d: %v", i, err)
		}
		observed = append(observed, o)
	}
	return corpus.JudgeValues(cs.Format, s.Expected, false, false, observed, valueProfile)
}

// judgeExamples checks an operation's examples by composing value
// validation: the core has no example checker. An example value checked
// against a stated contract holds or is a false claim, or gets no verdict;
// one where the operation states no contract makes no claim (§5.1).
func judgeExamples(cs corpus.Case, e openbindings.SchemaEvaluator) corpus.Judgment {
	var s struct {
		Given struct {
			Document  json.RawMessage `json:"document"`
			Operation string          `json:"operation"`
		} `json:"given"`
		Expected struct {
			Examples map[string]map[string]string `json:"examples"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(cs.Raw, &s); err != nil {
		return fail("unreadable scenario: %v", err)
	}
	contracts, model, carried, err := contractsOf(s.Given.Document, e, nil)
	if !carried || err != nil {
		return fail("the document's value contracts: %v", err)
	}
	key, operation, found := model.ResolveOperation(s.Given.Operation)
	if !found {
		return fail("operation %q not found", s.Given.Operation)
	}
	ctx, cancel := context.WithTimeout(context.Background(), caseBound)
	defer cancel()
	got := map[string]map[string]string{}
	for name, example := range operation.Examples {
		got[name] = map[string]string{}
		for _, side := range []struct {
			name    string
			value   json.RawMessage
			schema  openbindings.JSONSchema
			compile func(context.Context, string) (*openbindings.ValueContract, error)
		}{{"input", example.Input, operation.Input, contracts.CompileInput}, {"output", example.Output, operation.Output, contracts.CompileOutput}} {
			if side.value == nil {
				continue
			}
			if side.schema == nil {
				got[name][side.name] = "no-claim"
				continue
			}
			contract, err := side.compile(ctx, key)
			if err != nil {
				return fail("compiling %s's %s contract: %v", key, side.name, err)
			}
			o, err := observe(contract.ValidateJSON(ctx, side.value))
			if err != nil {
				return fail("example %s %s: %v", name, side.name, err)
			}
			got[name][side.name] = map[string]string{"valid": "holds", "instance-mismatch": "false-claim", "no-verdict": "no-verdict"}[o.Verdict]
		}
	}
	var diffs []string
	names := make([]string, 0, len(got))
	for name := range got {
		names = append(names, name)
	}
	for name := range s.Expected.Examples {
		if _, ok := got[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		g, w := got[name], s.Expected.Examples[name]
		for _, side := range []string{"input", "output"} {
			if g[side] != w[side] {
				diffs = append(diffs, fmt.Sprintf("%s %s: got %q, expected %q", name, side, g[side], w[side]))
			}
		}
	}
	if len(diffs) > 0 {
		return fail("%s", strings.Join(diffs, "; "))
	}
	return corpus.Judgment{Category: corpus.Pass, Detail: "composition"}
}
