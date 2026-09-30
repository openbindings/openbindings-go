package schemaeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// The numeric limits of the schema library. santhosh-tekuri/jsonschema v6
// parses the numbers it reads into math/big values: toward these limits the
// work grows, and past what math/big parses it drops a schema keyword holding
// the number, judges the number neither an integer nor equal to any other, or
// dereferences nil. So a number beyond them is never handed to it where it
// reads one: a value's is replaced by a stand-in (substitute) or gets no
// verdict, and a schema keyword holding one gives no verdict where evaluation
// reaches it (prepare). A number the library only carries, as in default, is
// harmless.
const (
	maxNumberLength   = 4096
	maxNumberExponent = 10000
)

// errNumericLimit is numericLimits's error.
var errNumericLimit = errors.New("a number beyond this evaluator's numeric limits (at most 4096 characters, an exponent within ±10000)")

// numericLimits reports the first number in v, in key order, beyond the
// numeric limits: its location in v as a JSON Pointer ("" for v itself) and
// errNumericLimit. It returns a nil error when v holds none.
func numericLimits(v any) (location string, err error) {
	return numericLimit(v, map[container]bool{})
}

// numericLimit is numericLimits, with within recording the containers found to
// hold no number beyond the limits, so a value sharing its parts is walked
// through each once.
func numericLimit(v any, within map[container]bool) (string, error) {
	held, isContainer := containerOf(v)
	if isContainer && within[held] {
		return "", nil
	}
	switch v := v.(type) {
	case json.Number:
		if !withinNumericLimits(string(v)) {
			return "", errNumericLimit
		}
	case []any:
		for i, item := range v {
			if location, err := numericLimit(item, within); err != nil {
				return pointerOf(fmt.Sprint(i)) + location, err
			}
		}
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			if location, err := numericLimit(v[key], within); err != nil {
				return pointerOf(key) + location, err
			}
		}
	}
	if isContainer {
		within[held] = true
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

// substitution is a value to validate in place of another: the other with
// each number beyond the numeric limits of schema evaluation replaced by a
// stand-in within them.
type substitution struct {
	// Value is the value to validate.
	Value any
	// standsFor maps each stand-in, by its value as a fraction, to a number
	// it stands for.
	standsFor map[string]string
}

// substitute returns v with each number beyond the numeric limits of schema
// evaluation replaced by a stand-in within them. v itself is not changed, and
// is the Value when it holds no such number.
//
// A stand-in has the sign of the number it stands for, is an integer exactly
// when that number is, and equals another number of the Value exactly when
// the number it stands for equals the corresponding number of v. A schema
// that tells numbers apart by nothing else (by type, by uniqueItems, by
// comparison with zero, and by const or enum holding no number) reaches the
// same verdict on the Value as on v, and the Substitution's Outcome states
// each finding as made on v.
func substitute(v any) substitution {
	if _, err := numericLimits(v); err == nil {
		return substitution{Value: v}
	}
	// The numbers within the limits, by value, which a stand-in equals
	// exactly when the number it stands for does.
	within := map[string]json.Number{}
	forEachNumber(v, func(n json.Number) {
		if withinNumericLimits(string(n)) {
			key, _ := numberKey(string(n))
			within[key] = n
		}
	})
	s := substitution{standsFor: map[string]string{}}
	chosen := map[string]json.Number{}
	fresh := 0
	// copies holds the copy made of each container, so a value sharing its
	// parts shares their copies and is copied through each once.
	copies := map[container]any{}
	var substituteAny func(v any) any
	substituteOne := func(v any) any {
		switch v := v.(type) {
		case json.Number:
			if withinNumericLimits(string(v)) {
				return v
			}
			key, integer := numberKey(string(v))
			if standIn, found := chosen[key]; found {
				return standIn
			}
			standIn, equal := within[key]
			switch {
			case equal:
			case key == "0":
				standIn = "0"
			default:
				for {
					fresh++
					standIn = json.Number(strconv.Itoa(fresh))
					if !integer {
						standIn += ".5"
					}
					if strings.HasPrefix(key, "-") {
						standIn = "-" + standIn
					}
					if taken, _ := numberKey(string(standIn)); within[taken] == "" {
						break
					}
				}
				value, _ := new(big.Rat).SetString(string(standIn))
				s.standsFor[value.RatString()] = string(v)
			}
			chosen[key] = standIn
			return standIn
		case []any:
			out := make([]any, len(v))
			for i, item := range v {
				out[i] = substituteAny(item)
			}
			return out
		case map[string]any:
			out := make(map[string]any, len(v))
			for name, member := range v {
				out[name] = substituteAny(member)
			}
			return out
		}
		return v
	}
	substituteAny = func(v any) any {
		held, isContainer := containerOf(v)
		if copied, done := copies[held]; isContainer && done {
			return copied
		}
		out := substituteOne(v)
		if isContainer {
			copies[held] = out
		}
		return out
	}
	s.Value = substituteAny(v)
	return s
}

// forEachNumber calls fn with each number in v.
func forEachNumber(v any, fn func(json.Number)) {
	visitNumbers(v, fn, map[container]bool{})
}

// visitNumbers is forEachNumber, with visited recording the containers
// walked, so a value sharing its parts is walked through each once.
func visitNumbers(v any, fn func(json.Number), visited map[container]bool) {
	if held, isContainer := containerOf(v); isContainer {
		if visited[held] {
			return
		}
		visited[held] = true
	}
	switch v := v.(type) {
	case json.Number:
		fn(v)
	case []any:
		for _, item := range v {
			visitNumbers(item, fn, visited)
		}
	case map[string]any:
		for _, member := range v {
			visitNumbers(member, fn, visited)
		}
	}
}

// numberKey returns a spelling of a JSON number's value that another number
// shares exactly when the two are equal, and whether the value is an integer:
// its significant digits and its power of ten. It takes time linear in the
// token, however many digits its exponent has.
func numberKey(token string) (key string, integer bool) {
	sign := ""
	if strings.HasPrefix(token, "-") {
		sign, token = "-", token[1:]
	}
	mantissa, exponent := token, ""
	if i := strings.IndexAny(token, "eE"); i >= 0 {
		mantissa, exponent = token[:i], token[i+1:]
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return "0", true
	}
	significant := strings.TrimRight(digits, "0")
	// The value is significant × 10^(exponent + shift).
	shift := int64(len(digits)-len(significant)) - int64(len(fraction))
	power := addToPower(exponent, shift)
	return sign + significant + "e" + power, !strings.HasPrefix(power, "-")
}

// addToPower returns an exponent, of any number of digits, plus a shift of at
// most a token's length, without leading zeros.
func addToPower(exponent string, shift int64) string {
	const e18 = 1_000_000_000_000_000_000
	negative := strings.HasPrefix(exponent, "-")
	magnitude := strings.TrimLeft(strings.TrimLeft(exponent, "+-"), "0")
	if len(magnitude) <= 18 {
		power, _ := strconv.ParseInt("0"+magnitude, 10, 64)
		if negative {
			power = -power
		}
		return strconv.FormatInt(power+shift, 10)
	}
	// The exponent's magnitude, at least 10^18, exceeds the shift's, so the
	// sum has its sign, and the shift changes only its last 18 digits and
	// what they carry.
	if negative {
		shift = -shift
	}
	head, tail := magnitude[:len(magnitude)-18], magnitude[len(magnitude)-18:]
	low, _ := strconv.ParseInt(tail, 10, 64)
	switch low += shift; {
	case low >= e18:
		low -= e18
		head = carry(head, true)
	case low < 0:
		low += e18
		head = carry(head, false)
	}
	sum := strings.TrimLeft(head+fmt.Sprintf("%018d", low), "0")
	if negative {
		return "-" + sum
	}
	return sum
}

// carry adds one to a string of decimal digits, or subtracts one from it when
// it is not zero.
func carry(digits string, add bool) string {
	b := []byte(digits)
	for i := len(b) - 1; i >= 0; i-- {
		switch {
		case add && b[i] < '9':
			b[i]++
			return string(b)
		case !add && b[i] > '0':
			b[i]--
			return string(b)
		case add:
			b[i] = '0'
		default:
			b[i] = '9'
		}
	}
	return "1" + string(b)
}

// container identifies a map or slice of a JSON value, which a value may
// share between its parts.
type container struct {
	at     uintptr
	length int
	kind   reflect.Kind
}

func containerOf(v any) (container, bool) {
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Map:
		return container{value.Pointer(), -1, reflect.Map}, value.Len() > 0
	case reflect.Slice:
		return container{value.Pointer(), value.Len(), reflect.Slice}, value.Len() > 0
	}
	return container{}, false
}

// pointerOf writes reference tokens as an RFC 6901 JSON Pointer.
func pointerOf(tokens ...string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(token))
	}
	return b.String()
}
