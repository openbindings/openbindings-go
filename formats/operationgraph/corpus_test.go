package operationgraph

// Conformance corpus adapter: runs this engine against the spec repository's
// operation-graph corpus unmodified (execution fixtures with mocked
// per-invocation operation behavior, and the OG-V validation fixtures).
//
// The corpus ROOT is located via OB_SPEC_CORPUS (the spec repo's
// conformance/ directory, the convention shared by every other format
// harness) or the local-dev sibling path (openbindings/spec next to
// openbindings/openbindings-go); this engine's fixtures live under the
// operation-graph subcorpus, which the adapter appends. The tests skip
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
	"sync"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/recolabs/gnata"
)

// ---------------------------------------------------------------------------
// Corpus location and fixture shapes
// ---------------------------------------------------------------------------

func corpusDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	// OB_SPEC_CORPUS names the conformance corpus ROOT; this engine's
	// fixtures live under the operation-graph subcorpus, which the adapter
	// appends. Tolerate an env var that already points at the subpath (an
	// older invocation) so both forms resolve.
	dir := root
	if filepath.Base(root) != "operation-graph" {
		dir = filepath.Join(root, "operation-graph")
	}
	if _, err := os.Stat(dir); err != nil {
		// OB_CORPUS_REQUIRED (set in CI) turns a missing corpus into a hard
		// failure so a mis-wired path turns CI red instead of silently green;
		// unset (local dev) it still skips.
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatalf("spec conformance corpus not found at %s (OB_CORPUS_REQUIRED is set; set OB_SPEC_CORPUS)", dir)
		}
		t.Skipf("spec conformance corpus not found at %s (set OB_SPEC_CORPUS)", dir)
	}
	return dir
}

type execFile struct {
	Fixtures []execFixture `json:"fixtures"`
}

type execFixture struct {
	ID                       string             `json:"id"`
	Name                     string             `json:"name"`
	Operations               map[string]*mockOp `json:"operations"`
	Graph                    json.RawMessage    `json:"graph"`
	Writes                   []any              `json:"writes"`
	AwaitOutputsBeforeWrites int                `json:"awaitOutputsBeforeWrites"`
	Expected                 struct {
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
		RefusedWrites *int   `json:"refusedWrites"`
	} `json:"expected"`
}

type mockOp struct {
	OnOpen      *mockResponse  `json:"onOpen"`
	ClosesAfter *int           `json:"closesAfter"`
	Responses   []mockResponse `json:"responses"`
}

type mockResponse struct {
	WhenInput  json.RawMessage `json:"whenInput"`
	WhenInputs *[]any          `json:"whenInputs"`
	Emit       *[]any          `json:"emit"`
	Fail       *string         `json:"fail"`
	FailData   json.RawMessage `json:"failData"`
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

func (m *mockBindingInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: mockFormat}}
}

func (m *mockBindingInvoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	opKey := ""
	if args.Binding != nil {
		opKey = args.Binding.Operation
	}
	op, ok := m.ops[opKey]
	if !ok {
		return openbindings.NewErroredInvocation[any, any](&openbindings.InvocationError{
			Code: "ERR_NO_MOCK",
		})
	}
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	// The controlled no-input mock closes from below as part of invocation
	// opening, before the caller can race a write (OG-EX-42).
	if op.ClosesAfter != nil && *op.ClosesAfter == 0 {
		_ = inv.CloseInput()
	}
	go m.serve(ctx, inv, op, opKey)
	return inv
}

