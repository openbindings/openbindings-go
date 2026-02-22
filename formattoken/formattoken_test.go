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
