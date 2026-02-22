package canonicaljson

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// This package intentionally implements RFC 8785 (JCS) in a small, dependency-free way.
//
// Rationale:
// - OpenBindings tooling benefits from deterministic JSON bytes (diffs, hashing, golden tests).
// - We want canonicalization to be stable across languages and implementations, so we target a
//   published standard (RFC 8785) rather than an ad-hoc "stable key order" scheme.
//
// Future:
// Go has an experimental `encoding/json/v2` + `jsontext` stack (enabled with GOEXPERIMENT=jsonv2)
// that includes deterministic options and related JSON tooling. Once a stable stdlib JCS-capable
// implementation is available without experimental flags, this package is intended to be a small,
// swappable façade so we can switch implementations without changing output bytes.
//
// References:
// - RFC 8785: JSON Canonicalization Scheme (JCS): https://www.rfc-editor.org/rfc/rfc8785
// - Go `encoding/json/v2` (experimental): https://pkg.go.dev/encoding/json/v2

// Marshal returns a deterministic JSON encoding of the input according to RFC 8785 (JCS).
//
// Notes:
// - Objects are sorted by member names using UTF-16 code unit lexicographic order.
// - Arrays preserve order.
// - Strings are serialized using JSON string syntax per RFC 8785 §3.2.2.2: \b, \t, \n, \f, \r use
//   shorthand escapes; remaining control characters use \u00XX (lowercase hex).
// - Numbers are serialized using ECMAScript-compatible number serialization (as required by RFC 8785).
// - Output is compact (no extra whitespace).
func Marshal(v any) ([]byte, error) {
	var b []byte

	switch x := v.(type) {
	case json.RawMessage:
		b = x
	case []byte:
		b = x
	default:
		var err error
		b, err = json.Marshal(v)
		if err != nil {
			return nil, err
		}
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var anyVal any
	if err := dec.Decode(&anyVal); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("invalid JSON: trailing data")
		}
		return nil, err
	}

	var buf bytes.Buffer
	if err := writeJCS(&buf, anyVal); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeJCS(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
		return nil
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil
	case string:
		writeJCSString(buf, x)
		return nil
	case json.Number:
		s, err := formatJCSNumber(x.String())
		if err != nil {
			return err
		}
		buf.WriteString(s)
		return nil
	case float64:
		// If callers bypassed UseNumber, preserve semantics by formatting as JCS number anyway.
		s, err := formatJCSFloat64(x)
		if err != nil {
			return err
		}
		buf.WriteString(s)
		return nil
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeJCS(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case map[string]any:
		type kv struct {
			k   string
			key []uint16
		}
		keys := make([]kv, 0, len(x))
		for k := range x {
			keys = append(keys, kv{k: k, key: utf16.Encode([]rune(k))})
		}
		sort.Slice(keys, func(i, j int) bool {
			return lessUTF16(keys[i].key, keys[j].key)
		})

		buf.WriteByte('{')
		for i, entry := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJCSString(buf, entry.k)
			buf.WriteByte(':')
			if err := writeJCS(buf, x[entry.k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	default:
		return errors.New("unsupported JSON value type")
	}
}

func lessUTF16(a, b []uint16) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] == b[i] {
			continue
		}
		return a[i] < b[i]
	}
	return len(a) < len(b)
}

func writeJCSString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			buf.WriteString(`\\`)
		case r == '"':
			buf.WriteString(`\"`)
		// RFC 8785 §3.2.2.2: these five control characters MUST use shorthand escapes.
		case r == '\b':
			buf.WriteString(`\b`)
		case r == '\t':
			buf.WriteString(`\t`)
		case r == '\n':
			buf.WriteString(`\n`)
		case r == '\f':
			buf.WriteString(`\f`)
		case r == '\r':
			buf.WriteString(`\r`)
		case r <= 0x1F:
			// Remaining control characters use \u00XX with lowercase hex.
			var b [6]byte
			b[0] = '\\'
			b[1] = 'u'
			b[2] = '0'
			b[3] = '0'
			hex.Encode(b[4:], []byte{byte(r)})
			buf.Write(b[:])
		default:
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}

func formatJCSNumber(s string) (string, error) {
	// Parse as float64 per RFC 8785 requirement (IEEE-754 double).
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", err
	}
	return formatJCSFloat64(f)
}

func formatJCSFloat64(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", errors.New("invalid JSON number: NaN or Infinity")
	}
	// Normalize -0 to 0.
	if f == 0 {
		return "0", nil
	}

	abs := math.Abs(f)
	var s string
	if abs >= 1e21 || abs < 1e-6 {
		s = strconv.FormatFloat(f, 'e', -1, 64)
		s = normalizeExponent(s)
	} else {
		// Decimal form without exponent.
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	// Go may emit "-0" in some cases; normalize again.
	if s == "-0" {
		return "0", nil
	}
	return s, nil
}

func normalizeExponent(s string) string {
	// Go's 'e' format uses a zero-padded exponent (e.g., 1e-06). JCS/ECMAScript uses no padding (1e-6).
	i := strings.IndexByte(s, 'e')
	if i < 0 {
		return s
	}
	if i+2 >= len(s) {
		return s
	}
	sign := s[i+1]
	exp := s[i+2:]
	// trim leading zeros, keep at least one digit
	j := 0
	for j < len(exp)-1 && exp[j] == '0' {
		j++
	}
	exp = exp[j:]
	return s[:i+1] + string(sign) + exp
}