func (m *mockBindingInvoker) serve(ctx context.Context, handle openbindings.BindingHandle[any, any], op *mockOp, opKey string) {
	if op.OnOpen != nil {
		if op.OnOpen.Emit != nil {
			for _, v := range *op.OnOpen.Emit {
				if err := handle.EmitOutput(v); err != nil {
					return
				}
			}
		}
		if op.OnOpen.Fail != nil {
			if len(op.OnOpen.FailData) > 0 {
				var value any
				if err := json.Unmarshal(op.OnOpen.FailData, &value); err != nil {
					handle.FireError(openbindings.NewInvocationError(openbindings.ErrCodeRuntime))
					return
				}
				handle.FireError(openbindings.NewInvocationErrorWithData(*op.OnOpen.Fail, value))
			} else {
				handle.FireError(openbindings.NewInvocationError(*op.OnOpen.Fail))
			}
			return
		}
	}

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
			Code: "ERR_NO_MOCK",
		})
		return
	}
	// A response may emit, fail, or emit-then-fail. Emitted events are the
	// outputs produced before any terminal, so they go out first; a fail then
	// terminates the invocation (fatal at a conduit unless onError, per-event
	// at an each node).
	if resp.Emit != nil {
		for _, v := range *resp.Emit {
			if err := handle.EmitOutput(v); err != nil {
				return
			}
		}
	}
	if resp.Fail != nil {
		if len(resp.FailData) > 0 {
			var value any
			if err := json.Unmarshal(resp.FailData, &value); err != nil {
				handle.FireError(openbindings.NewInvocationError(openbindings.ErrCodeRuntime))
				return
			}
			handle.FireError(openbindings.NewInvocationErrorWithData(*resp.Fail, value))
		} else {
			handle.FireError(openbindings.NewInvocationError(*resp.Fail))
		}
		return
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
	expr, err := gnata.Compile(expression)
	if err != nil {
		return nil, fmt.Errorf("compile jsonata: %w", err)
	}
	input, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal transform input: %w", err)
	}
	var result any
	if len(bindings) > 0 {
		vars := make(map[string]any, len(bindings))
		for k, v := range bindings {
			vars[k] = normalizeJSON(v)
		}
		result, err = expr.EvalBytesWithVars(context.Background(), input, vars)
	} else {
		result, err = expr.EvalBytes(context.Background(), input)
	}
	if err != nil {
		return nil, fmt.Errorf("evaluate jsonata: %w", err)
	}
	// gnata signals an undefined result as (nil, nil); a JSON null result is a
	// non-nil sentinel. Map undefined to the SDK sentinel so the engine fails
	// the node with TRANSFORM_UNDEFINED while null flows downstream.
	if result == nil {
		return nil, openbindings.ErrTransformUndefined
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
			"mock": {BindingSpec: mockFormat, Content: mustContent(map[string]any{})},
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
		Source:    openbindings.InvocationSource{BindingSpec: BindingSpec, Content: mustContent(doc)},
		Ref:       "#/graphs/g",
		Interface: iface,
	})

	outputsReady := make(chan struct{})
	var outputsReadyOnce sync.Once
	if fx.AwaitOutputsBeforeWrites == 0 {
		outputsReadyOnce.Do(func() { close(outputsReady) })
	}
	writerDone := make(chan int, 1)

	// Writer: the fixture's caller. Writes are paced: after each write the
	// caller yields until either the graph back-closes the input side (then
	// remaining writes are refused at the boundary, per the spec) or a short
	// settle window passes. ERR_INPUT_CLOSED on a write is the boundary
	// refusal, not a failure.
	go func() {
		defer func() { _ = call.Close() }()
		refused := 0
		defer func() { writerDone <- refused }()
		select {
		case <-outputsReady:
		case <-ctx.Done():
			return
		}
		// The corpus schedule begins writes after graph startup. Give the
		// asynchronous SDK adapter time to open held conduits and propagate an
		// immediate input-side close before the first caller write.
		select {
		case <-call.InputClosed():
			refused += len(fx.Writes)
			return
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			return
		}
		for i, w := range fx.Writes {
			if i > 0 {
				select {
				case <-call.InputClosed():
					refused += len(fx.Writes) - i
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
			select {
			case <-call.InputClosed():
				refused += len(fx.Writes) - i
				return
			default:
			}
			if err := call.Write(ctx, w); err != nil {
				refused += len(fx.Writes) - i
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
		if fx.AwaitOutputsBeforeWrites > 0 && len(outputs) >= fx.AwaitOutputsBeforeWrites {
			outputsReadyOnce.Do(func() { close(outputsReady) })
		}
	}
	outputsReadyOnce.Do(func() { close(outputsReady) })
	refusedWrites := <-writerDone
	if fx.Expected.RefusedWrites != nil && refusedWrites != *fx.Expected.RefusedWrites {
		t.Fatalf("refused writes = %d, want %d", refusedWrites, *fx.Expected.RefusedWrites)
	}

	// Terminal expectations.
	if fx.Expected.Error {
		if terminal == nil {
			t.Fatalf("expected terminal error %v, graph completed normally with outputs %s", fx.Expected.ErrorDetail, canon(outputs))
		}
		if detail, ok := abstractTerminalRecord(fx.Expected.ErrorDetail); ok {
			if terminal.Code != detail.code {
				t.Fatalf("terminal error code = %q, want %q (err: %v)", terminal.Code, detail.code, terminal)
			}
			if terminal.HasData() != detail.dataPresent {
				t.Fatalf("terminal data presence = %v, want %v", terminal.HasData(), detail.dataPresent)
			}
			if detail.dataPresent && canon(terminal.Data) != canon(detail.data) {
				t.Fatalf("terminal data = %s, want %s", canon(terminal.Data), canon(detail.data))
			}
		} else {
			// A graph-defined terminal (exit or unhandled per-event failure)
			// carries the event as structured details.
			if terminal.Code != openbindings.ErrCodeOperationGraphExit {
				t.Fatalf("terminal error code = %q, want %q (err: %v)", terminal.Code, openbindings.ErrCodeOperationGraphExit, terminal)
			}
			if canon(terminal.Data) != canon(fx.Expected.ErrorDetail) {
				t.Fatalf("exit error detail = %s, want %s", canon(terminal.Data), canon(fx.Expected.ErrorDetail))
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

type expectedAbstractTerminal struct {
	code        string
	data        any
	dataPresent bool
}

func abstractTerminalRecord(value any) (expectedAbstractTerminal, bool) {
	record, ok := value.(map[string]any)
	if !ok || len(record) < 1 || len(record) > 2 {
		return expectedAbstractTerminal{}, false
	}
	code, ok := record["code"].(string)
	if !ok {
		return expectedAbstractTerminal{}, false
	}
	data, dataPresent := record["data"]
	for key := range record {
		if key != "code" && key != "data" {
			return expectedAbstractTerminal{}, false
		}
	}
	return expectedAbstractTerminal{code: code, data: data, dataPresent: dataPresent}, true
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

// mustContent marshals a Go value into the raw-JSON content carriage
// (Source.Content presence semantics: raw JSON, nil = absent).
func mustContent(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
