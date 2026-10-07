package openbindings

import (
	"errors"
	"slices"
	"testing"
	"unicode/utf16"
)

// A text declares a version exactly when it is UTF-8 with no leading
// byte-order mark and parses under the JSON grammar as an object with exactly
// one openbindings member whose value is a SemVer string (§8.1). Repeated
// names elsewhere, and a string escaping a lone surrogate, which the grammar
// admits, do not stop a declaration. An ill-formed byte anywhere, a byte-order
// mark, a repeated openbindings member, or a text the grammar does not parse
// leaves it declaring none, and a text declaring none is never refused.
func TestVersionDecision_ReadsTheVersionTheTextDeclares(t *testing.T) {
	utf16le := func(s string) []byte {
		var out []byte
		for _, unit := range utf16.Encode([]rune(s)) {
			out = append(out, byte(unit), byte(unit>>8))
		}
		return out
	}
	for name, tc := range map[string]struct {
		input    []byte
		declared string // "" when the text declares none
	}{
		"an unsupported version":                      {[]byte(`{"openbindings":"0.3.0","operations":{}}`), "0.3.0"},
		"an escaped member name":                      {[]byte(`{"\u006fpenbindings":"0.3.0","operations":{}}`), "0.3.0"},
		"a repeated name elsewhere":                   {[]byte(`{"openbindings":"0.3.0","operations":{},"operations":{}}`), "0.3.0"},
		"a lone surrogate escaped elsewhere":          {[]byte(`{"openbindings":"0.3.0","description":"\ud800","operations":{}}`), "0.3.0"},
		"a bad byte in another member's string":       {[]byte("{\"openbindings\":\"0.3.0\",\"description\":\"x\xff\",\"operations\":{}}"), ""},
		"a truncated sequence before a closing quote": {[]byte("{\"openbindings\":\"0.3.0\",\"description\":\"x\xc2\",\"operations\":{}}"), ""},
		"an encoded surrogate in a string":            {[]byte("{\"openbindings\":\"0.3.0\",\"description\":\"\xed\xa0\x80\",\"operations\":{}}"), ""},
		"a byte-order mark":                           {append([]byte("\xef\xbb\xbf"), `{"openbindings":"0.3.0","operations":{}}`...), ""},
		"UTF-16 without a byte-order mark":            {utf16le(`{"openbindings":"0.3.0","operations":{}}`), ""},
		"a bad byte in the version":                   {[]byte("{\"openbindings\":\"0.3.\xff0\",\"operations\":{}}"), ""},
		"a bad byte in the member's name":             {[]byte("{\"openbind\xffings\":\"0.3.0\",\"operations\":{}}"), ""},
		"a bad byte within an escape":                 {[]byte("{\"openbindings\":\"0.3.0\",\"description\":\"\\u12\xff4\",\"operations\":{}}"), ""},
		"a repeated openbindings member":              {[]byte(`{"openbindings":"0.2.0","openbindings":"0.3.0","operations":{}}`), ""},
		"a version that is not SemVer":                {[]byte(`{"openbindings":"0.3","operations":{}}`), ""},
		"a version that is not a string":              {[]byte(`{"openbindings":3,"operations":{}}`), ""},
		"a member only in a nested object":            {[]byte(`{"x-a":{"openbindings":"0.3.0"},"operations":{}}`), ""},
		"an array":                                    {[]byte(`[{"openbindings":"0.3.0"}]`), ""},
		"trailing text":                               {[]byte(`{"openbindings":"0.3.0","operations":{}} {}`), ""},
	} {
		if got, declared := declaredVersion(tc.input); got != tc.declared || declared != (tc.declared != "") {
			t.Errorf("%s: declares %q, %v; want %q", name, got, declared, tc.declared)
		}
		_, _, err := ValidateDocument(tc.input)
		_, parseErr := ParseDocument(tc.input)
		for entry, err := range map[string]error{"ValidateDocument": err, "ParseDocument": parseErr} {
			if refused, want := errors.As(err, new(*VersionRefusalError)), tc.declared != ""; refused != want {
				t.Errorf("%s: %s refused %v, want %v (%v)", name, entry, refused, want, err)
			}
		}
	}
}

