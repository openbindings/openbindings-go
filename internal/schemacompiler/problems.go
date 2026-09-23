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
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) || hasRefCycle(ve) {
		return nil, false
	}
	problems = collect(ve)
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

// The numeric limits of schema evaluation. The backend parses numbers into
// math/big values: toward these limits the work grows, and past what
// math/big parses v6.0.3 dereferences nil or drops the keyword, so a value or
// schema holding such a number is not handed to it.
const (
	maxNumberLength   = 4096
	maxNumberExponent = 10000
)

// errNumericLimit is NumericLimit's error.
var errNumericLimit = errors.New("a number beyond the numeric limits of schema evaluation (at most 4096 characters, an exponent within ±10000)")

// NumericLimit reports the first number in v, in key order, beyond the
// numeric limits of schema evaluation: its location in v as a JSON Pointer
// ("" for v itself) and errNumericLimit. It returns a nil error when v holds
// none. Only a json.Number can exceed them; Go's numeric types cannot.
func NumericLimit(v any) (location string, err error) {
	switch v := v.(type) {
	case json.Number:
		if !withinNumericLimits(string(v)) {
			return "", errNumericLimit
		}
	case []any:
		for i, item := range v {
			if location, err := NumericLimit(item); err != nil {
				return jsonpointer.Format(fmt.Sprint(i)) + location, err
			}
		}
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			if location, err := NumericLimit(v[key]); err != nil {
				return jsonpointer.Format(key) + location, err
			}
		}
	}
	return "", nil
}

func withinNumericLimits(token string) bool {
	if len(token) > maxNumberLength {
		return false
	}
	if i := strings.LastIndexAny(token, "eE"); i >= 0 {
		exponent, ok := new(big.Int).SetString(token[i+1:], 10)
		if !ok || exponent.CmpAbs(big.NewInt(maxNumberExponent)) > 0 {
			return false
		}
	}
	return true
}

// Depth returns the nesting depth of v: 0 for a scalar, and one more than its
// deepest member or item for an array or object.
func Depth(v any) int {
	deepest := 0
	switch v := v.(type) {
	case []any:
		for _, item := range v {
			deepest = max(deepest, Depth(item)+1)
		}
		if len(v) == 0 {
			deepest = 1
		}
	case map[string]any:
		for _, member := range v {
			deepest = max(deepest, Depth(member)+1)
		}
		if len(v) == 0 {
			deepest = 1
		}
	}
	return deepest
}

func finiteProblem(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%v is not a JSON number", f)
	}
	return ""
}
