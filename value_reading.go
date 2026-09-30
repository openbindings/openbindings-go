package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// readJSONText reads JSON text exactly into a JSON value: nil, bool, string,
// json.Number, []any, or map[string]any. Input that is not one JSON value of
// valid UTF-8 returns an error saying so; input this SDK cannot read exactly
// (a repeated member name, an escaped lone UTF-16 surrogate, nesting past the
// decoder) returns a *NoVerdictError.
func readJSONText(data []byte) (any, error) {
	if err := verifyExactJSON(data); err != nil {
		var repeated *duplicateNameError
		var lone *loneSurrogateError
		if errors.As(err, &repeated) || errors.As(err, &lone) || errors.Is(err, errNestingLimit) {
			return nil, &NoVerdictError{Cause: fmt.Errorf("the value cannot be read exactly: %w", err)}
		}
		return nil, fmt.Errorf("openbindings: the input is not JSON: %w", err)
	}
	var value any
	if err := unmarshalJSON(data, &value); err != nil {
		return nil, fmt.Errorf("openbindings: the input is not JSON: %w", err)
	}
	return value, nil
}

// readGoValue reads a Go value as encoding/json encodes it, exactly: floats
// as their shortest round-trip decimal, typed nil slices and maps as null,
// structs by exported fields, and a json.RawMessage as written. It refuses,
// with an error saying the value is not a JSON value, what encoding/json
// cannot encode (a NaN, a channel, a cycle) and invalid UTF-8, which
// encoding/json would replace; and a top-level []byte, which encoding/json
// writes as base64 but is more likely JSON text meant for ValidateJSON.
func readGoValue(value any) (any, error) {
	if _, isBytes := value.([]byte); isBytes {
		return nil, errors.New("openbindings: a []byte is not validated as a value; use ValidateJSON for JSON text")
	}
	if below, problem := invalidUTF8(reflect.ValueOf(value), map[heldValue]bool{}); problem != "" {
		slices.Reverse(below)
		where := "the value"
		if len(below) > 0 {
			where = strings.Join(below, "/")
		}
		return nil, fmt.Errorf("openbindings: not a JSON value: %s: %s", where, problem)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("openbindings: not a JSON value: %w", err)
	}
	return readJSONText(data)
}