// The informative examples of §8.1, Version declaration: what each text
// declares, and what follows under the 0.2 line's rules. A text declaring
// another line or a prerelease is governed by that text, which this SDK does
// not apply, so it refuses it; every other text is judged.
func TestVersionDecision_TheSpecificationsExamples(t *testing.T) {
	const ff, bom = "\xff", "\xef\xbb\xbf"
	for _, tc := range []struct {
		text     string
		declared string   // "" for none
		refused  bool     // the SDK's refusal, for another line or a prerelease
		violated []string // under 0.2's rules; nil when the text conforms
	}{
		{`{"openbindings":"0.2.0","operations":{}}`, "0.2.0", false, nil},
		{`{"openbindings":"0.2.7","operations":{}}`, "0.2.7", false, nil},
		{`{"openbindings":"0.2.0+build.5","operations":{}}`, "0.2.0+build.5", false, nil},
		{`{"openbindings":"0.3.0","operations":{}}`, "0.3.0", true, nil},
		{`{"openbindings":"0.2.0-rc.1","operations":{}}`, "0.2.0-rc.1", true, nil},
		{`{"openbindings":"0.3.0","operations":{},"operations":{}}`, "0.3.0", true, nil},
		{`{"openbindings":"0.2.0","operations":{},"operations":{}}`, "0.2.0", false, []string{"OBI-01"}},
		{`{"openbindings":"0.3.0","description":"x` + ff + `","operations":{}}`, "", false, []string{"OBI-01"}},
		{bom + `{"openbindings":"0.3.0","operations":{}}`, "", false, []string{"OBI-01"}},
		{`{"openbindings":"0.2.0","openbindings":"0.3.0","operations":{}}`, "", false, []string{"OBI-01"}},
		{`{"openbindings":"0.3","operations":{}}`, "", false, []string{"OBI-02", "OBI-03"}},
		{`{"openbindings":2,"operations":{}}`, "", false, []string{"OBI-02", "OBI-03"}},
		{`{"operations":{}}`, "", false, []string{"OBI-02", "OBI-03"}},
	} {
		if got, _ := declaredVersion([]byte(tc.text)); got != tc.declared {
			t.Errorf("%q: declares %q, want %q", tc.text, got, tc.declared)
		}
		doc, report, err := ValidateDocument([]byte(tc.text))
		if tc.refused {
			if !errors.As(err, new(*VersionRefusalError)) || doc != nil || report.Evidence != nil {
				t.Errorf("%q: want a refusal and no report, got %v", tc.text, err)
			}
			continue
		}
		if errors.As(err, new(*VersionRefusalError)) {
			t.Errorf("%q: refused, want it judged: %v", tc.text, err)
			continue
		}
		want := ConclusionConformant
		if tc.violated != nil {
			want = ConclusionNonConformant
		}
		if report.Conclusion != want || !slices.Equal(report.Violated, tc.violated) {
			t.Errorf("%q: %s violating %v, want %s violating %v", tc.text, report.Conclusion, report.Violated, want, tc.violated)
		}
	}
}

// A 0.3.0 text holding an ill-formed byte inside a string declares no
// version (§8.1), so 0.2's rules govern it (§10): it violates OBI-01, every
// other rule is not applicable, and neither ValidateDocument nor
// ParseDocument refuses it.
func TestVersionDecision_AnIllFormedByteDeclaresNoVersion(t *testing.T) {
	text := []byte("{\"openbindings\":\"0.3.0\",\"description\":\"x\xff\",\"operations\":{}}")
	if version, declared := declaredVersion(text); declared {
		t.Fatalf("declares %q, want none", version)
	}
	doc, report, err := ValidateDocument(text)
	var violation *ValidationError
	if doc != nil || !errors.As(err, &violation) || report.Conclusion != ConclusionNonConformant || !slices.Equal(report.Violated, []string{"OBI-01"}) {
		t.Fatalf("document %v, %s violating %v, error %v", doc, report.Conclusion, report.Violated, err)
	}
	for _, rule := range DocumentRules()[1:] {
		if report.Evidence[rule] != EvidenceNotApplicable {
			t.Errorf("%s = %s, want not applicable", rule, report.Evidence[rule])
		}
	}
	if _, err := ParseDocument(text); !errors.As(err, &violation) || len(violation.Findings) != 1 || violation.Findings[0].Rule != "OBI-01" {
		t.Fatalf("ParseDocument: %v", err)
	}
}
