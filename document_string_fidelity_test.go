package openbindings

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// An escape of a lone UTF-16 surrogate is RFC 8259 JSON, so it breaks no
// document rule, but a Go string cannot hold it and encoding/json would
// replace it with U+FFFD. The document model refuses it rather than alter
// the document; validation decides OBI-D-01 and leaves every other rule
// inconclusive, never violated.
func TestDocumentStrings_LoneSurrogatesAreNotCarried(t *testing.T) {
	raw := []byte(`{"openbindings":"0.2.0","operations":{"echo":{"input":{"type":"string","maxLength":1},"examples":{"unit":{"input":"\ud800"}}}}}`)

	var iface Interface
	if err := json.Unmarshal(raw, &iface); err == nil || !strings.Contains(err.Error(), "lone UTF-16 surrogate") {
		t.Fatalf("decoding must refuse, got %v", err)
	}
	if _, err := ParseDocument(raw); err == nil || errors.As(err, new(*ValidationError)) || !strings.Contains(err.Error(), "/operations/echo/examples/unit/input") {
		t.Fatalf("parsing must refuse, locating the string, without a violation: %v", err)
	}

	decoded, report, err := ValidateDocument(raw, ValidateOptions{})
	if err != nil || decoded != nil {
		t.Fatalf("no violation is established and no document decoded: %v", err)
	}
	if report.Evidence["OBI-D-01"] != EvidenceSatisfied || report.Evidence["OBI-D-11"] != EvidenceInconclusive || report.Conclusion != ConclusionConformanceUndetermined {
		t.Fatalf("OBI-D-01 %q, OBI-D-11 %q, conclusion %q", report.Evidence["OBI-D-01"], report.Evidence["OBI-D-11"], report.Conclusion)
	}
}

// Member names are compared exactly: a lone surrogate and U+FFFD are two
// names, and the same lone surrogate twice is a repeated one (OBI-D-01).
func TestDocumentStrings_NamesCompareExactly(t *testing.T) {
	_, report, _ := ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{},"x-values":{"\ud800":1,"�":2}}`), ValidateOptions{})
	if report.Evidence["OBI-D-01"] != EvidenceSatisfied {
		t.Fatalf("distinct names are no duplicate: OBI-D-01 %q", report.Evidence["OBI-D-01"])
	}
	_, report, _ = ValidateDocument([]byte(`{"openbindings":"0.2.0","operations":{},"x-values":{"\ud800":1,"\uD800":2}}`), ValidateOptions{})
	if report.Evidence["OBI-D-01"] != EvidenceViolated {
		t.Fatalf("the same name twice is a duplicate: OBI-D-01 %q", report.Evidence["OBI-D-01"])
	}
}

// A surrogate pair escapes one character, which the model carries.
func TestDocumentStrings_SurrogatePairsAreCarried(t *testing.T) {
	var iface Interface
	if err := json.Unmarshal([]byte(`{"openbindings":"0.2.0","name":"😀","operations":{}}`), &iface); err != nil {
		t.Fatal(err)
	}
	if Value(iface.Name) != "😀" {
		t.Fatalf("got %q", Value(iface.Name))
	}
}

// exactString agrees with encoding/json on every string without a lone
// surrogate, and reports one exactly when encoding/json substitutes U+FFFD
// for an escape.
func FuzzExactString(f *testing.F) {
	for _, seed := range []string{`""`, `"a"`, `"\u00e9"`, `"\ud83d\ude00"`, `"\ud800"`, `"\udc00x"`, `"\ud800\ud800"`, `"\\u0041\n\t\"\/"`, `"\ufffd"`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		// The scanner hands exactString one whole string token of valid UTF-8
		// JSON: verifyExactJSON refuses invalid UTF-8 first.
		if !utf8.ValidString(token) || !json.Valid([]byte(token)) || len(token) < 2 || token[0] != '"' || jsonValueEnd([]byte(token), 0) != len(token) {
			return
		}
		var want string
		if err := json.Unmarshal([]byte(token), &want); err != nil {
			return
		}
		got, lone := exactString([]byte(token))
		if !lone && got != want {
			t.Fatalf("%s: exact %q, encoding/json %q", token, got, want)
		}
		if lone && !strings.Contains(want, "\ufffd") {
			t.Fatalf("%s: a lone surrogate encoding/json kept: %q", token, want)
		}
	})
}
