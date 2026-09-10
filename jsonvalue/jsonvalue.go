// Package jsonvalue supplies protocol-neutral JSON decoding for generic values.
// It uses encoding/json, retaining json.Number instead of reducing numbers to
// float64. Typed destinations and their custom UnmarshalJSON methods still own
// their representations. Boundary-specific duplicate/Unicode policies are not
// imposed here.
package jsonvalue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	codec "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
)

// Unmarshal decodes exactly one JSON value. Numbers decoded into any retain
// their JSON token; callers explicitly select Int64/Float64 when they need a
// native numeric conversion (those methods have encoding/json semantics).
func Unmarshal(data []byte, target any) error {
	d := codec.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("JSON contains more than one value")
	}
	return nil
}

// IsNumber checks the complete token, including rejecting json.Number("").
func IsNumber(n json.Number) bool {
	s := string(n)
	return len(s) > 0 && (s[0] == '-' || s[0] >= '0' && s[0] <= '9') && json.Valid([]byte(s)) && bytes.Equal(bytes.TrimSpace([]byte(s)), []byte(s))
}
