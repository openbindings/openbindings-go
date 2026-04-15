package formattoken

import "testing"

func TestParse_NormalizesNameToLowercase(t *testing.T) {
	tok, err := Parse("OpenAPI@3.1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tok.Name != "openapi" || tok.Version != "3.1" {
		t.Fatalf("unexpected token: %#v", tok)
	}
	if tok.String() != "openapi@3.1" {
		t.Fatalf("unexpected string: %q", tok.String())
	}
}

func TestParse_RejectsInvalid(t *testing.T) {
	cases := []string{
		"",
		" ",
		"noatsign",
		"@3.1",
		"openapi@",
		"openapi@@3.1",
		"openapi@3.1/extra",
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

func TestNormalize(t *testing.T) {
	got, err := Normalize("OpenBindings@0.1.0")
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got != "openbindings@0.1.0" {
		t.Fatalf("unexpected: %q", got)
	}
}

func TestIsFormatToken(t *testing.T) {
	if !IsFormatToken("openapi@3.1") {
		t.Fatalf("expected true")
	}
	if IsFormatToken("openapi") {
		t.Fatalf("expected false")
	}
}

func TestIsOpenBindings(t *testing.T) {
	tok, _ := Parse("openbindings@0.1.0")
	if !IsOpenBindings(tok) {
		t.Fatalf("expected true")
	}
	tok2, _ := Parse("openapi@3.1")
	if IsOpenBindings(tok2) {
		t.Fatalf("expected false")
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		rangeToken  string
		sourceToken string
		want        bool
	}{
		{"openapi@^3.0.0", "openapi@3.1", true},
		{"openapi@^3.0.0", "openapi@3.0", true},
		{"openapi@^3.0.0", "openapi@3.0.0", true},
		{"openapi@^3.0.0", "openapi@3.9.9", true},
		{"openapi@^3.0.0", "openapi@4.0", false},
		{"openapi@^3.0.0", "openapi@2.0", false},
		{"openapi@^3.2.0", "openapi@3.1", false},
		{"mcp@2025-11-25", "mcp@2025-11-25", true},
		{"mcp@2025-11-25", "mcp@2025-12-01", false},
		{"grpc", "grpc", true},
		{"grpc", "grpc@1.0", false},
		{"grpc@1.0", "grpc", false},
		{"openapi@3.1.0", "openapi@3.1", true},
		{"openapi@3.1", "openapi@3.1.0", true},
		{"OpenAPI@^3.0.0", "openapi@3.1", true},
	}
	for _, tc := range cases {
		vr, err := ParseRange(tc.rangeToken)
		if err != nil {
			t.Fatalf("ParseRange(%q): %v", tc.rangeToken, err)
		}
		got := Matches(vr, tc.sourceToken)
		if got != tc.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", tc.rangeToken, tc.sourceToken, got, tc.want)
		}
	}
}
