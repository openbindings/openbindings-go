package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
)

// scriptedEvaluator answers as its functions say, whatever the bundle.
type scriptedEvaluator struct {
	compile  func(ctx context.Context) (CompiledSchema, error)
	validate func(ctx context.Context, value any) error
}

func (e scriptedEvaluator) Compile(ctx context.Context, _ SchemaBundle) (CompiledSchema, error) {
	if e.compile != nil {
		return e.compile(ctx)
	}
	return scriptedSchema(e), nil
}

type scriptedSchema scriptedEvaluator

func (s scriptedSchema) Validate(ctx context.Context, value any) error { return s.validate(ctx, value) }

const oneOperation = `{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"object"}}}}`

func compileScripted(t *testing.T, ctx context.Context, e SchemaEvaluator) (*ValueContract, error) {
	t.Helper()
	compiler, err := NewValueContractCompiler(e)
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := compiler.Resolve(context.Background(), mustDecodeDocument(t, oneOperation))
	if err != nil {
		t.Fatal(err)
	}
	return contracts.CompileInput(ctx, "op")
}

func answering(t *testing.T, answer func(ctx context.Context, value any) error) *ValueContract {
	t.Helper()
	contract, err := compileScripted(t, context.Background(), scriptedEvaluator{validate: answer})
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

type libraryError struct{ detail string }

func (e *libraryError) Error() string { return e.detail }

func TestAnswers_Read(t *testing.T) {
	mismatch := &MismatchError{Problems: []SchemaProblem{{InstanceLocation: "/b", Message: "second"}, {InstanceLocation: "/a/nowhere", Message: "first"}}}
	var typedNil *MismatchError
	for name, c := range map[string]struct {
		answer  error
		panics  bool
		verdict string
	}{
		"nil":                          {nil, false, "valid"},
		"a mismatch":                   {mismatch, false, "mismatch"},
		"a wrapped mismatch":           {fmt.Errorf("library: %w", mismatch), false, "mismatch"},
		"a typed nil mismatch":         {typedNil, false, "no verdict"},
		"any other error":              {errors.New("pattern engine limit"), false, "no verdict"},
		"a mismatch and ErrNoVerdict":  {errors.Join(mismatch, ErrNoVerdict), false, "no verdict"},
		"a mismatch and ErrUndefined":  {errors.Join(mismatch, ErrUndefined), false, "no verdict"},
		"a NoVerdictError":             {&NoVerdictError{Location: "#/x", Cause: errors.New("y")}, false, "no verdict"},
		"a mismatch and a deadline":    {errors.Join(mismatch, context.DeadlineExceeded), false, "no verdict"},
		"a panic":                      {nil, true, "no verdict"},
		"ErrMismatch alone, no errors": {ErrMismatch, false, "no verdict"},
	} {
		contract := answering(t, func(context.Context, any) error {
			if c.panics {
				panic("boom")
			}
			return c.answer
		})
		err := contract.Validate(context.Background(), map[string]any{"a": true, "b": true})
		if got := verdictOf(t, err); got != c.verdict {
			t.Errorf("%s: %s, want %s: %v", name, got, c.verdict, err)
		}
		// Whatever the evaluator says, core's promises about a returned
		// error's chain hold.
		if errors.Is(err, ErrUndefined) || errors.Is(err, ErrNoValueContract) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: a promise about the chain broke: %v", name, err)
		}
		var refusal *NoVerdictError
		if errors.As(err, &refusal) && refusal.Location != "" {
			t.Errorf("%s: an evaluator's refusal is located: %v", name, refusal)
		}
	}
}

