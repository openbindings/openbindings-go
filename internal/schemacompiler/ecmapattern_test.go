package schemacompiler

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The patterns a JSON Schema reads with the u flag (JSON Schema Core §6.4),
// judged as ECMA-262 does, early errors included.
func TestCheckPattern(t *testing.T) {
	for _, pattern := range []string{
		`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`, `^[^#]*#?$`, `^(?=x)`, `(?<=a)b`, `(?!a)`, `\u{1F600}`, `😀`,
		`(?<year>\d{4})-\k<year>`, `[\-]`, `[\b]`, `a{2,3}?`, `a{2,}`, `a{2}`, `\/`, `\.`, `(a)\1`, `^\s*$`,
		`[^]`, `[]`, `\0`, `\cA`, `\x41`, `[A-Z]`, `[a-z\d]`, `(?:a|b)+`, `$`, ``, `\$`, `[\]]`,
		`(?<\u0061>x)\k<a>`, `(?<\u{61}>x)\k<a>`, `\uD83D\uDE00`, `(?<=(a)\1)b`, `(?!(a)b)a\1`, `(a)*`,
	} {
		if err := CheckPattern(pattern); err != nil {
			t.Errorf("%q: %v", pattern, err)
		}
	}
	for _, pattern := range []string{
		`[\w-.]`, `[a-\d]`, `\-`, `\q`, `a{`, `{`, `}`, `]`, `\1`, `(a)\2`, `(?<a>x)(?<a>y)`, `\k<b>`, `(?<a>x)\k<b>`,
		`a{3,1}`, `(?=x)*`, `(?<=x)+`, `^*`, `\b+`, `\c1`, `\x4`, `\u12`, `\u{110000}`, `\u{}`, `[z-a]`, `\8`,
		`(`, `)`, `\00`, `[\1]`, `(?<>x)`, `(?<1a>x)`, `(?x)`, `*`, `a**`, `[`, `\`,
	} {
		if err := CheckPattern(pattern); err == nil {
			t.Errorf("%q: want an error", pattern)
		}
	}
	// A backreference to a group a quantified atom holds is valid; how an
	// engine keeps its capture is the evaluator's.
	for _, pattern := range []string{`^(a|(b))*\2$`, `^(?:(a)|b)*\1$`, `^(?<n>a|(?<m>b))*\k<m>$`, `(?:\1(a))+`, `((a))?\2`, `(a)(b)*\1`} {
		if err := CheckPattern(pattern); err != nil {
			t.Errorf("%q: %v", pattern, err)
		}
	}
}

// A Unicode property escape is valid when its names are ECMA-262 11th
// edition's for Unicode 13.0; which characters it matches is the evaluator's.
func TestCheckPattern_PropertyEscapes(t *testing.T) {
	for _, pattern := range []string{
		`\p{L}`, `[\P{L}]`, `\p{Letter}`, `\p{gc=Lu}`, `\p{General_Category=Uppercase_Letter}`,
		`\p{Script=Greek}`, `\p{sc=Grek}`, `\p{scx=Hira}`, `\p{Script_Extensions=Latin}`,
		`\p{ASCII}`, `\p{Any}`, `\p{Assigned}`, `\p{Alphabetic}`, `\p{Alpha}`, `\p{White_Space}`,
		`\p{Emoji}`, `\p{Extended_Pictographic}`, `\p{Script=Yezidi}`, `\p{digit}`, `\p{punct}`,
	} {
		if err := CheckPattern(pattern); err != nil {
			t.Errorf("%q: %v", pattern, err)
		}
	}
	for _, pattern := range []string{
		`\p`, `\p{`, `\p{}`, `\pL`, `\p{l}`, `\p{letter}`, `\p{Greek}`, `\p{Script=Unknown_Script}`,
		`\p{Alphabetic=Y}`, `\p{gc=Greek}`, `\p{Block=Basic_Latin}`, `\p{Script=Vith}`, `[\p{L}-z]`,
	} {
		if err := CheckPattern(pattern); err == nil {
			t.Errorf("%q: want an error", pattern)
		}
	}
}

// Checking a pattern takes time in proportion to its length, and nesting is
// bounded, however many quantified groups hold one another.
func TestCheckPattern_Limits(t *testing.T) {
	nested := func(n int) string { return strings.Repeat("(", n) + "a" + strings.Repeat(")*", n) }
	if err := CheckPattern(nested(maxPatternNesting)); err != nil {
		t.Fatalf("nesting at the limit: %v", err)
	}
	if err := CheckPattern(nested(maxPatternNesting + 1)); !errors.Is(err, ErrPatternNesting) {
		t.Fatalf("nesting past the limit: %v", err)
	}
	// Numbers compare as digit strings, whatever their length.
	for pattern, valid := range map[string]bool{
		`a{9,10}`: true, `a{10,9}`: false, `a{0010,9}`: false, `a{0009,0010}`: true,
		`\u{0000041}`: true, `\u{10FFFF}`: true, `\u{110000}`: false, `\u{0000000110000}`: false,
		`(a)\1`: true, `(a)\0001`: false, `(a)\100000000000000000000000000`: false,
		"a{" + strings.Repeat("9", 200000) + "}": true,
	} {
		if err := CheckPattern(pattern); (err == nil) != valid {
			t.Errorf("%.40q: %v, want valid %v", pattern, err, valid)
		}
	}
	// Many quantified groups beside one another.
	wide := strings.Repeat("(a)*", 200000) + `\1`
	start := time.Now()
	if err := CheckPattern(wide); err != nil {
		t.Fatalf("a wide pattern: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("checking an 800,000-character pattern took %v", elapsed)
	}
}
