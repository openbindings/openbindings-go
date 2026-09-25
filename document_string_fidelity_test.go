package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
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
	if report.Evidence["OBI-D-01"] != EvidenceSatisfied || report.Evidence["OBI-D-10"] != EvidenceInconclusive || report.Conclusion != ConclusionConformanceUndetermined {
		t.Fatalf("OBI-D-01 %q, OBI-D-10 %q, conclusion %q", report.Evidence["OBI-D-01"], report.Evidence["OBI-D-10"], report.Conclusion)
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

// The exact scan's work is linear in its input: the location of a value is
// formatted only when one is reported, not copied for every value, which
// made deep and wide input cost depth times width.
func TestVerifyExactJSON_WorkIsLinear(t *testing.T) {
	data := []byte(`{"openbindings":"0.2.0","operations":{},"x-a":` + strings.Repeat("[", 2000) + strings.Repeat("0,", 19999) + "0" + strings.Repeat("]", 2000) + `}`)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := verifyExactJSON(data); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 20*uint64(len(data)) {
		t.Fatalf("verifyExactJSON allocated %d bytes for %d bytes of input", allocated, len(data))
	}
}

// The exact scan walks with its own stack. It finds exactly what a recursive
// walk of the same input finds: the first repeated name and, before it, the
// first lone surrogate, at the same locations. (The recursive walk stops at
// the repeat; the scan reads on.)
func FuzzExactScan(f *testing.F) {
	for _, seed := range []string{`{}`, `[]`, `{"a":[1,{"b":2,"b":3}]}`, `[[],[{}],{"\ud800":1}]`, `{"a":{"x":"\udc00"},"a":2}`, `[0,"\ud800",[1,2,{"c":{"c":1,"c":2}}]]`, `"x"`, `{"":{"":[{"":1,"":2}]}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		b := []byte(input)
		if !utf8.Valid(b) || !json.Valid(b) {
			return
		}
		scan := exactScan{b: b}
		if err := scan.run(); err != nil {
			t.Fatalf("%s: valid JSON refused: %v", input, err)
		}
		var got error
		if scan.repeat != nil {
			got = scan.repeat
		}
		var want recursiveScan
		_, wantErr := want.value(b, skipJSONSpace(b, 0), nil)
		if fmt.Sprint(got) != fmt.Sprint(wantErr) || wantErr == nil && fmt.Sprint(scan.lone) != fmt.Sprint(want.lone) {
			t.Fatalf("%s: scan %v / %v, recursive walk %v / %v", input, got, scan.lone, wantErr, want.lone)
		}
	})
}

// recursiveScan is the recursive walk the exact scan replaced, kept as the
// reference it is checked against.
type recursiveScan struct{ lone *loneSurrogateError }

func (s *recursiveScan) value(b []byte, i int, tokens []string) (int, error) {
	switch b[i] {
	case '{':
		seen := map[string]bool{}
		i = skipJSONSpace(b, i+1)
		for b[i] != '}' {
			nameEnd := jsonValueEnd(b, i)
			name, lone := exactString(b[i:nameEnd])
			if lone && s.lone == nil {
				s.lone = &loneSurrogateError{location: jsonpointer.Format(tokens...), name: true}
			}
			if seen[name] {
				return 0, &duplicateNameError{location: jsonpointer.Format(tokens...), name: name}
			}
			seen[name] = true
			end, err := s.value(b, skipJSONSpace(b, skipJSONSpace(b, nameEnd)+1), append(slices.Clip(tokens), name))
			if err != nil {
				return 0, err
			}
			if i = skipJSONSpace(b, end); b[i] == ',' {
				i = skipJSONSpace(b, i+1)
			}
		}
		return i + 1, nil
	case '[':
		i = skipJSONSpace(b, i+1)
		for index := 0; b[i] != ']'; index++ {
			end, err := s.value(b, i, append(slices.Clip(tokens), strconv.Itoa(index)))
			if err != nil {
				return 0, err
			}
			if i = skipJSONSpace(b, end); b[i] == ',' {
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

// The exact scan accepts exactly what encoding/json accepts as one JSON value,
// wherever encoding/json can read it, and needs nothing from encoding/json
// to decide (it reads deeper input in full).
func FuzzExactScanValidity(f *testing.F) {
	for _, seed := range []string{`{}`, ` [1, 2.5e-3, -0, true, false, null] `, `{"a":"\u00e9\n"}`, `01`, `[1,]`, `{"a" 1}`, `"\x"`, `"\u12"`, `1.`, `-`, `1e+`, `tru`, `nul`, `{"a":1}{}`, `[`, `"\ud800\udc00"`, "\"\x01\""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		b := []byte(input)
		if !utf8.Valid(b) || strings.Count(input, "[")+strings.Count(input, "{") >= jsonNestingLimit {
			return
		}
		scan := exactScan{b: b}
		if err := scan.run(); (err == nil) != json.Valid(b) {
			t.Fatalf("%q: scan %v, encoding/json valid %v", input, err, json.Valid(b))
		}
	})
}

// A syntax error is worded as encoding/json words its own, at any depth.
func TestExactScan_SyntaxErrors(t *testing.T) {
	for input, want := range map[string]string{
		``:              "unexpected end of JSON input",
		`   `:           "unexpected end of JSON input",
		`{"a":1`:        "unexpected end of JSON input",
		`x`:             "invalid character 'x' looking for beginning of value",
		`01`:            "invalid character '1' after top-level value",
		`[1,]`:          "invalid character ']' looking for beginning of value",
		`[1 2]`:         "invalid character '2' after array element",
		`{"a" 1}`:       "invalid character '1' after object key",
		`{"a":1 "b":2}`: "invalid character '\"' after object key:value pair",
		`{1:2}`:         "invalid character '1' looking for beginning of object key string",
		`{"a":1,}`:      "invalid character '}' looking for beginning of object key string",
		"\"\x01\"":      "invalid character '\\x01' in string literal",
		`"\x"`:          "invalid character 'x' in string escape code",
		`"\u12x4"`:      "invalid character 'x' in \\u hexadecimal character escape",
		`-x`:            "invalid character 'x' in numeric literal",
		`1.x`:           "invalid character 'x' after decimal point in numeric literal",
		`1ex`:           "invalid character 'x' in exponent of numeric literal",
		`trux`:          "invalid character 'x' in literal true (expecting 'e')",
		`{} []`:         "invalid character '[' after top-level value",
	} {
		scan := exactScan{b: []byte(input)}
		if err := scan.run(); err == nil || err.Error() != want {
			t.Errorf("%q: got %v, want %q", input, err, want)
		}
	}
}