// A mismatch's problems are copied, located where they resolve in the value,
// and sorted; one with none gets a whole-value problem.
func TestAnswers_ProblemsRepaired(t *testing.T) {
	contract := answering(t, func(context.Context, any) error {
		return &MismatchError{Problems: []SchemaProblem{{InstanceLocation: "/b", Message: "second"}, {InstanceLocation: "/a/nowhere", Message: "first"}, {InstanceLocation: "not a pointer", Message: "zero"}}}
	})
	var mismatch *MismatchError
	if !errors.As(contract.Validate(context.Background(), map[string]any{"a": true, "b": true}), &mismatch) {
		t.Fatal("want a mismatch")
	}
	want := []SchemaProblem{{"", "zero"}, {"/a", "first"}, {"/b", "second"}}
	if fmt.Sprint(mismatch.Problems) != fmt.Sprint(want) {
		t.Fatalf("problems %v, want %v", mismatch.Problems, want)
	}
	empty := answering(t, func(context.Context, any) error { return &MismatchError{} })
	if !errors.As(empty.Validate(context.Background(), map[string]any{}), &mismatch) || len(mismatch.Problems) != 1 || mismatch.Problems[0].InstanceLocation != "" {
		t.Fatalf("a mismatch with no problems: %v", mismatch)
	}
}

// The evaluator's own errors stay reachable when they keep the contract, and
// in Cause always.
func TestAnswers_EvaluatorErrorsStayReachable(t *testing.T) {
	cause := &libraryError{"regexp backtracking limit"}
	contract := answering(t, func(context.Context, any) error { return fmt.Errorf("schemaeval: %w", cause) })
	var found *libraryError
	if err := contract.Validate(context.Background(), map[string]any{}); !errors.As(err, &found) || found != cause {
		t.Fatalf("the library's error is not reachable: %v", err)
	}
	contract = answering(t, func(context.Context, any) error { return errors.Join(cause, ErrUndefined) })
	err := contract.Validate(context.Background(), map[string]any{})
	var refusal *NoVerdictError
	if errors.As(err, &found) || !errors.As(err, &refusal) || !errors.As(refusal.Cause, &found) {
		t.Fatalf("an error breaking the contract stays in Cause alone: %v", err)
	}
}

// A context error reaches the result only when it is the caller's own, after
// the caller's ctx is done.
func TestAnswers_ContextErrors(t *testing.T) {
	budget := answering(t, func(context.Context, any) error { return context.DeadlineExceeded })
	if err := budget.Validate(context.Background(), map[string]any{}); !errors.Is(err, ErrNoVerdict) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an evaluator's own budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := answering(t, func(context.Context, any) error { cancel(); return context.Canceled })
	if err := stopped.Validate(ctx, map[string]any{}); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrNoVerdict) {
		t.Fatalf("the caller's cancellation: %v", err)
	}
	if err := stopped.Validate(ctx, map[string]any{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a done ctx before validating: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	mixed := answering(t, func(context.Context, any) error { cancel(); return context.DeadlineExceeded })
	if err := mixed.Validate(ctx, map[string]any{}); errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a deadline the caller did not set, after the caller cancelled: %v", err)
	}
}

func TestCompile_Answers(t *testing.T) {
	for name, compile := range map[string]func(context.Context) (CompiledSchema, error){
		"an error":   func(context.Context) (CompiledSchema, error) { return nil, errors.New("unsupported keyword") },
		"nil, nil":   func(context.Context) (CompiledSchema, error) { return nil, nil },
		"a panic":    func(context.Context) (CompiledSchema, error) { panic("boom") },
		"a sentinel": func(context.Context) (CompiledSchema, error) { return nil, ErrUndefined },
	} {
		contract, err := compileScripted(t, context.Background(), scriptedEvaluator{compile: compile})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := contract.Err(); !errors.Is(err, ErrNoVerdict) || errors.Is(err, ErrUndefined) {
			t.Errorf("%s: %v", name, err)
		}
		if err := contract.Validate(context.Background(), "x"); !errors.Is(err, ErrNoVerdict) {
			t.Errorf("%s: validating: %v", name, err)
		}
	}
	// A ctx done before core calls the evaluator, or while its Compile fails,
	// gives the ctx's error and no value contract.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if contract, err := compileScripted(t, ctx, scriptedEvaluator{}); contract != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("a done ctx: %v, %v", contract, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	failing := scriptedEvaluator{compile: func(context.Context) (CompiledSchema, error) { cancel(); return nil, errors.New("stopped") }}
	if contract, err := compileScripted(t, ctx, failing); contract != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("a failure under a done ctx: %v, %v", contract, err)
	}
}

