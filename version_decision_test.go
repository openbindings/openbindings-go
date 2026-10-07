package openbindings

import (
	"errors"
	"testing"
	"unicode/utf16"
)

// A text declares a version exactly when it has no byte-order mark and, read
// with each byte outside a well-formed UTF-8 sequence replaced, parses as JSON
// with one openbindings member holding a SemVer string (§8.1). A byte in
// another member's string changes nothing; one in the version, in the
// member's name, within an escape, or between tokens leaves the text declaring
// none, and no version is refused.
func TestVersionDecision_ReadsTheTextAsTheVersionDeclarationDecodes(t *testing.T) {
	utf16le := func(s string) []byte {
		var out []byte
		for _, unit := range utf16.Encode([]rune(s)) {
			out = append(out, byte(unit), byte(unit>>8))
		}
		return out
	}
	for name, tc := range map[string]struct {
		input   []byte
		refused bool
	}{
		"an unsupported version":                      {[]byte(`{"openbindings":"0.3.0","operations":{}}`), true},
		"a bad byte in another member's string":       {[]byte("{\"openbindings\":\"0.3.0\",\"description\":\"x\xff\",\"operations\":{}}"), true},
		"a truncated sequence before a closing quote": {[]byte("{\"openbindings\":\"0.3.0\",\"description\":\"x\xc2\",\"operations\":{}}"), true},
		"a repeated name elsewhere":                   {[]byte(`{"openbindings":"0.3.0","operations":{},"operations":{}}`), true},
		"a byte-order mark":                           {append([]byte("\xef\xbb\xbf"), `{"openbindings":"0.3.0","operations":{}}`...), false},
		"UTF-16 without a byte-order mark":            {utf16le(`{"openbindings":"0.3.0","operations":{}}`), false},
		"a bad byte in the version":                   {[]byte("{\"openbindings\":\"0.3.\xff0\",\"operations\":{}}"), false},
		"a bad byte in the member's name":             {[]byte("{\"openbind\xffings\":\"0.3.0\",\"operations\":{}}"), false},
		"a bad byte within an escape":                 {[]byte("{\"openbindings\":\"0.3.0\",\"description\":\"\\u12\xff4\",\"operations\":{}}"), false},
		"a repeated openbindings member":              {[]byte(`{"openbindings":"0.2.0","openbindings":"0.3.0","operations":{}}`), false},
	} {
		_, _, err := ValidateDocument(tc.input)
		_, parseErr := ParseDocument(tc.input)
		for entry, err := range map[string]error{"ValidateDocument": err, "ParseDocument": parseErr} {
			if refused := errors.As(err, new(*VersionRefusalError)); refused != tc.refused {
				t.Errorf("%s: %s refused %v, want %v (%v)", name, entry, refused, tc.refused, err)
			}
		}
	}
}
