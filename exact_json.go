package openbindings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// errNestingLimit is wrapped by the error for input nested deeper than
// encoding/json reads (10000 levels). Such input is not shown to break
// OBI-D-01; it meets a resource limit, which is no evidence of a violation
// (§10.5).
var errNestingLimit = errors.New("nested deeper than the decoder reads (10000 levels)")

// loneSurrogateError reports a string escape of an isolated UTF-16 surrogate
// (a lone \uD800, say). RFC 8259 admits it, so it breaks no document rule,
// but a Go string cannot hold it and encoding/json would replace it with
// U+FFFD, altering the document; the document model does not carry it
// (§10.5: a capability this SDK lacks).
type loneSurrogateError struct {
	// location is the JSON Pointer of the string value holding it, or of the
	// object whose member name holds it.
	location string
	name     bool
}

func (e *loneSurrogateError) Error() string {
	where := "a string at " + strconv.Quote(e.location)
	if e.name {
		where = "a member name of the object at " + strconv.Quote(e.location)
	}
	return where + " holds an escape of a lone UTF-16 surrogate, which this SDK does not carry"
}

// verifyExactJSON checks what decoding JSON into Go values would otherwise
// lose without error: that the input is one valid UTF-8 JSON value, and that
// no object in it repeats a member name, which OBI-D-01 requires; and that no
// string escapes a lone UTF-16 surrogate, which the document model cannot
// carry. encoding/json replaces invalid UTF-8 and lone surrogates and keeps
// only the last of repeated names. A duplicate name is reported before a lone
// surrogate: the one breaks OBI-D-01, the other only exceeds the model.
func verifyExactJSON(b []byte) error {
	if !utf8.Valid(b) {
		return errors.New("not valid UTF-8")
	}
	if !json.Valid(b) {
		var discard any
		if err := json.Unmarshal(b, &discard); err != nil {
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) && strings.Contains(syntax.Error(), "exceeded max depth") {
				return errNestingLimit
			}
			return err // the decoder's error locates the syntax error
		}
		return errors.New("not valid JSON")
	}
	var scan exactScan
	if _, err := scan.value(b, skipJSONSpace(b, 0), nil); err != nil {
		return err
	}
	if scan.lone != nil {
		return scan.lone
	}
	return nil
}

// exactScan walks valid JSON for repeated member names and lone surrogates.
type exactScan struct {
	lone *loneSurrogateError // the first lone surrogate, in document order
}

// value walks the JSON value starting at b[i], at the given reference
// tokens, and returns the index just past it, or an error at the first
// object that repeats a member name.
func (s *exactScan) value(b []byte, i int, tokens []string) (int, error) {
	switch b[i] {
	case '{':
		var seen map[string]struct{}
		i = skipJSONSpace(b, i+1)
		for b[i] != '}' {
			nameEnd := jsonValueEnd(b, i)
			name, lone := exactString(b[i:nameEnd])
			if lone && s.lone == nil {
				s.lone = &loneSurrogateError{location: jsonpointer.Format(tokens...), name: true}
			}
			if _, repeated := seen[name]; repeated {
				return 0, fmt.Errorf("duplicate object key %q", name)
			}
			if seen == nil {
				seen = map[string]struct{}{}
			}
			seen[name] = struct{}{}
			end, err := s.value(b, skipJSONSpace(b, skipJSONSpace(b, nameEnd)+1), append(tokens[:len(tokens):len(tokens)], name))
			if err != nil {
				return 0, err
			}
			i = skipJSONSpace(b, end)
			if b[i] == ',' {
				i = skipJSONSpace(b, i+1)
			}
		}
		return i + 1, nil
	case '[':
		i = skipJSONSpace(b, i+1)
		for index := 0; b[i] != ']'; index++ {
			end, err := s.value(b, i, append(tokens[:len(tokens):len(tokens)], strconv.Itoa(index)))
			if err != nil {
				return 0, err
			}
			i = skipJSONSpace(b, end)
			if b[i] == ',' {
				i = skipJSONSpace(b, i+1)
			}
		}
		return i + 1, nil
	case '"':
		end := jsonValueEnd(b, i)
		if _, lone := exactString(b[i:end]); lone && s.lone == nil {
			s.lone = &loneSurrogateError{location: jsonpointer.Format(tokens...)}
		}
		return end, nil
	default:
		return jsonValueEnd(b, i), nil
	}
}

