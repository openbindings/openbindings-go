package invoke

import (
	"context"
	"testing"
)

// config.value schema (2026-08-20 working-draft amendment): a config.value
// requirement MAY carry an engine-asserted `schema` (JSON Schema) for the
// value at (point, path). Absent = unconstrained; an `enum` member is a
// closed admissible set that satisfaction validates against; `choices` is
// removed everywhere — one mechanism, no sugar.

func TestNewConfigValueRequirement_SchemaCarriage(t *testing.T) {
	durable := true
	req := NewConfigValueRequirement("server", "/url", "supply a connection URL",
		map[string]any{"enum": []any{"wss://a.example.com", "wss://b.example.com"}}, &durable)
	schema, ok := req.Extra["schema"].(map[string]any)
	if !ok {
		t.Fatalf("Extra[\"schema\"] = %v, want the asserted schema object", req.Extra["schema"])
	}
	if members, _ := schema["enum"].([]any); len(members) != 2 {
		t.Errorf("schema enum = %v, want both admissible values", schema["enum"])
	}
	if _, present := req.Extra["choices"]; present {
		t.Error("choices is removed from the contract; the constructor must not emit it")
	}

	absent := NewConfigValueRequirement("server", "/url", "supply a connection URL", nil, nil)
	if _, present := absent.Extra["schema"]; present {
		t.Error("a nil schema is absent (unconstrained), never an empty member")
	}
}

func TestValidContextRequiredDetails_SchemaMember(t *testing.T) {
	build := func(schema any, includeSchema bool) *ContextRequiredDetails {
		extra := map[string]any{"point": "server", "path": "/url"}
		if includeSchema {
			extra["schema"] = schema
		}
		return &ContextRequiredDetails{
			Target: "https://example.com/spec.yaml",
			Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{
				{Type: "config.value", Extra: extra},
			}}},
		}
	}
	if !ValidContextRequiredDetails(build(nil, false)) {
		t.Error("an absent schema is valid (unconstrained)")
	}
	if !ValidContextRequiredDetails(build(map[string]any{"enum": []any{"x"}}, true)) {
		t.Error("an object schema is valid")
	}
	// The member is required to be a JSON object, not metaschema-validated:
	// arbitrary members pass here and answer to the emitting engine.
	if !ValidContextRequiredDetails(build(map[string]any{"anything": 1.0}, true)) {
		t.Error("schema content must not be metaschema-validated by this gate")
	}
	for _, invalid := range []any{"string", []any{"a", "b"}, 42.0, true, nil} {
		if ValidContextRequiredDetails(build(invalid, true)) {
			t.Errorf("a present non-object schema (%v) must invalidate the challenge", invalid)
		}
	}
}

func TestContextSatisfies_ConfigValueValidatesAgainstSchema(t *testing.T) {
	challenge := func(schema map[string]any) *ContextRequiredDetails {
		extra := map[string]any{"point": "server", "path": "/variables/env"}
		if schema != nil {
			extra["schema"] = schema
		}
		return &ContextRequiredDetails{
			Target: "https://example.com/spec.yaml",
			Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{
				{Type: "config.value", Extra: extra},
			}}},
		}
	}
	stored := func(value any) map[string]any {
		return map[string]any{"configuration": map[string]any{
			"server": map[string]any{"variables": map[string]any{"env": value}},
		}}
	}

	enum := map[string]any{"enum": []any{"prod", "staging"}}
	if !ContextSatisfies(stored("prod"), challenge(enum)) {
		t.Error("a value inside the closed enum satisfies")
	}
	if ContextSatisfies(stored("dev"), challenge(enum)) {
		t.Error("a value outside the closed enum must not satisfy")
	}
	if !ContextSatisfies(stored("dev"), challenge(nil)) {
		t.Error("without a schema, presence of a non-empty value satisfies (unconstrained)")
	}

	typed := map[string]any{"type": "string"}
	if !ContextSatisfies(stored("anything"), challenge(typed)) {
		t.Error("a general (non-enum) schema validates the selected value: matching value satisfies")
	}
	if ContextSatisfies(stored(42.0), challenge(typed)) {
		t.Error("a general (non-enum) schema validates the selected value: mismatching value must not satisfy")
	}
}

// Keying rule (context-scope model, ratified 2026-08-19): an alternative
// consisting solely of config.value requirements files and fetches under the
// EXACT asserted target — an artifact-bound scope, typically the
// canonicalized source location, which endpoint normalization would conflate
// with its origin (many artifacts on one host). Credential-bearing
// alternatives keep the endpoint-normalized convention.
func TestStoreContextResolver_ConfigOnlyAlternativeKeysByExactTarget(t *testing.T) {
	source := "https://example.com/specs/orders.json"
	durable := true
	store := testStore{
		source: {"configuration": map[string]any{"server": map[string]any{"url": "wss://broker.orders.example.com"}}},
		// The origin-keyed entry belongs to a DIFFERENT artifact on the same
		// host; the config-only alternative must never reach it.
		"example.com": {"configuration": map[string]any{"server": map[string]any{"url": "wss://broker.other.example.com"}}},
	}
	details := &ContextRequiredDetails{
		Target: source,
		Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{
			{Type: "config.value", Durable: &durable, Extra: map[string]any{"point": "server", "path": "/url"}},
		}}},
	}
	got, err := StoreContextResolver(store)(context.Background(), details)
	if err != nil {
		t.Fatal(err)
	}
	configuration, _ := got["configuration"].(map[string]any)
	point, _ := configuration["server"].(map[string]any)
	if point == nil || point["url"] != "wss://broker.orders.example.com" {
		t.Fatalf("resolved %v, want the exact-target entry's configuration", got)
	}
}

func TestStoreContextResolver_CredentialAlternativeKeepsNormalizedKey(t *testing.T) {
	durable := true
	// Nothing is filed under the exact source-location target; the
	// credential-bearing alternative still resolves from its origin key.
	store := testStore{"example.com": {"bearerToken": "stored-tok"}}
	details := &ContextRequiredDetails{
		Target: "https://example.com/specs/orders.json",
		Alternatives: []ContextAlternative{
			{Requirements: []ContextRequirement{
				{Type: "config.value", Durable: &durable, Extra: map[string]any{"point": "server", "path": "/url"}},
			}},
			{Requirements: []ContextRequirement{{Type: "auth.bearer", Durable: &durable}}},
		},
	}
	got, err := StoreContextResolver(store)(context.Background(), details)
	if err != nil {
		t.Fatal(err)
	}
	if ContextBearerToken(got) != "stored-tok" {
		t.Fatalf("resolved %v, want the origin-keyed bearer token", got)
	}
}

func TestStoreContextResolver_MixedAlternativeKeepsNormalizedKey(t *testing.T) {
	durable := true
	// An alternative carrying ANY credential family keys by the normalized
	// endpoint, even when it also carries config.value members.
	store := testStore{"example.com": {
		"bearerToken":   "stored-tok",
		"configuration": map[string]any{"server": map[string]any{"url": "wss://broker.example.com"}},
	}}
	details := &ContextRequiredDetails{
		Target: "https://example.com/specs/orders.json",
		Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{
			{Type: "auth.bearer", Durable: &durable},
			{Type: "config.value", Durable: &durable, Extra: map[string]any{"point": "server", "path": "/url"}},
		}}},
	}
	got, err := StoreContextResolver(store)(context.Background(), details)
	if err != nil {
		t.Fatal(err)
	}
	if ContextBearerToken(got) != "stored-tok" {
		t.Fatalf("resolved %v, want the origin-keyed entry to satisfy the mixed alternative", got)
	}
}
