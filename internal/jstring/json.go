package jstring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Unquote decodes one validated JSON string without replacing isolated UTF-16
// code units. Structural JSON validation remains the standard decoder's job.
func Unquote(raw []byte) (string, error) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' || !json.Valid(raw) {
		return "", fmt.Errorf("invalid JSON string")
	}
	var out strings.Builder
	for i := 1; i < len(raw)-1; {
		if raw[i] == '\\' {
			if raw[i+1] == 'u' {
				n, _ := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
				unit := uint16(n)
				i += 6
				if unit >= 0xd800 && unit <= 0xdbff && i+6 <= len(raw)-1 && raw[i] == '\\' && raw[i+1] == 'u' {
					low, _ := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
					if low >= 0xdc00 && low <= 0xdfff {
						out.WriteRune(rune(0x10000 + (n-0xd800)*0x400 + low - 0xdc00))
						i += 6
						continue
					}
				}
				out.WriteString(CodeUnit(unit))
				continue
			}
			const escapes = "\"\\/bfnrt"
			const decoded = "\"\\/\b\f\n\r\t"
			out.WriteByte(decoded[strings.IndexByte(escapes, raw[i+1])])
			i += 2
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		out.WriteRune(r) // retain the existing invalid-wire-UTF-8 replacement policy
		i += size
	}
	return out.String(), nil
}

// Quote emits standard JSON. WTF-8 is an internal representation only: isolated
// units are always six-byte ASCII escapes on the wire.
func Quote(s string) ([]byte, error) {
	if !Valid(s) {
		return nil, fmt.Errorf("malformed internal JSON string encoding")
	}
	var out bytes.Buffer
	out.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		if len(s)-i >= 3 {
			if unit := surrogate(s[i : i+3]); unit != 0 {
				part, _ := json.Marshal(s[start:i])
				out.Write(part[1 : len(part)-1])
				fmt.Fprintf(&out, "\\u%04x", unit)
				i += 3
				start = i
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	part, _ := json.Marshal(s[start:])
	out.Write(part[1 : len(part)-1])
	out.WriteByte('"')
	return out.Bytes(), nil
}

// Token delegates JSON grammar to encoding/json and uses its exact input offsets
// to recover only string code units that its native conversion would replace.
func Token(dec *json.Decoder, source []byte) (json.Token, error) {
	start := dec.InputOffset()
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if _, ok := token.(string); !ok {
		return token, nil
	}
	raw := bytes.TrimLeft(source[start:dec.InputOffset()], " \t\r\n,:")
	return Unquote(raw)
}