func TestValues_Read(t *testing.T) {
	type task struct {
		Title string  `json:"title"`
		Tags  []int   `json:"tags"`
		Skip  string  `json:"-"`
		Score float64 `json:"score"`
	}
	var seen any
	contract := answering(t, func(_ context.Context, value any) error { seen = value; return nil })
	if err := contract.Validate(context.Background(), task{Title: "t", Skip: "\xff", Score: 0.1}); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"title": "t", "tags": nil, "score": json.Number("0.1")}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("read %#v, want %#v", seen, want)
	}
	// A byte slice whose element marshals itself is an array, as
	// encoding/json writes it.
	if err := contract.Validate(context.Background(), []digit{1}); err != nil || fmt.Sprint(seen) != "[1]" {
		t.Fatalf("read %#v: %v", seen, err)
	}
	if err := contract.Validate(context.Background(), []pointerDigit{2}); err != nil || fmt.Sprint(seen) != "[2]" {
		t.Fatalf("read %#v: %v", seen, err)
	}
	type body []byte
	raw := []byte(`{}`)
	cycle := map[string]any{}
	cycle["self"] = cycle
	for name, value := range map[string]any{
		"a []byte":              []byte(`{}`),
		"a named byte slice":    body(`{}`),
		"a pointer to a []byte": &raw,
		"invalid text":          map[string]any{"a": "\xff"},
		"a NaN":                 math.NaN(),
		"a channel":             make(chan int),
		"a cycle":               cycle,
	} {
		requireCategory(t, name, contract.Validate(context.Background(), value), "")
	}
	for name, value := range map[string]any{
		"a repeated name in raw JSON": json.RawMessage(`{"a":1,"a":2}`),
		"a lone surrogate":            json.RawMessage(`"\ud800"`),
	} {
		if err := contract.Validate(context.Background(), value); !errors.Is(err, ErrNoVerdict) || errors.Is(err, ErrInconclusive) {
			t.Errorf("%s: want no verdict, got %v", name, err)
		}
	}
	for name, text := range map[string]string{"not JSON": `{`, "two values": `1 2`, "invalid UTF-8": "\"\xff\""} {
		requireCategory(t, name, contract.ValidateJSON(context.Background(), []byte(text)), "")
	}
	if err := contract.ValidateJSON(context.Background(), []byte(`{"a":1,"a":2}`)); !errors.Is(err, ErrNoVerdict) || errors.Is(err, ErrInconclusive) {
		t.Errorf("a repeated name: %v", err)
	}
	if err := contract.ValidateJSON(context.Background(), []byte(` {"n": 1e400} `)); err != nil || fmt.Sprint(seen) != "map[n:1e400]" {
		t.Errorf("numbers read exactly: %v %v", err, seen)
	}
}

// A zero or nil value contract gets no verdict rather than a panic.
func TestValueContract_Zero(t *testing.T) {
	var zero ValueContract
	var nilContract *ValueContract
	for _, c := range []*ValueContract{&zero, nilContract} {
		if err := c.Validate(context.Background(), "x"); !errors.Is(err, ErrNoVerdict) {
			t.Errorf("%v", err)
		}
		if err := c.Err(); !errors.Is(err, ErrNoVerdict) {
			t.Errorf("%v", err)
		}
	}
}

// digit is a byte that marshals itself as a JSON number.
type digit byte

func (d digit) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprint(int(d))), nil }

// pointerDigit marshals itself through a pointer, which encoding/json uses
// for a slice's elements.
type pointerDigit byte

func (d *pointerDigit) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprint(int(*d))), nil }
