package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

// categories names the outcome categories an error matches, of ErrMismatch,
// ErrNoVerdict, and ErrInconclusive; a value-validation error matches exactly
// one.
func categories(err error) []string {
	var matched []string
	for name, sentinel := range map[string]error{"ErrMismatch": ErrMismatch, "ErrNoVerdict": ErrNoVerdict, "ErrInconclusive": ErrInconclusive} {
		if errors.Is(err, sentinel) {
			matched = append(matched, name)
		}
	}
	return matched
}

func requireCategory(t *testing.T, name string, err error, want string) {
	t.Helper()
	if got := categories(err); len(got) != 1 || got[0] != want {
		t.Errorf("%s: matches %v, want %s alone: %v", name, got, want, err)
	}
}

// inconclusiveVariants are evaluator answers that speak ErrInconclusive, which
// the evaluator contract reserves for core: alone, wrapped, and joined with a
// *MismatchError, plainly and wrapped.
func inconclusiveVariants() map[string]error {
	mismatch := &MismatchError{Problems: []SchemaProblem{{InstanceLocation: "", Message: "type"}}}
	return map[string]error{
		"ErrInconclusive":                 ErrInconclusive,
		"wrapped":                         fmt.Errorf("evaluator: %w", ErrInconclusive),
		"joined with a mismatch":          errors.Join(mismatch, ErrInconclusive),
		"joined with a mismatch, wrapped": fmt.Errorf("evaluator: %w", errors.Join(ErrInconclusive, mismatch)),
	}
}

