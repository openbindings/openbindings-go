package schemacompiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/jsonvalue"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// Problem is one established schema mismatch: where in the validated value
// it lies, as RFC 6901 reference tokens, and what failed there.
type Problem struct {
	Location []string
	Message  string
}

// Line renders the problem as "location: message", with the location as a
// JSON Pointer into the validated value.
func (p Problem) Line() string {
	if len(p.Location) == 0 {
		return p.Message
	}
	return jsonpointer.Format(p.Location...) + ": " + p.Message
}

// kindPrinter renders backend error kinds, which implement
// LocalizedString(*message.Printer) rather than String().
var kindPrinter = message.NewPrinter(language.English)

// Outcome classifies a backend validation error. mismatch is true for an
// established mismatch between value and schema, with its problems. It is
// false for anything that reached no verdict: an error that is not a
// validation result, or a reference cycle that never advances, which leaves
// the schema unevaluable.
func Outcome(err error) (problems []Problem, mismatch bool) {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) || hasRefCycle(ve) {
		return nil, false
	}
	return Problems(ve), true
}

func hasRefCycle(ve *jsonschema.ValidationError) bool {
	if _, ok := ve.ErrorKind.(*kind.RefCycle); ok {
		return true
	}
	for _, cause := range ve.Causes {
		if hasRefCycle(cause) {
			return true
		}
	}
	return false
}

// Problems flattens a backend validation error into one problem per failed
// constraint, sorted by location and message. An anyOf or oneOf that no
// alternative satisfies is one problem at its own location, stating what each
// alternative lacked; a report of one problem per alternative would read as
// several defects. A member name that fails propertyNames is located at the
// member.
func Problems(err error) []Problem {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Problem{{Message: err.Error()}}
	}
	problems := collect(ve)
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if order := slices.Compare(a.Location, b.Location); order != 0 {
			return order
		}
		return strings.Compare(a.Message, b.Message)
	})
	return problems
}

func collect(ve *jsonschema.ValidationError) []Problem {
	switch k := ve.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf:
		if len(ve.Causes) > 0 {
			var alternatives []string
			for _, cause := range ve.Causes {
				for _, problem := range collect(cause) {
					alternatives = append(alternatives, relativeText(ve.InstanceLocation, problem))
				}
			}
			slices.Sort(alternatives)
			return []Problem{{
				Location: ve.InstanceLocation,
				Message:  "satisfies none of the alternatives: " + strings.Join(alternatives, "; "),
			}}
		}
	case *kind.PropertyNames:
		// The backend validates the name as its own instance, so the causes
		// carry no location; the name is the member it names.
		var messages []string
		for _, cause := range ve.Causes {
			for _, problem := range collect(cause) {
				messages = append(messages, problem.Message)
			}
		}
		if len(messages) == 0 {
			messages = []string{ve.ErrorKind.LocalizedString(kindPrinter)}
		}
		slices.Sort(messages)
		location := append(slices.Clone(ve.InstanceLocation), k.Property)
		return []Problem{{Location: location, Message: "invalid member name: " + strings.Join(messages, "; ")}}
	}
	if len(ve.Causes) == 0 {
		return []Problem{{Location: ve.InstanceLocation, Message: ve.ErrorKind.LocalizedString(kindPrinter)}}
	}
	var out []Problem
	for _, cause := range ve.Causes {
		out = append(out, collect(cause)...)
	}
	return out
}

// relativeText renders a problem found under base, naming its location
// relative to base when it lies deeper.
func relativeText(base []string, problem Problem) string {
	if len(problem.Location) <= len(base) {
		return problem.Message
	}
	return jsonpointer.Format(problem.Location[len(base):]...) + ": " + problem.Message
}

// ValueProblem states why v is not a JSON value the validator accepts, or
// returns "" when it is one: nil, a bool, a string, a number (a valid
// json.Number, a finite float, or an integer type), or a []any or
// map[string]any of JSON values. A value outside that domain has no JSON
// meaning to validate, so validation reaches no verdict on it.
func ValueProblem(v any) string {
	switch v := v.(type) {
	case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return ""
	case json.Number:
		if !jsonvalue.IsNumber(v) {
			return fmt.Sprintf("json.Number %q is not a JSON number", string(v))
		}
		return ""
	case float32:
		return finiteProblem(float64(v))
	case float64:
		return finiteProblem(v)
	case []any:
		for i, item := range v {
			if problem := ValueProblem(item); problem != "" {
				return fmt.Sprintf("/%d: %s", i, problem)
			}
		}
		return ""
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			if problem := ValueProblem(v[key]); problem != "" {
				return jsonpointer.Format(key) + ": " + problem
			}
		}
		return ""
	default:
		return fmt.Sprintf("a Go %T is not a JSON value; decode the value as generic JSON first", v)
	}
}

// NumericLimit reports a number in v beyond the numeric work the backend is
// given (jsonvalue.CheckNumericWork: at most 4096 characters, an exponent
// within ±10000), wrapping the *jsonvalue.CapabilityError, or returns nil
// when v holds none. The backend parses numbers into math/big values; past
// those limits a parse fails and v6.0.3 then dereferences nil or drops the
// keyword, so such a value or schema is not handed to it.
func NumericLimit(v any) error {
	switch v := v.(type) {
	case json.Number:
		if err := jsonvalue.CheckNumericWork(v); err != nil {
			return fmt.Errorf("a number beyond the numeric limits of schema evaluation (at most 4096 characters, an exponent within ±10000): %w", err)
		}
	case []any:
		for i, item := range v {
			if err := NumericLimit(item); err != nil {
				return fmt.Errorf("/%d: %w", i, err)
			}
		}
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			if err := NumericLimit(v[key]); err != nil {
				return fmt.Errorf("%s: %w", jsonpointer.Format(key), err)
			}
		}
	}
	return nil
}

func finiteProblem(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%v is not a JSON number", f)
	}
	return ""
}
