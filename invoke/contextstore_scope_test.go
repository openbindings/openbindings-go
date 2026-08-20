package invoke

import (
	"reflect"
	"testing"
)

// ScopeContext enforces least privilege: a CONTEXT_REQUIRED challenge is a
// scope, not a hint. Only fields named by the satisfied alternative pass;
// arbitrary stored context is not assumed public or relevant.

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
	want := map[string]any{"bearerToken": "tok"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped = %v, want %v (unrelated apiKey and headers withheld)", got, want)
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
	if _, ok := got["headers"]; ok {
		t.Errorf("unrequested headers must be withheld, got %v", got)
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

// TestScopeContext_AdmitsOnlyTheNamedAPIKey is the R2.e ruling's core test:
// a stored context carrying apiKeys entries for TWO names, challenged by an
// alternative naming only ONE of them, must scope to exactly that one entry
// — never the whole apiKeys map, and never the other alternative's key.
func TestScopeContext_AdmitsOnlyTheNamedAPIKey(t *testing.T) {
	stored := map[string]any{
		"apiKeys": map[string]any{
			"serviceA": "key-a",
			"serviceB": "key-b", // a different alternative's key; must be withheld
		},
		"headers": map[string]any{"Accept": "application/json"},
	}
	details := &ContextRequiredDetails{
		Target: "https://api.example.com",
		Alternatives: []ContextAlternative{
			{Requirements: []ContextRequirement{{Type: "auth.apiKey", Name: "serviceA"}}},
		},
	}
	got := ScopeContext(stored, details)
	want := map[string]any{"apiKeys": map[string]any{"serviceA": "key-a"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped = %v, want %v (serviceB withheld)", got, want)
	}
}

// TestScopeContext_AdmitsBothNamedAPIKeysForANDedAlternative verifies the
// R2.d/e pairing: an alternative that ANDs two named apiKey requirements
// admits both named entries (and only those two).
func TestScopeContext_AdmitsBothNamedAPIKeysForANDedAlternative(t *testing.T) {
	stored := map[string]any{
		"apiKeys": map[string]any{
			"headerKey": "hdr-key",
			"queryKey":  "qry-key",
			"unrelated": "should-be-withheld",
		},
	}
	details := &ContextRequiredDetails{
		Alternatives: []ContextAlternative{
			{Requirements: []ContextRequirement{
				{Type: "auth.apiKey", Name: "headerKey"},
				{Type: "auth.apiKey", Name: "queryKey"},
			}},
		},
	}
	got := ScopeContext(stored, details)
	want := map[string]any{
		"apiKeys": map[string]any{"headerKey": "hdr-key", "queryKey": "qry-key"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped = %v, want %v", got, want)
	}
}

// TestScopeContext_NamedAPIKeyFallsBackToPlainApiKey verifies the same
// fallback priority as ContextAPIKeyFor: when no apiKeys map satisfies the
// named requirement, the plain "apiKey" field (if present) is what actually
// satisfied it, and that is what gets admitted.
func TestScopeContext_NamedAPIKeyFallsBackToPlainApiKey(t *testing.T) {
	stored := map[string]any{"apiKey": "fallback-key"}
	details := &ContextRequiredDetails{
		Alternatives: []ContextAlternative{
			{Requirements: []ContextRequirement{{Type: "auth.apiKey", Name: "svcKey"}}},
		},
	}
	got := ScopeContext(stored, details)
	want := map[string]any{"apiKey": "fallback-key"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped = %v, want %v", got, want)
	}
}

// TestScopeContext_UnmappedAlternativeAdmitsNoCredentials verifies the R2.c
// ruling holds through scoping: an alternative whose only requirement is an
// unmapped family (e.g. "auth.http.digest") is never satisfiable
// (requirementSatisfied always declines it), so ScopeContext falls through
// the whole loop admitting nothing — real stored credentials (bearerToken,
// apiKey) and unrelated fields stay withheld exactly as when no alternative
// is satisfied at all.
func TestScopeContext_UnmappedAlternativeAdmitsNoCredentials(t *testing.T) {
	stored := map[string]any{
		"bearerToken": "should-be-withheld",
		"apiKey":      "should-be-withheld",
		"headers":     map[string]any{"X": "1"},
	}
	details := &ContextRequiredDetails{
		Alternatives: []ContextAlternative{
			{Requirements: []ContextRequirement{{Type: "auth.http.digest"}}},
		},
	}
	got := ScopeContext(stored, details)
	want := map[string]any{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped = %v, want %v (no alternative is satisfiable, so no credential is admitted)", got, want)
	}
}

func TestScopeContext_AdmitsOnlyNamedConfigurationPoint(t *testing.T) {
	stored := map[string]any{
		"configuration": map[string]any{
			"server": map[string]any{"url": "https://api.example.com"},
			"decode": "text",
		},
		"headers": map[string]any{"Authorization": "sensitive"},
	}
	details := &ContextRequiredDetails{
		Target: "https://api.example.com",
		Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{
			Type:  "config.value",
			Extra: map[string]any{"point": "server", "path": "/url"},
		}}}},
	}
	got := ScopeContext(stored, details)
	want := map[string]any{"configuration": map[string]any{
		"server": map[string]any{"url": "https://api.example.com"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped = %v, want %v", got, want)
	}
}