// An evaluator that answers with ErrInconclusive breaks its contract: core
// reads the answer as no verdict, matching ErrNoVerdict alone, and keeps the
// evaluator's error in Cause.
func TestCategories_EvaluatorSpeaksInconclusive(t *testing.T) {
	for name, answer := range inconclusiveVariants() {
		var compiled atomic.Int32
		contract, err := compileScripted(t, context.Background(), scriptedEvaluator{compile: func(context.Context) (CompiledSchema, error) {
			compiled.Add(1)
			return nil, answer
		}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if compiled.Load() != 1 {
			t.Errorf("%s: Compile was called %d times", name, compiled.Load())
		}
		requireCategory(t, name+" (Compile, Err)", contract.Err(), "ErrNoVerdict")
		requireCategory(t, name+" (Compile, Validate)", contract.Validate(context.Background(), map[string]any{}), "ErrNoVerdict")
		var refusal *NoVerdictError
		if !errors.As(contract.Err(), &refusal) || refusal.Cause != answer {
			t.Errorf("%s (Compile): the evaluator's error is not the Cause: %v", name, contract.Err())
		}

		var validated atomic.Int32
		contract = answering(t, func(context.Context, any) error {
			validated.Add(1)
			return answer
		})
		err = contract.Validate(context.Background(), map[string]any{})
		requireCategory(t, name+" (Validate)", err, "ErrNoVerdict")
		if validated.Load() != 1 {
			t.Errorf("%s: Validate was called %d times", name, validated.Load())
		}
		if !errors.As(err, &refusal) || refusal.Cause != answer {
			t.Errorf("%s (Validate): the evaluator's error is not the Cause: %v", name, err)
		}
		if errors.As(err, new(*MismatchError)) {
			t.Errorf("%s (Validate): a mismatch is reachable: %v", name, err)
		}
	}
}

// failingMarshaler is a Go value whose encoding fails with its error.
type failingMarshaler struct{ err error }

func (m failingMarshaler) MarshalJSON() ([]byte, error) { return nil, m.err }

// A value whose own encoding fails is not a JSON value, whatever its
// marshaler says: the error matches ErrInconclusive alone, and the evaluator
// is never called.
func TestCategories_MarshalerErrorsAreNotVerdicts(t *testing.T) {
	var validated atomic.Int32
	contract := answering(t, func(context.Context, any) error {
		validated.Add(1)
		return nil
	})
	said := map[string]error{
		"ErrMismatch":       ErrMismatch,
		"a *MismatchError":  &MismatchError{Problems: []SchemaProblem{{Message: "made by the marshaler"}}},
		"ErrNoVerdict":      ErrNoVerdict,
		"ErrInconclusive":   ErrInconclusive,
		"a *NoVerdictError": &NoVerdictError{Location: "#/x", Cause: errors.New("made by the marshaler")},
	}
	for name, err := range said {
		for form, failure := range map[string]error{
			"":          err,
			", wrapped": fmt.Errorf("marshaler: %w", err),
			", joined":  errors.Join(errors.New("marshaler"), err),
		} {
			for shape, value := range map[string]any{
				"the value":        failingMarshaler{failure},
				"a member":         map[string]any{"a": failingMarshaler{failure}},
				"a pointer to one": &failingMarshaler{failure},
			} {
				label := fmt.Sprintf("%s%s, as %s", name, form, shape)
				got := contract.Validate(context.Background(), value)
				requireCategory(t, label, got, "ErrInconclusive")
				if errors.As(got, new(*NoVerdictError)) || errors.As(got, new(*MismatchError)) {
					t.Errorf("%s: the marshaler's error is reachable: %v", label, got)
				}
			}
		}
	}
	if validated.Load() != 0 {
		t.Fatalf("the evaluator was called %d times", validated.Load())
	}
	// A marshaler that writes nothing is not a JSON value either, and core's
	// own refusal to read a value exactly is still no verdict; neither calls
	// the evaluator.
	requireCategory(t, "a marshaler writing nothing", contract.Validate(context.Background(), failingMarshaler{}), "ErrInconclusive")
	requireCategory(t, "a repeated member name", contract.Validate(context.Background(), repeatingMarshaler{}), "ErrNoVerdict")
	if validated.Load() != 0 {
		t.Fatalf("the evaluator was called %d times", validated.Load())
	}
}

// repeatingMarshaler writes an object repeating a member name, which
// encoding/json accepts and core cannot read exactly.
type repeatingMarshaler struct{}

func (repeatingMarshaler) MarshalJSON() ([]byte, error) { return []byte(`{"a":1,"a":2}`), nil }

// Raw JSON in a Go value is read as JSON text is: Validate on a
// json.RawMessage gives exactly what ValidateJSON gives on the same bytes,
// and raw JSON a value holds gets the category the same text gets, so depth
// and a lone surrogate are core's refusal to read the value exactly, never
// "not a JSON value".
func TestCategories_RawJSONReadsAsText(t *testing.T) {
	var validated atomic.Int32
	contract := answering(t, func(context.Context, any) error {
		validated.Add(1)
		return nil
	})
	ctx := context.Background()
	backslash := string(rune(92)) // built, so that no cleanup turns the escape into a character
	texts := map[string]string{
		"nested past the decoder": strings.Repeat("[", 10001) + strings.Repeat("]", 10001),
		"a lone surrogate":        `"` + backslash + `ud800"`,
		"a repeated name":         `{"a":1,"a":2}`,
		"an object":               `{"a":1}`,
		"surrounded by space":     ` {"a":1} `,
		"not JSON":                `{`,
		"two values":              `1 2`,
		"empty":                   ``,
	}
	for name, text := range texts {
		want := contract.ValidateJSON(ctx, []byte(text))
		raw := json.RawMessage(text)
		for form, value := range map[string]any{"a json.RawMessage": raw, "a *json.RawMessage": &raw} {
			got := contract.Validate(ctx, value)
			if fmt.Sprint(got) != fmt.Sprint(want) || fmt.Sprint(categories(got)) != fmt.Sprint(categories(want)) {
				t.Errorf("%s, as %s: %v, want what ValidateJSON gives: %v", name, form, got, want)
			}
		}
		// Held in a value, the same text gets the same category as the
		// value's text.
		held := contract.Validate(ctx, map[string]any{"x": raw})
		whole := contract.ValidateJSON(ctx, []byte(`{"x":`+text+`}`))
		if fmt.Sprint(categories(held)) != fmt.Sprint(categories(whole)) {
			t.Errorf("%s, held in a value: %v (%v), want the category of %v (%v)", name, held, categories(held), whole, categories(whole))
		}
	}
	if got := contract.Validate(ctx, json.RawMessage(nil)); got != nil {
		t.Errorf("a nil json.RawMessage is null, as encoding/json writes it: %v", got)
	}
	if got, want := contract.Validate(ctx, json.RawMessage(nil)), contract.ValidateJSON(ctx, []byte(`null`)); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("a nil json.RawMessage: %v, want %v", got, want)
	}
	requireCategory(t, "nested past the decoder, held", contract.Validate(ctx, map[string]any{"x": json.RawMessage(texts["nested past the decoder"])}), "ErrNoVerdict")
	if validated.Load() == 0 {
		t.Fatal("the evaluator was never called on a readable value")
	}
}
