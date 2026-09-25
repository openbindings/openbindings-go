package schemacompiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
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

// kindPrinter renders backend error kinds, which implement
// LocalizedString(*message.Printer) rather than String().
var kindPrinter = message.NewPrinter(language.English)

// Outcome classifies a backend validation error. mismatch is true for an
// established mismatch between value and schema, with its problems: one per
// failed constraint, sorted by location and message. It is false for
// anything that reached no verdict: an error that is not a validation result,
// or a reference cycle that never advances, which leaves the schema
// unevaluable.
//
// An anyOf or oneOf that no alternative satisfies is one problem at its own
// location, stating what each alternative lacked; a report of one problem per
// alternative would read as several defects. A member name that fails
// propertyNames is located at the member.
func Outcome(err error) (problems []Problem, mismatch bool) {
	return Substitution{}.Outcome(err)
}

// Outcome classifies a validation error of the Value as the package's Outcome
// does, stating a comparison the schema made with a stand-in as made with the
// number it stands for.
func (s Substitution) Outcome(err error) (problems []Problem, mismatch bool) {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) || hasRefCycle(ve) {
		return nil, false
	}
	problems = collect(ve, s.standsFor)
	slices.SortStableFunc(problems, func(a, b Problem) int {
		if order := slices.Compare(a.Location, b.Location); order != 0 {
			return order
		}
		return strings.Compare(a.Message, b.Message)
	})
	return problems, true
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

func collect(ve *jsonschema.ValidationError, standsFor map[string]string) []Problem {
	switch k := ve.ErrorKind.(type) {
	case *kind.AnyOf, *kind.OneOf:
		if len(ve.Causes) > 0 {
			var alternatives []string
			for _, cause := range ve.Causes {
				for _, problem := range collect(cause, standsFor) {
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
			for _, problem := range collect(cause, standsFor) {
				messages = append(messages, problem.Message)
			}
		}
		if len(messages) == 0 {
			messages = []string{ve.ErrorKind.LocalizedString(kindPrinter)}
		}
		slices.Sort(messages)
		location := append(slices.Clone(ve.InstanceLocation), k.Property)
		return []Problem{{Location: location, Message: "invalid member name: " + strings.Join(messages, "; ")}}
	case *kind.AdditionalProperties:
		// The backend lists the members in map order; sorted, the same value
		// gives the same message every time.
		sorted := &kind.AdditionalProperties{Properties: slices.Sorted(slices.Values(k.Properties))}
		return []Problem{{Location: ve.InstanceLocation, Message: sorted.LocalizedString(kindPrinter)}}
	}
	if got, want, compared := comparison(ve.ErrorKind); compared {
		if number, standIn := standsFor[got.RatString()]; standIn {
			// The schema compared the stand-in; the finding states the number.
			limit, _ := want.Float64()
			return []Problem{{Location: ve.InstanceLocation, Message: kindPrinter.Sprintf("%s: got %s, want %v", ve.ErrorKind.KeywordPath()[0], number, limit)}}
		}
	}
	if len(ve.Causes) == 0 {
		return []Problem{{Location: ve.InstanceLocation, Message: ve.ErrorKind.LocalizedString(kindPrinter)}}
	}
	var out []Problem
	for _, cause := range ve.Causes {
		out = append(out, collect(cause, standsFor)...)
	}
	return out
}

// comparison returns the numbers a comparison failed on: the value's and the
// schema's.
func comparison(k jsonschema.ErrorKind) (got, want *big.Rat, compared bool) {
	switch k := k.(type) {
	case *kind.Minimum:
		return k.Got, k.Want, true
	case *kind.Maximum:
		return k.Got, k.Want, true
	case *kind.ExclusiveMinimum:
		return k.Got, k.Want, true
	case *kind.ExclusiveMaximum:
		return k.Got, k.Want, true
	case *kind.MultipleOf:
		return k.Got, k.Want, true
	}
	return nil, nil, false
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
		if !IsNumber(v) {
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

// IsNumber reports whether n holds exactly one JSON number token, which
// encoding/json does not check of an empty json.Number (it encodes one as 0).
func IsNumber(n json.Number) bool {
	s := string(n)
	return s != "" && (s[0] == '-' || s[0] >= '0' && s[0] <= '9') && strings.TrimSpace(s) == s && json.Valid([]byte(s))
}

func finiteProblem(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%v is not a JSON number", f)
	}
	return ""
}
