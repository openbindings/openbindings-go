package schemacompiler

import (
	"errors"
	"testing"
)

// The patterns a JSON Schema reads with the u flag (JSON Schema Core §6.4),
// judged as ECMA-262 does, early errors included.
func TestCheckUnicodePattern(t *testing.T) {
	for _, pattern := range []string{
		`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`, `^[^#]*#?$`, `^(?=x)`, `(?<=a)b`, `(?!a)`, `\u{1F600}`, `😀`,
		`(?<year>\d{4})-\k<year>`, `[\-]`, `[\b]`, `a{2,3}?`, `a{2,}`, `a{2}`, `\/`, `\.`, `(a)\1`, `^\s*$`,
		`[^]`, `[]`, `\0`, `\cA`, `\x41`, `[A-Z]`, `[a-z\d]`, `(?:a|b)+`, `$`, ``, `\$`, `[\]]`,
		`(?<\u0061>x)\k<a>`, `(?<\u{61}>x)\k<a>`, `\uD83D\uDE00`, `(?<=(a)\1)b`, `(?!(a)b)a\1`, `(a)*`,
	} {
		if err := checkUnicodePattern(pattern); err != nil {
			t.Errorf("%q: %v", pattern, err)
		}
	}
	for _, pattern := range []string{
		`[\w-.]`, `[a-\d]`, `\-`, `\q`, `a{`, `{`, `}`, `]`, `\1`, `(a)\2`, `(?<a>x)(?<a>y)`, `\k<b>`, `(?<a>x)\k<b>`,
		`a{3,1}`, `(?=x)*`, `(?<=x)+`, `^*`, `\b+`, `\c1`, `\x4`, `\u12`, `\u{110000}`, `\u{}`, `[z-a]`, `\8`,
		`(`, `)`, `\00`, `[\1]`, `(?<>x)`, `(?<1a>x)`, `(?x)`, `*`, `a**`, `[`, `\`,
	} {
		if err := checkUnicodePattern(pattern); err == nil {
			t.Errorf("%q: want an error", pattern)
		}
	}
	for _, pattern := range []string{`\p{L}`, `[\P{L}]`} {
		if err := checkUnicodePattern(pattern); !errors.Is(err, errPropertyEscape) {
			t.Errorf("%q: want the property escape left unevaluated, got %v", pattern, err)
		}
	}
	for _, pattern := range []string{`^(a|(b))*\2$`, `^(?:(a)|b)*\1$`, `^(?<n>a|(?<m>b))*\k<m>$`, `(?:\1(a))+`, `((a))?\2`} {
		if err := checkUnicodePattern(pattern); !errors.Is(err, errResetCapture) {
			t.Errorf("%q: want the backreference left unevaluated, got %v", pattern, err)
		}
	}
}

// Matching follows ECMA-262 with the u flag where Go's regexp does not.
func TestCompilePatternMatchesAsECMA262(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		match      bool
	}{
		{`^.$`, "😀", true},
		{`^.$`, "\r", false},
		{`^.$`, "\u2028", false},
		{`^\s$`, "\u00a0", true},
		{`^\s$`, "\ufeff", true},
		{`^\w$`, "é", false},
		{`^\d$`, "٣", false},
		{`^(?=x)`, "x", true},
		{`^\u{1F600}$`, "😀", true},
		{`^[a-z]+$`, "abc\n", false},
		{`^.$`, "\u2029", false},
		{`^[.]$`, "x", false},
		{`a\b`, "aé", true},
		{`a\B`, "aé", false},
		{`^[\b]$`, "\b", true},
		{`\.`, "x", false},
		{`b?[^a]`, "b", true},
		{`[ab]*[^a]+?`, "aab", true},
		{`^\uD83D\uDE00$`, "😀", true},
		{`^[\uD83D\uDE00-\uD83D\uDE02]$`, "😁", true},
		{`^\uD83D$`, "\U0001F600", false},
		{`(?<=\1(a))b`, "aab", true},
		{`^(?:(a)|b)\1$`, "b", true},
		{`^(?=(a))a\1$`, "a", false},
	} {
		re, err := CompilePattern(tc.pattern)
		if err != nil {
			t.Fatalf("%q: %v", tc.pattern, err)
		}
		if got, _ := re.MatchString(tc.s); got != tc.match {
			t.Errorf("%q against %q: %v, want %v", tc.pattern, tc.s, got, tc.match)
		}
	}
}
