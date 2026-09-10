package jsonvalue

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jstring"
	codec "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
)

// CapabilityError is inability to perform an exact operation, not inequality
// or a JSON Schema validation failure. Carriage has no expansion requirement.
type CapabilityError struct{ Operation string }

func (e *CapabilityError) Error() string {
	return "exact JSON " + e.Operation + " exceeds its supported work limit"
}

// NumberToken obtains the JSON representation selected by the caller's Go
// numeric type. It cannot reconstruct precision lost before this boundary.
func NumberToken(v any) (string, bool, error) {
	if n, ok := v.(json.Number); ok {
		if !IsNumber(n) {
			return "", false, fmt.Errorf("invalid JSON number token")
		}
		return string(n), true, nil
	}
	if v == nil {
		return "", false, nil
	}
	switch reflect.TypeOf(v).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		b, err := json.Marshal(v)
		return string(b), true, err
	}
	return "", false, nil
}

// CheckNumericWork checks the numeric-predicate budget without expanding a
// mantissa or exponent. Parsers and ordinary carriage do not call this gate.
func CheckNumericWork(v any) error {
	t, ok, err := NumberToken(v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("expected JSON number")
	}
	if len(t) > 4096 {
		return &CapabilityError{"numeric predicate"}
	}
	if i := strings.LastIndexAny(t, "eE"); i >= 0 {
		exponent, ok := new(big.Int).SetString(t[i+1:], 10)
		if !ok {
			return fmt.Errorf("invalid JSON exponent")
		}
		if exponent.Cmp(big.NewInt(-10000)) < 0 || exponent.Cmp(big.NewInt(10000)) > 0 {
			return &CapabilityError{"numeric predicate"}
		}
	}
	return nil
}

func rational(v any) (*big.Rat, error) {
	if err := CheckNumericWork(v); err != nil {
		return nil, err
	}
	t, _, _ := NumberToken(v)
	r, ok := new(big.Rat).SetString(t)
	if !ok {
		return nil, fmt.Errorf("invalid JSON number")
	}
	return r, nil
}

func CompareNumbers(a, b any) (int, error) {
	// Identical admitted tokens need no rational allocation; budget checks
	// still precede this shortcut, so it cannot turn refusal into equality.
	at, an, ae := NumberToken(a)
	bt, bn, be := NumberToken(b)
	if ae == nil && be == nil && an && bn && at == bt {
		return 0, CheckNumericWork(a)
	}
	x, err := rational(a)
	if err != nil {
		return 0, err
	}
	y, err := rational(b)
	if err != nil {
		return 0, err
	}
	return x.Cmp(y), nil
}

// Equal compares JSON values, not canonical bytes. Native/typed host values
// select their existing encoding/json representation before comparison.
func Equal(a, b any) (bool, error) {
	x, err := detached(a)
	if err != nil {
		return false, err
	}
	y, err := detached(b)
	if err != nil {
		return false, err
	}
	return equal(x, y)
}

func detached(v any) (any, error) {
	// Scalar JSON values are already detached. Keep the same encoding/json
	// representation for native numbers without allocating a stream decoder.
	switch x := v.(type) {
	case nil, bool:
		return v, nil
	case string:
		if utf8.ValidString(x) || jstring.Valid(x) {
			return x, nil
		}
		// Keep encoding/json's replacement behavior at native Go string
		// boundaries, including one replacement per invalid UTF-8 sequence.
		return string([]rune(x)), nil
	case json.Number:
		if !IsNumber(x) {
			return nil, fmt.Errorf("invalid JSON number token")
		}
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("nonfinite JSON number")
		}
		// The standard shortest-decimal formatter selects the same numeric
		// value as encoding/json. Exponent spelling is not value identity.
		return json.Number(strconv.FormatFloat(x, 'g', -1, 64)), nil
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, fmt.Errorf("nonfinite JSON number")
		}
		return json.Number(strconv.FormatFloat(float64(x), 'g', -1, 32)), nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return json.Number(b), nil
	}
	data, err := Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	err = Unmarshal(data, &out)
	return out, err
}

// Marshal retains encoding/json's representation choices and rejects malformed
// json.Number carriers, including its otherwise special-cased empty token.
func Marshal(value any) ([]byte, error) {
	if err := checkNumberCarriers(reflect.ValueOf(value), make(map[reflect.Value]bool)); err != nil {
		return nil, err
	}
	return codec.Marshal(value)
}

func checkNumberCarriers(v reflect.Value, seen map[reflect.Value]bool) error {
	if !v.IsValid() {
		return nil
	}
	if v.CanInterface() {
		if n, ok := v.Interface().(json.Number); ok {
			if !IsNumber(n) {
				return fmt.Errorf("invalid JSON number token")
			}
			return nil
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() || seen[v] {
			return nil
		}
		seen[v] = true
		return checkNumberCarriers(v.Elem(), seen)
	case reflect.Map:
		if v.IsNil() || seen[v] {
			return nil
		}
		seen[v] = true
		iter := v.MapRange()
		for iter.Next() {
			if err := checkNumberCarriers(iter.Value(), seen); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if seen[v] {
			return nil
		}
		seen[v] = true
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		} // encoded bytes/RawMessage have their own parser
		for i := 0; i < v.Len(); i++ {
			if err := checkNumberCarriers(v.Index(i), seen); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			if err := checkNumberCarriers(v.Field(i), seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func equal(a, b any) (bool, error) {
	if an, ok := a.(json.Number); ok {
		bn, ok := b.(json.Number)
		if !ok {
			return false, nil
		}
		if an == bn {
			return true, CheckNumericWork(an)
		}
		c, err := CompareNumbers(an, bn)
		return c == 0 && err == nil, err
	}
	switch x := a.(type) {
	case nil:
		return b == nil, nil
	case string:
		y, ok := b.(string)
		return ok && x == y, nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y, nil
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false, nil
		}
		for i, v := range x {
			same, err := equal(v, y[i])
			if err != nil || !same {
				return same, err
			}
		}
		return true, nil
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false, nil
		}
		for k, v := range x {
			w, present := y[k]
			if !present {
				return false, nil
			}
			same, err := equal(v, w)
			if err != nil || !same {
				return same, err
			}
		}
		return true, nil
	}
	return false, fmt.Errorf("unsupported JSON value %T", a)
}
