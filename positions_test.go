package openbindings

import (
	"errors"
	"testing"
)

// Findings from bytes are positioned where the input holds what they are
// about: a member at its name, an array element at its value, a break in
// the syntax at the byte that breaks it.
func TestFindings_Positions(t *testing.T) {
	document := "{\n" +
		"  \"openbindings\": \"0.2.0\",\n" +
		"  \"description\": 5,\n" +
		"  \"operations\": {\n" +
		"    \"bad key\": {},\n" +
		"    \"op\": {\"aliases\": [\"ok\", \"no good\"]}\n" +
		"  }\n" +
		"}\n"
	_, report, _ := ValidateDocument([]byte(document))
	want := map[string]string{
		"/description":             "3:3",
		"/operations/bad key":      "5:5",
		"/operations/op/aliases/1": "6:30",
	}
	seen := map[string]bool{}
	for _, finding := range report.Findings {
		if position, ok := want[finding.Path]; ok {
			seen[finding.Path] = true
			if finding.Position.String() != position {
				t.Errorf("%s %s at %s, want %s", finding.Rule, finding.Path, finding.Position, position)
			}
		}
	}
	for path := range want {
		if !seen[path] {
			t.Errorf("no finding at %s; findings %+v", path, report.Findings)
		}
	}

	// ParseDocument positions its findings the same way.
	var violation *ValidationError
	if _, err := ParseDocument([]byte(document)); !errors.As(err, &violation) {
		t.Fatalf("ParseDocument: %v", err)
	}
	for _, finding := range violation.Findings {
		if !finding.Position.IsValid() {
			t.Errorf("ParseDocument: %s at %s has no position", finding.Rule, finding.Path)
		}
	}

	// Document.Validate reads no bytes, so its findings carry none.
	iface := mustDecodeInterface(t, `{"openbindings":"0.2.0","operations":{"op":{"aliases":["no good"]}}}`)
	if _, err := iface.Validate(); !errors.As(err, &violation) || violation.Findings[0].Position.IsValid() {
		t.Errorf("Document.Validate: %v", err)
	}
}

// OBI-D-01 findings are positioned at the byte that breaks the input.
func TestFindings_D01Positions(t *testing.T) {
	for _, c := range []struct {
		name, input, want string
	}{
		{"a trailing comma", "{\n  \"openbindings\": \"0.2.0\",\n}", "3:1"},
		{"input that ends too early", "{\"openbindings\": \"0.2.0\"", "1:25"},
		{"a byte that is not UTF-8", "{\"openbindings\": \"0.2.0\", \"x\": \"\xff\"}", "1:33"},
		{"a byte-order mark", "\xef\xbb\xbf{\"openbindings\": \"0.2.0\"}", "1:1"},
		{"a repeated name", "{\"openbindings\": \"0.2.0\",\n \"openbindings\": \"0.2.0\"}", "2:2"},
	} {
		_, report, _ := ValidateDocument([]byte(c.input))
		violations := report.Violations()
		if len(violations) != 1 || violations[0].Rule != "OBI-D-01" || violations[0].Position.String() != c.want {
			t.Errorf("%s: %+v, want OBI-D-01 at %s", c.name, violations, c.want)
		}
	}
}