// exactString decodes a JSON string token of valid JSON exactly: an escape of
// a lone UTF-16 surrogate becomes that code unit's three-byte encoding, so
// "\uD800" and "\uFFFD" stay distinct, and lone reports whether the token
// held one.
func exactString(token []byte) (value string, lone bool) {
	body := token[1 : len(token)-1]
	if bytes.IndexByte(body, '\\') < 0 {
		return string(body), false
	}
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			out = append(out, body[i])
			continue
		}
		i++
		switch body[i] {
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			unit := hexUnit(body[i+1 : i+5])
			i += 4
			if utf16.IsSurrogate(rune(unit)) {
				if unit < 0xdc00 && i+6 < len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
					if low := hexUnit(body[i+3 : i+7]); low >= 0xdc00 && low <= 0xdfff {
						out = utf8.AppendRune(out, utf16.DecodeRune(rune(unit), rune(low)))
						i += 6
						continue
					}
				}
				lone = true
				out = append(out, 0xe0|byte(unit>>12), 0x80|byte(unit>>6)&0x3f, 0x80|byte(unit)&0x3f)
				continue
			}
			out = utf8.AppendRune(out, rune(unit))
		default: // " \ /
			out = append(out, body[i])
		}
	}
	return string(out), lone
}

// hexUnit reads four hexadecimal digits of valid JSON.
func hexUnit(digits []byte) uint16 {
	unit, _ := strconv.ParseUint(string(digits), 16, 16)
	return uint16(unit)
}

// unmarshalJSON decodes exactly one JSON value into target, keeping numbers
// as json.Number, in input verifyExactJSON has accepted.
func unmarshalJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("JSON holds more than one value")
	}
	return nil
}

// jsonStringValue decodes a JSON string token of input verifyExactJSON has
// accepted, which holds no lone surrogate.
func jsonStringValue(token []byte) (string, error) {
	value, _ := exactString(token)
	return value, nil
}

type objectEntry struct {
	name  string
	value json.RawMessage // aliases the input
}

// splitObject splits a JSON object, in input verifyExactJSON has accepted,
// into its entries in document order. Entry values alias the input.
func splitObject(b []byte) ([]objectEntry, error) {
	i := skipJSONSpace(b, 0)
	if i == len(b) || b[i] != '{' {
		if isJSONNull(b) {
			return nil, errNullJSONObject
		}
		return nil, errNotJSONObject
	}
	var entries []objectEntry
	i = skipJSONSpace(b, i+1)
	for b[i] != '}' {
		nameEnd := jsonValueEnd(b, i)
		name, err := jsonStringValue(b[i:nameEnd])
		if err != nil {
			return nil, err
		}
		start := skipJSONSpace(b, skipJSONSpace(b, nameEnd)+1) // past the colon
		end := jsonValueEnd(b, start)
		entries = append(entries, objectEntry{name: name, value: b[start:end]})
		i = skipJSONSpace(b, end)
		if b[i] == ',' {
			i = skipJSONSpace(b, i+1)
		}
	}
	return entries, nil
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// jsonValueEnd returns the index just past the JSON value starting at b[i],
// in valid JSON.
func jsonValueEnd(b []byte, i int) int {
	switch b[i] {
	case '"':
		for i++; b[i] != '"'; i++ {
			if b[i] == '\\' {
				i++
			}
		}
		return i + 1
	case '{', '[':
		depth := 0
		for ; i < len(b); i++ {
			switch b[i] {
			case '"':
				i = jsonValueEnd(b, i) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return i
	default:
		for i < len(b) && !strings.ContainsRune(",}] \t\n\r", rune(b[i])) {
			i++
		}
		return i
	}
}

func isJSONNull(b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(b), []byte("null"))
}
