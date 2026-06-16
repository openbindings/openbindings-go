package openbindings

import "testing"

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
		// OBI-D-03 requires ^[A-Za-z_]: digit-leading sanitized keys are
		// prefixed with an underscore.
		{"2fa.enable", "_2fa.enable"},
		{"42", "_42"},
		{"123 go", "_123_go"},
		// '.' and '-' survive sanitization but cannot lead either.
		{".hidden", "_.hidden"},
		{"-flag", "_-flag"},
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
