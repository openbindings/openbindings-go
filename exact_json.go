package openbindings

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
)

// verifyExactJSON checks what decoding JSON into Go values would otherwise
// lose without error: that the input is one valid UTF-8 JSON value, and that
// no object in it repeats a member name. The codec replaces invalid UTF-8 and
// keeps only the last of repeated names. Both checks are OBI-D-01's, and the
// document model refuses input that fails them.
func verifyExactJSON(b []byte) error {
	if !utf8.Valid(b) {
		return errors.New("not valid UTF-8")
	}
	if !json.Valid(b) {
		var discard any
		if err := json.Unmarshal(b, &discard); err != nil {
			return err // the codec's error locates the syntax error
		}
		return errors.New("not valid JSON")
	}
	_, err := scanDuplicateNames(b, skipJSONSpace(b, 0))
	return err
}

// scanDuplicateNames walks the JSON value starting at b[i], in valid JSON,
// and returns the index just past it, or an error at the first object that
// repeats a member name.
func scanDuplicateNames(b []byte, i int) (int, error) {
	switch b[i] {
	case '{':
		var seen map[string]struct{}
		i = skipJSONSpace(b, i+1)
		for b[i] != '}' {
			nameEnd := jsonValueEnd(b, i)
			name, err := jsonStringValue(b[i:nameEnd])
			if err != nil {
				return 0, err
			}
			if _, repeated := seen[name]; repeated {
				return 0, fmt.Errorf("duplicate object key %q", name)
			}
			if seen == nil {
				seen = map[string]struct{}{}
			}
			seen[name] = struct{}{}
			end, err := scanDuplicateNames(b, skipJSONSpace(b, skipJSONSpace(b, nameEnd)+1))
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
		for b[i] != ']' {
			end, err := scanDuplicateNames(b, i)
			if err != nil {
				return 0, err
			}
			i = skipJSONSpace(b, end)
			if b[i] == ',' {
				i = skipJSONSpace(b, i+1)
			}
		}
		return i + 1, nil
	default:
		return jsonValueEnd(b, i), nil
	}
}

// jsonStringValue decodes a JSON string token.
func jsonStringValue(token []byte) (string, error) {
	if bytes.IndexByte(token, '\\') < 0 {
		return string(token[1 : len(token)-1]), nil
	}
	var value string
	err := json.Unmarshal(token, &value)
	return value, err
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
