package operationgraph

// Conformance corpus adapter: runs this engine against the spec repository's
// operation-graph corpus unmodified (execution fixtures with mocked
// per-invocation operation behavior, and the OG-V validation fixtures).
//
// The corpus is located via OB_SPEC_CORPUS or the local-dev sibling path
// (openbindings/spec next to openbindings/openbindings-go); the tests skip
// when it is absent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	jsonata "github.com/blues/jsonata-go"
	openbindings "github.com/openbindings/openbindings-go"
)

// ---------------------------------------------------------------------------
// Corpus location and fixture shapes
// ---------------------------------------------------------------------------

func corpusDir(t *testing.T) string {
	t.Helper()
	if env := os.Getenv("OB_SPEC_CORPUS"); env != "" {
		return env
	}
	dir := filepath.Join("..", "..", "..", "spec", "conformance", "operation-graph")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("spec conformance corpus not found at %s (set OB_SPEC_CORPUS)", dir)
	}
	return dir
}

type execFile struct {
	Fixtures []execFixture `json:"fixtures"`
}

type execFixture struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Operations map[string]*mockOp `json:"operations"`
	Graph      json.RawMessage    `json:"graph"`
	Writes     []any              `json:"writes"`
	Expected   struct {
		Output   []any  `json:"output"`
		Ordering string `json:"ordering"`
		// ArrayOrdering: "set" compares ARRAY-VALUED output events as
		// multisets (element order inside the array is implementation-
		// defined — a buffer fed by concurrent each invocations), while
		// the event stream itself still honors Ordering. See the corpus
		// README ("which `ordering` alone cannot express").
		ArrayOrdering string `json:"arrayOrdering"`
		Error         bool   `json:"error"`
		ErrorDetail   any    `json:"errorDetail"`
	} `json:"expected"`
}

type mockOp struct {
	ClosesAfter *int           `json:"closesAfter"`
	Responses   []mockResponse `json:"responses"`
}

type mockResponse struct {
	WhenInput  json.RawMessage `json:"whenInput"`
	WhenInputs *[]any          `json:"whenInputs"`
	Emit       *[]any          `json:"emit"`
	Fail       *string         `json:"fail"`
}

type validationFile struct {
	Rules []struct {
		Rule  string `json:"rule"`
		Title string `json:"title"`
		Tests []struct {
			Description string          `json:"description"`
			Graph       json.RawMessage `json:"graph"`
			Valid       bool            `json:"valid"`
			Operations  *[]string       `json:"operations"`
		} `json:"tests"`
	} `json:"rules"`
}

// ---------------------------------------------------------------------------
// Mock binding invoker: the fixtures' per-invocation operation model
// ---------------------------------------------------------------------------

const mockFormat = "corpus-mock@1"

// mockBindingInvoker serves fixture operations: an invocation collects
// writes (closing its input from below after closesAfter reads, when set),
// then responds per the first matching response rule.
type mockBindingInvoker struct {
	ops map[string]*mockOp
}

func (m *mockBindingInvoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: mockFormat}}
}

func (m *mockBindingInvoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	opKey := ""
	if args.Binding != nil {
		opKey = args.Binding.Operation
	}
	op, ok := m.ops[opKey]
	if !ok {
		return openbindings.NewErroredInvocation[any, any](&openbindings.InvocationError{
			Code: "ERR_NO_MOCK", Message: fmt.Sprintf("fixture has no mock for operation %q", opKey),
		})
	}
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go m.serve(ctx, inv, op, opKey)
	return inv
}

func (m *mockBindingInvoker) serve(ctx context.Context, handle openbindings.BindingHandle[any, any], op *mockOp, opKey string) {
	writes := []any{}
	limit := -1
	if op.ClosesAfter != nil {
		limit = *op.ClosesAfter
	}
	for limit != 0 {
		v, err := handle.ReadInput(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return // invocation terminal
			}
			break
		}
		writes = append(writes, v)
		if limit > 0 && len(writes) >= limit {
			break
		}
	}
	if limit >= 0 {
		_ = handle.CloseInput() // the binding closes its input from below
	}

	resp := matchMockResponse(op, writes)
	if resp == nil {
		handle.FireError(&openbindings.InvocationError{
			Code: "ERR_NO_MOCK", Message: fmt.Sprintf("no mocked response for %q writes %s", opKey, canon(writes)),
		})
		return
	}
	if resp.Fail != nil {
		handle.FireError(&openbindings.InvocationError{Code: *resp.Fail, Message: *resp.Fail})
		return
	}
	for _, v := range *resp.Emit {
		if err := handle.EmitOutput(v); err != nil {
			return
		}
	}
	handle.CloseOutput()
}

