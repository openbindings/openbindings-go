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
		if refusal := cannotReadExactly(err); refusal != nil {
			return nil, refusal
		}
		return nil, fmt.Errorf("the input is not JSON: %w", err)
	}
	var value any
	if err := unmarshalJSON(data, &value); err != nil {
		return nil, fmt.Errorf("the input is not JSON: %w", err)
	}
	return value, nil
}

// cannotReadExactly returns core's refusal of JSON text verifyExactJSON
// refused with err, when the text is JSON this SDK cannot read exactly (a
// repeated member name, an escaped lone UTF-16 surrogate, nesting past the
// decoder), and nil when it is not JSON at all.
func cannotReadExactly(err error) *NoVerdictError {
	var repeated *duplicateNameError
	var lone *loneSurrogateError
	if errors.As(err, &repeated) || errors.As(err, &lone) || errors.Is(err, errNestingLimit) {
		return &NoVerdictError{Cause: fmt.Errorf("the value cannot be read exactly: %w", err)}
	}
	return nil
}

// readGoValue reads a Go value as encoding/json encodes it, exactly: floats
// as their shortest round-trip decimal, typed nil slices and maps as null,
// structs by exported fields, and a json.RawMessage as written. A
// json.RawMessage, whether it is the value or one the value holds, is read as
// readJSONText reads JSON text, so it gets what ValidateJSON gives the same
// text: core's refusal to read it exactly (a repeated member name, an escaped
// lone UTF-16 surrogate, nesting past the decoder), as a *NoVerdictError.
// It refuses, with an error saying the value is not a JSON value, which its
// caller frames, what encoding/json cannot encode (a NaN, a channel, a cycle,
// a marshaler's error) and invalid UTF-8, which encoding/json would replace;
// and a top-level byte slice of any named type that encoding/json writes as
// base64 (one that marshals itself, such as json.RawMessage, is read as it
// marshals), which is more likely JSON text meant for ValidateJSON.
func readGoValue(value any) (any, error) {
	switch raw := value.(type) {
	case json.RawMessage:
		if raw == nil {
			return readJSONText([]byte("null")) // as encoding/json writes it
		}
		return readJSONText(raw)
	case *json.RawMessage:
		if raw == nil || *raw == nil {
			return readJSONText([]byte("null"))
		}
		return readJSONText(*raw)
	}
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
	// Raw JSON the value holds is read exactly as JSON text is, before
	// encoding/json compacts it and refuses, as a syntax error of its own,
	// what is only too deep for this SDK to read.
	err := rawLeaves(reflect.ValueOf(value), true, nil, map[heldValue]bool{}, func(_ []string, raw json.RawMessage) error {
		if err := verifyExactJSON(raw); err != nil {
			if refusal := cannotReadExactly(err); refusal != nil {
				return refusal
			}
		}
		return nil // text that is not JSON is encoding/json's to refuse
	})
	if err != nil {
		return nil, err
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
