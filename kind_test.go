package openbindings

import (
	"errors"
	"testing"
)

func TestDependencyAllowsKind_ExactAndSupportIndependent(t *testing.T) {
	d := DependencyEntry{Kinds: []string{"example.openapi@1", "É", "https://example.test/kind"}}
	for _, tc := range []struct {
		kind string
		want bool
	}{
		{"example.openapi@1", true},
		{"example.openapi@2", false},
		{"example.openapi@1.0", false},
		{"EXAMPLE.OPENAPI@1", false},
		{"É", true},
		{"É", false},
		{"https://example.test/kind", true},
		{"https://example.test/kind/", false},
		{"unknown@9", false},
	} {
		if got := d.AllowsKind(tc.kind); got != tc.want {
			t.Errorf("AllowsKind(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
	if !(DependencyEntry{}).AllowsKind("unknown@9") {
		t.Fatal("omitted kinds must impose no constraint")
	}
	if (DependencyEntry{Kinds: []string{}}).AllowsKind("unknown@9") {
		t.Fatal("present empty kinds must not behave as omitted")
	}
}

func TestUnsupportedKindsDoNotPreventCoreValidation(t *testing.T) {
	data := []byte(`{"openbindings":"0.2.0","operations":{"op":{}},"sources":{"s":{"kind":"unknown@9"}},"bindings":{"b":{"operation":"op","source":"s"}},"dependencies":{"d":{"operation":"op","kinds":["unknown@9"]}}}`)
	iface, report, err := ValidateDocument(data, ValidateOptions{})
	if err != nil {
		t.Fatalf("unknown kind rejected: %v", err)
	}
	if report.Conclusion != ConclusionConformant {
		t.Fatalf("unknown kind report: %s", report.Conclusion)
	}
	if !iface.Dependencies["d"].AllowsKind(iface.Sources["s"].Kind) {
		t.Fatal("same unsupported kind must meet an exact constraint")
	}
}

func TestFormerKindFieldsAreNotCoreFields(t *testing.T) {
	data := []byte(`{"openbindings":"0.2.0","operations":{"op":{}},"sources":{"s":{"bindingSpec":"old@1"}},"dependencies":{"d":{"operation":"op","bindingSpecs":["old@1"]}}}`)
	_, report, err := ValidateDocument(data, ValidateOptions{})
	if err == nil {
		t.Fatal("former fields must not make a conformant document")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) || report.Conclusion != ConclusionNonConformant {
		t.Fatalf("former fields yielded %v, %s", err, report.Conclusion)
	}
}