func matchMockResponse(op *mockOp, writes []any) *mockResponse {
	for i := range op.Responses {
		r := &op.Responses[i]
		switch {
		case r.WhenInputs != nil:
			if canon(*r.WhenInputs) == canon(writes) {
				return r
			}
		case len(r.WhenInput) > 0:
			var want any
			if json.Unmarshal(r.WhenInput, &want) == nil &&
				len(writes) == 1 && canon(want) == canon(writes[0]) {
				return r
			}
		default:
			return r // wildcard
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// JSONata evaluator (test-side; mirrors ob's, mapping undefined to the SDK
// sentinel)
// ---------------------------------------------------------------------------

type jsonataEvaluator struct{}

func (j *jsonataEvaluator) Evaluate(expression string, data any) (any, error) {
	return j.EvaluateWithBindings(expression, data, nil)
}

func (j *jsonataEvaluator) EvaluateWithBindings(expression string, data any, bindings map[string]any) (any, error) {
	expr, err := jsonata.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("compile jsonata: %w", err)
	}
	if len(bindings) > 0 {
		vars := make(map[string]any, len(bindings))
		for k, v := range bindings {
			vars[k] = normalizeJSON(v)
		}
		if err := expr.RegisterVars(vars); err != nil {
			return nil, fmt.Errorf("register jsonata vars: %w", err)
		}
	}
	result, err := expr.Eval(normalizeJSON(data))
	if err != nil {
		if errors.Is(err, jsonata.ErrUndefined) {
			return nil, openbindings.ErrTransformUndefined
		}
		return nil, fmt.Errorf("evaluate jsonata: %w", err)
	}
	return normalizeJSON(result), nil
}

func normalizeJSON(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

// ---------------------------------------------------------------------------
// Canonical comparison
// ---------------------------------------------------------------------------

// canon renders a value as canonical JSON (encoding/json sorts map keys).
func canon(v any) string {
	b, err := json.Marshal(normalizeJSON(v))
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

// sortArraysCanonically normalizes array-valued output events for
// arrayOrdering:set comparison: each top-level array event's elements are
// sorted by canonical encoding (multiset semantics), leaving the event
// stream's own order untouched.
func sortArraysCanonically(events []any) []any {
	out := make([]any, len(events))
	for i, ev := range events {
		arr, ok := ev.([]any)
		if !ok {
			out[i] = ev
			continue
		}
		sorted := append([]any(nil), arr...)
		sort.Slice(sorted, func(a, b int) bool { return canon(sorted[a]) < canon(sorted[b]) })
		out[i] = sorted
	}
	return out
}

func multisetEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	ca := make([]string, len(a))
	cb := make([]string, len(b))
	for i := range a {
		ca[i] = canon(a[i])
	}
	for i := range b {
		cb[i] = canon(b[i])
	}
	sort.Strings(ca)
	sort.Strings(cb)
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Execution fixtures
// ---------------------------------------------------------------------------

func TestCorpusExecution(t *testing.T) {
	dir := filepath.Join(corpusDir(t), "execution")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus execution dir: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var file execFile
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, fx := range file.Fixtures {
			fx := fx
			t.Run(fx.ID, func(t *testing.T) {
				runExecutionFixture(t, &fx)
			})
		}
	}
}

func runExecutionFixture(t *testing.T, fx *execFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Synthetic OBI: one schema-less operation per mocked key, each bound to
	// the mock source, so conduit/each nodes route through the real
	// OperationInvoker (name resolution, binding selection) into the mock.
	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{},
		Sources: map[string]openbindings.Source{
			"mock": {Format: mockFormat, Content: map[string]any{}},
		},
		Bindings: map[string]openbindings.BindingEntry{},
	}
	for opKey := range fx.Operations {
		iface.Operations[opKey] = openbindings.Operation{}
		iface.Bindings[opKey+".mock"] = openbindings.BindingEntry{Operation: opKey, Source: "mock"}
	}

	opInvoker := openbindings.NewOperationInvoker(&mockBindingInvoker{ops: fx.Operations})
	opInvoker.TransformEvaluator = &jsonataEvaluator{}
	opInvoker.AddBindingInvoker(NewInvoker(opInvoker))

	var graphValue any
	if err := json.Unmarshal(fx.Graph, &graphValue); err != nil {
		t.Fatalf("parse fixture graph: %v", err)
	}
	doc := map[string]any{"graphs": map[string]any{"g": graphValue}}

	call := opInvoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:    openbindings.InvocationSource{Format: FormatToken, Content: doc},
		Ref:       "#/graphs/g",
		Interface: iface,
	})

	// Writer: the fixture's caller. Writes are paced: after each write the
	// caller yields until either the graph back-closes the input side (then
	// remaining writes are refused at the boundary, per the spec) or a short
	// settle window passes. ERR_INPUT_CLOSED on a write is the boundary
	// refusal, not a failure.
	go func() {
		defer func() { _ = call.Close() }()
		for i, w := range fx.Writes {
			if i > 0 {
				select {
				case <-call.InputClosed():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			if err := call.Write(ctx, w); err != nil {
				return // input closed from below (back-closure) or terminal
			}
		}
	}()

	// Collect outputs to EOF or terminal.
	var outputs []any
	var terminal *openbindings.InvocationError
	out := call.Outputs()
	for {
		v, err := out.Read(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("timed out collecting outputs (got %d so far: %s)", len(outputs), canon(outputs))
			}
			terminal = openbindings.AsInvocationError(err)
			break
		}
		outputs = append(outputs, v)
	}

	// Terminal expectations.
	if fx.Expected.Error {
		if terminal == nil {
			t.Fatalf("expected terminal error %v, graph completed normally with outputs %s", fx.Expected.ErrorDetail, canon(outputs))
		}
		switch detail := fx.Expected.ErrorDetail.(type) {
		case string:
			// An unhandled conduit terminal error: the graph surfaces the
			// inner terminal error itself; its code is its identity.
			if terminal.Code != detail {
				t.Fatalf("terminal error code = %q, want %q (err: %v)", terminal.Code, detail, terminal)
			}
		default:
			// An exit node with error:true: the event is the error detail.
			if terminal.Code != openbindings.ErrCodeOperationGraphExit {
				t.Fatalf("terminal error code = %q, want %q (err: %v)", terminal.Code, openbindings.ErrCodeOperationGraphExit, terminal)
			}
			if canon(terminal.Details) != canon(detail) {
				t.Fatalf("exit error detail = %s, want %s", canon(terminal.Details), canon(detail))
			}
		}
	} else if terminal != nil {
		t.Fatalf("graph terminated with %v, expected normal completion with outputs %s", terminal, canon(fx.Expected.Output))
	}

	// Output expectations (nil and empty are the same empty stream).
	if outputs == nil {
		outputs = []any{}
	}
	expected := fx.Expected.Output
	if expected == nil {
		expected = []any{}
	}
	fx.Expected.Output = expected
	if fx.Expected.ArrayOrdering == "set" {
		outputs = sortArraysCanonically(outputs)
		fx.Expected.Output = sortArraysCanonically(fx.Expected.Output)
	}
	if fx.Expected.Ordering == "set" {
		if !multisetEqual(outputs, fx.Expected.Output) {
			t.Fatalf("output multiset mismatch\n  got:    %s\n  expect: %s", canon(outputs), canon(fx.Expected.Output))
		}
	} else {
		if canon(outputs) != canon(fx.Expected.Output) {
			t.Fatalf("output mismatch\n  got:    %s\n  expect: %s", canon(outputs), canon(fx.Expected.Output))
		}
	}
}

// ---------------------------------------------------------------------------
// Validation fixtures
// ---------------------------------------------------------------------------

func TestCorpusValidation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(corpusDir(t), "validation", "OG-VR.json"))
	if err != nil {
		t.Fatalf("read OG-VR.json: %v", err)
	}
	var file validationFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse OG-VR.json: %v", err)
	}
	for _, rule := range file.Rules {
		for _, test := range rule.Tests {
			name := fmt.Sprintf("%s/%s", rule.Rule, test.Description)
			t.Run(name, func(t *testing.T) {
				var g Graph
				if err := json.Unmarshal(test.Graph, &g); err != nil {
					t.Fatalf("parse graph: %v", err)
				}
				var opKeys map[string]bool
				if test.Operations != nil {
					opKeys = map[string]bool{}
					for _, k := range *test.Operations {
						opKeys[k] = true
					}
				}
				err := Validate(&g, opKeys)
				if test.Valid && err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				if !test.Valid && err == nil {
					t.Fatalf("expected validation failure (%s), got none", rule.Title)
				}
			})
		}
	}
}
