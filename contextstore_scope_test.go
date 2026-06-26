package openbindings

import (
	"reflect"
	"testing"
)

// ScopeContext enforces least privilege: a CONTEXT_REQUIRED challenge is a
// scope, not a hint. Non-secret config always passes; only the satisfied
// alternative's credential family is admitted; other credentials are withheld.

func TestScopeContext_WithholdsUnrelatedCredentials(t *testing.T) {
	stored := map[string]any{
		"bearerToken": "tok",
		"apiKey":      "key", // unrelated credential, must be withheld
		"headers":     map[string]any{"Accept": "application/json"},
	}
	details := &ContextRequiredDetails{
		Target: "https://api.example.com",
		Alternatives: []ContextAlternative{
			{Requirements: []ContextRequirement{{Type: "auth.bearer"}}},
		},
	}
	got := ScopeContext(stored, details)
	want := map[string]any{
		"bearerToken": "tok",
		"headers":     map[string]any{"Accept": "application/json"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped = %v, want %v (apiKey withheld, headers kept)", got, want)
	}
}

func TestScopeContext_AdmitsWholeOAuth2Family(t *testing.T) {
	stored := map[string]any{
		"accessToken":  "at",
		"refreshToken": "rt",
		"clientSecret": "cs",
		"apiKey":       "key", // unrelated, must be withheld
		"headers":      map[string]any{"X": "1"},
	}
	details := &ContextRequiredDetails{
		Alternatives: []ContextAlternative{
			{Requirements: []ContextRequirement{{Type: "auth.oauth2"}}},
		},
	}
	got := ScopeContext(stored, details)
	for _, f := range []string{"accessToken", "refreshToken", "clientSecret"} {
		if got[f] == nil {
			t.Errorf("oauth2 family field %q must be admitted, got %v", f, got)
		}
	}
	if _, ok := got["apiKey"]; ok {
		t.Errorf("unrelated apiKey must be withheld, got %v", got)
	}
	if _, ok := got["headers"]; !ok {
		t.Errorf("non-secret headers must pass through, got %v", got)
	}
}

func TestScopeContext_NilDetailsReturnsIndependentCopy(t *testing.T) {
	stored := map[string]any{"bearerToken": "tok", "headers": map[string]any{"A": "b"}}
	got := ScopeContext(stored, nil)
	if !reflect.DeepEqual(got, stored) {
		t.Errorf("nil details should return the full context, got %v", got)
	}
	got["injected"] = "x"
	if _, leaked := stored["injected"]; leaked {
		t.Error("ScopeContext must return a copy, not alias the input map")
	}
}

func TestScopeContext_NilInput(t *testing.T) {
	if ScopeContext(nil, &ContextRequiredDetails{}) != nil {
		t.Error("nil input must return nil")
	}
}
