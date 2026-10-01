package openbindings

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// readJSONText reads JSON text exactly into a JSON value: nil, bool, string,
// json.Number, []any, or map[string]any. Input that is not one JSON value of
// valid UTF-8 returns an error saying so, which its caller frames; input this
// SDK cannot read exactly (a repeated member name, an escaped lone UTF-16
// surrogate, nesting past the decoder) returns a *NoVerdictError.
func readJSONText(data []byte) (any, error) {
	if err := verifyExactJSON(data); err != nil {
		var repeated *duplicateNameError
		var lone *loneSurrogateError
		if errors.As(err, &repeated) || errors.As(err, &lone) || errors.Is(err, errNestingLimit) {
			return nil, &NoVerdictError{Cause: fmt.Errorf("the value cannot be read exactly: %w", err)}
		}
		return nil, fmt.Errorf("the input is not JSON: %w", err)
	}
	var value any
	if err := unmarshalJSON(data, &value); err != nil {
		return nil, fmt.Errorf("the input is not JSON: %w", err)
	}
	return value, nil
}

// readGoValue reads a Go value as encoding/json encodes it, exactly: floats
// as their shortest round-trip decimal, typed nil slices and maps as null,
// structs by exported fields, and a json.RawMessage as written. It refuses,
// with an error saying the value is not a JSON value, which its caller
// frames, what encoding/json
// cannot encode (a NaN, a channel, a cycle) and invalid UTF-8, which
// encoding/json would replace; and a top-level byte slice of any named type
// that encoding/json writes as base64 (one that marshals itself, such as
// json.RawMessage, is read as it marshals), which is more likely JSON text
// meant for ValidateJSON.
func readGoValue(value any) (any, error) {
	if base64Bytes(value) {
		return nil, fmt.Errorf("a %T is not validated as a value; use ValidateJSON for JSON text", value)
	}
	if below, problem := invalidUTF8(reflect.ValueOf(value), map[heldValue]bool{}); problem != "" {
		slices.Reverse(below)
		where := "the value"
		if len(below) > 0 {
			where = strings.Join(below, "/")
		}
		return nil, fmt.Errorf("not a JSON value: %s: %s", where, problem)
	}
	data, err := json.Marshal(value)
	if err != nil {
		// The error can be a marshaler's own, which may say anything, a
		// sentinel or a *NoVerdictError of this package included. It is kept
		// as text, so the failure matches no category but the one notAValue
		// gives it.
		return nil, fmt.Errorf("not a JSON value: %v", err)
	}
	return readJSONText(data)
}

// base64Bytes reports whether encoding/json writes a value as a base64
// string, as its slice encoder decides: a byte slice, reached through any
// pointers, that neither it nor its element type marshals itself or is
// marshaled as text.
func base64Bytes(value any) bool {
	marshals := func(t reflect.Type) bool {
		return t.Implements(reflect.TypeFor[json.Marshaler]()) || t.Implements(reflect.TypeFor[encoding.TextMarshaler]())
	}
	v := reflect.ValueOf(value)
	for v.IsValid() && !marshals(v.Type()) && v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	switch {
	case !v.IsValid(), marshals(v.Type()), v.CanAddr() && marshals(reflect.PointerTo(v.Type())):
		return false
	case v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Uint8:
		return false
	}
	return !marshals(reflect.PointerTo(v.Type().Elem()))
}

// notAValue frames a failure to read a value: core's own refusal to read it
// exactly, a *NoVerdictError readJSONText returns itself, as it is, and input
// that is not one JSON value as ErrInconclusive, since there is no value to
// judge. The refusal is told apart by its type, not searched for in a chain,
// and the other failures hold no error from outside core, so the result
// matches exactly one of ErrNoVerdict and ErrInconclusive.
func notAValue(err error) error {
	if refusal, isRefusal := err.(*NoVerdictError); isRefusal {
		return refusal
	}
	return fmt.Errorf("%w: %w", ErrInconclusive, err)
}
