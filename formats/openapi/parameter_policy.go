package openapi

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"strconv"
)

// DecimalParameterConversion is an opt-in host policy, not a binding default.
// It renders booleans in lower case and finite binary64 scalars as shortest
// round-trip decimal without exponents. Strings are unchanged; negative zero
// is zero. Integers outside the interoperable +/- (2^53-1) range and values
// whose decimal precision would be lost are refused. Null remains edition-owned.
func DecimalParameterConversion(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case bool:
		return strconv.FormatBool(v), nil
	case json.Number:
		f, err := strconv.ParseFloat(string(v), 64)
		if err != nil {
			return "", fmt.Errorf("parameter is not a finite interoperable number")
		}
		text, err := decimalFloat(f)
		if err != nil {
			return "", err
		}
		before, ok := new(big.Rat).SetString(string(v))
		after, valid := new(big.Rat).SetString(text)
		if !ok || !valid || before.Cmp(after) != 0 {
			return "", fmt.Errorf("parameter conversion would lose decimal precision")
		}
		return text, nil
	}
	if value != nil {
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.Float32, reflect.Float64:
			return decimalFloat(v.Float())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n := v.Int()
			if n >= -9007199254740991 && n <= 9007199254740991 {
				return strconv.FormatInt(n, 10), nil
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			n := v.Uint()
			if n <= 9007199254740991 {
				return strconv.FormatUint(n, 10), nil
			}
		}
	}
	return "", fmt.Errorf("parameter requires a string, boolean, or interoperable finite number")
}

func decimalFloat(value float64) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 9007199254740991 {
		return "", fmt.Errorf("parameter is outside the interoperable finite number range")
	}
	if value == 0 {
		return "0", nil
	}
	return strconv.FormatFloat(value, 'f', -1, 64), nil
}
