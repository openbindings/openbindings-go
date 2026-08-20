package synthesize

import (
	"regexp"
	"testing"
)

// identPattern mirrors the core package's unexported OBI-D-03 identifier
// pattern (validate.go) for this package's key-derivation assertions.
var identPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func TestSanitizeKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"getUser", "getUser"},
		{"get user", "get_user"},
		{"GET /users/{id}", "GET__users__id"},
		{"_private", "private"},
		{"", "unnamed"},
		{"***", "unnamed"},
		// Key-derivation conservatism (not an OBI-D-03 requirement — the
		// grammar permits a leading digit): digit-leading sanitized keys are
		// still prefixed with an underscore.
		{"2fa.enable", "_2fa.enable"},
		{"42", "_42"},
		{"123 go", "_123_go"},
		// '.' and '-' survive sanitization but cannot lead either.
		{".hidden", "_.hidden"},
		{"-flag", "_-flag"},
		// Cross-SDK pin: an astral-plane character replaces as ONE
		// underscore (regexp operates on runes). The TS SDK matches via a
		// Unicode-mode character class — without the u flag it would emit
		// one underscore per UTF-16 surrogate half ("t-__-a").
		{"t-😀-a", "t-_-a"},
	}
	for _, tc := range cases {
		if got := SanitizeKey(tc.in); got != tc.want {
			t.Errorf("SanitizeKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeKey_ResultMatchesIdentPattern(t *testing.T) {
	for _, in := range []string{"2fa", "9", ".x", "-x", "a b", "GET /v1", "ünïcode", "", "_-_"} {
		got := SanitizeKey(in)
		if !identPattern.MatchString(got) {
			t.Errorf("SanitizeKey(%q) = %q does not match OBI-D-03 identifier pattern", in, got)
		}
	}
}
