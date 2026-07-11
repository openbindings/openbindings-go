package openbindings

// Binding execution context (BEC) tests: the context store, the well-known
// context helpers, and the store-backed CONTEXT_REQUIRED resolver that
// composes the binding-invoker and context-store roles.

import (
	"context"
	"testing"
)

// testStore is a minimal in-memory ContextStore for exercising the
// store-backed resolver. Storage backing is the consuming tool's job, so the
// SDK ships only the ContextStore interface; tests supply their own.
type testStore map[string]map[string]any

func (s testStore) Get(_ context.Context, key string) (map[string]any, error) {
	return s[key], nil
}

func (s testStore) Set(_ context.Context, key string, value map[string]any) error {
	if value == nil {
		delete(s, key)
		return nil
	}
	s[key] = value
	return nil
}

func (s testStore) Delete(_ context.Context, key string) error {
	delete(s, key)
	return nil
}

// bearerOrAPIKey carries a raw target URL; the resolver normalizes it to the
// "api.example.com" store key.
var bearerOrAPIKey = &ContextRequiredDetails{
	Target: "https://api.example.com/v1/users",
	Alternatives: []ContextAlternative{
		{Requirements: []ContextRequirement{{Type: "auth.bearer"}}},
		{Requirements: []ContextRequirement{{Type: "auth.apiKey"}}},
	},
}

func TestContextSatisfies(t *testing.T) {
	t.Run("any one alternative suffices (disjunctive)", func(t *testing.T) {
		if !ContextSatisfies(map[string]any{"bearerToken": "t"}, bearerOrAPIKey) {
			t.Fatal("bearer should satisfy")
		}
		if !ContextSatisfies(map[string]any{"apiKey": "k"}, bearerOrAPIKey) {
			t.Fatal("apiKey should satisfy")
		}
	})

	t.Run("all requirements within an alternative (conjunctive)", func(t *testing.T) {
		details := &ContextRequiredDetails{
			Target: "k",
			Alternatives: []ContextAlternative{
				{Requirements: []ContextRequirement{{Type: "auth.basic"}, {Type: "config.value"}}},
			},
		}
		if ContextSatisfies(map[string]any{"basic": map[string]any{"username": "u"}}, details) {
			t.Fatal("partial conjunction must not satisfy")
		}
		ctx := map[string]any{
			"basic":        map[string]any{"username": "u", "password": "p"},
			"config.value": "x",
		}
		if !ContextSatisfies(ctx, details) {
			t.Fatal("full conjunction should satisfy")
		}
	})

	t.Run("empty or missing fields do not satisfy", func(t *testing.T) {
		if ContextSatisfies(map[string]any{}, bearerOrAPIKey) {
			t.Fatal("empty context must not satisfy")
		}
		if ContextSatisfies(map[string]any{"bearerToken": ""}, bearerOrAPIKey) {
			t.Fatal("empty string must not satisfy")
		}
	})

	t.Run("standard families map to well-known fields", func(t *testing.T) {
		details := &ContextRequiredDetails{
			Target:       "k",
			Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.oauth2"}}}},
		}
		if !ContextSatisfies(map[string]any{"accessToken": "a"}, details) {
			t.Fatal("auth.oauth2 should map to accessToken")
		}
	})

	// R2.d ruling: a NAMED auth.apiKey requirement is satisfied by the
	// scheme-scoped apiKeys[name] entry, falling back to the single apiKey
	// convenience field — the same priority ContextAPIKeyFor applies.
	t.Run("named auth.apiKey checks apiKeys[name] first, falls back to apiKey", func(t *testing.T) {
		named := &ContextRequiredDetails{
			Target:       "k",
			Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.apiKey", Name: "svcKey"}}}},
		}
		if !ContextSatisfies(map[string]any{"apiKeys": map[string]any{"svcKey": "k1"}}, named) {
			t.Fatal("apiKeys[name] entry should satisfy a named requirement")
		}
		if ContextSatisfies(map[string]any{"apiKeys": map[string]any{"otherKey": "k1"}}, named) {
			t.Fatal("an apiKeys entry under a DIFFERENT name must not satisfy")
		}
		if !ContextSatisfies(map[string]any{"apiKey": "fallback"}, named) {
			t.Fatal("the plain apiKey field must still satisfy a named requirement (fallback)")
		}
	})

	// R2.c ruling: an unmapped requirement family (a scheme this SDK
	// surfaces but has no resolver for) must never be satisfiable by the
	// built-in check — no resolver at this layer, no invented convention.
	t.Run("unmapped family is never satisfiable", func(t *testing.T) {
		details := &ContextRequiredDetails{
			Target:       "k",
			Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.http.digest"}}}},
		}
		ctx := map[string]any{
			"bearerToken":      "t",
			"apiKey":           "k",
			"basic":            map[string]any{"username": "u", "password": "p"},
			"accessToken":      "a",
			"auth.http.digest": "anything", // even a literally-matching key must not count
		}
		if ContextSatisfies(ctx, details) {
			t.Fatal("an unmapped requirement family must never be satisfiable by the built-in check")
		}
	})
}

func TestStoreContextResolver(t *testing.T) {
	ctx := context.Background()

	t.Run("normalizes the target into the store key", func(t *testing.T) {
		// Stored under the normalized host; the raw target carries a URL.
		store := testStore{"api.example.com": {"bearerToken": "stored-tok"}}
		resolve := StoreContextResolver(store)
		got, err := resolve(ctx, bearerOrAPIKey)
		if err != nil || ContextBearerToken(got) != "stored-tok" {
			t.Fatalf("got %v err=%v", got, err)
		}
	})

	t.Run("declines when nothing stored", func(t *testing.T) {
		resolve := StoreContextResolver(testStore{})
		got, err := resolve(ctx, bearerOrAPIKey)
		if err != nil || got != nil {
			t.Fatalf("expected decline, got %v err=%v", got, err)
		}
	})

	t.Run("declines when stored context is insufficient", func(t *testing.T) {
		store := testStore{"api.example.com": {"unrelated": "field"}}
		resolve := StoreContextResolver(store)
		got, err := resolve(ctx, bearerOrAPIKey)
		if err != nil || got != nil {
			t.Fatalf("expected decline, got %v err=%v", got, err)
		}
	})

	// R2.e ruling, end to end through the store-backed resolver: a store
	// carrying apiKeys entries for TWO names, challenged by an alternative
	// naming only one, must resolve a scoped context carrying only that one
	// entry — never the other name's key.
	t.Run("scopes apiKeys to only the challenged name", func(t *testing.T) {
		store := testStore{"api.example.com": {
			"apiKeys": map[string]any{
				"serviceA": "key-a",
				"serviceB": "key-b",
			},
		}}
		resolve := StoreContextResolver(store)
		details := &ContextRequiredDetails{
			Target: "https://api.example.com/v1",
			Alternatives: []ContextAlternative{
				{Requirements: []ContextRequirement{{Type: "auth.apiKey", Name: "serviceA"}}},
			},
		}
		got, err := resolve(ctx, details)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ContextAPIKeyFor(got, "serviceA") != "key-a" {
			t.Fatalf("serviceA key not resolved: %v", got)
		}
		if ContextAPIKeyFor(got, "serviceB") != "" {
			t.Fatalf("serviceB key must be withheld: %v", got)
		}
	})
}

func TestStoreResolverDrivesRetryEndToEnd(t *testing.T) {
	// Composition test: binding challenges, the store-backed resolver
	// supplies the stored credential, the operation invoker replays.
	store := testStore{"api.example.com": {"bearerToken": "stored"}}

	mock := &mockBindingInvoker{opts: mockOpts{requireBearer: true}}
	op := newOpInvoker(mock, StoreContextResolver(store))
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["name"] != "Ada" {
		t.Fatalf("got %v", v)
	}
	if _, _, _, contexts := mock.snapshot(); ContextBearerToken(contexts[1]) != "stored" {
		t.Fatalf("retry context: %v", contexts[1])
	}
}

func TestWithRuntimeSwapsResolver(t *testing.T) {
	base := newOpInvoker(&mockBindingInvoker{}, nil)
	called := false
	scoped := base.WithRuntime(func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		called = true
		return nil, nil
	})
	if scoped.ContextResolver == nil || base.ContextResolver != nil {
		t.Fatal("WithRuntime must not mutate the original")
	}
	// Shared registry still routes.
	call := Invoke(bg(), scoped, opTestInterface(), NewOperationSignature[any, any]("ping"))
	if _, err := Single(shortCtx(t), call.Outputs()); err != nil {
		t.Fatal(err)
	}
	_ = called
}

func TestRedactContext(t *testing.T) {
	red := RedactContext(map[string]any{
		"bearerToken": "secret",
		"basic":       map[string]any{"username": "u", "password": "pw"},
		"plain":       "visible",
	})
	if red["bearerToken"] != "[REDACTED]" {
		t.Fatalf("bearer not redacted: %v", red)
	}
	if red["basic"].(map[string]any)["password"] != "[REDACTED]" {
		t.Fatalf("password not redacted: %v", red)
	}
	if red["basic"].(map[string]any)["username"] != "u" || red["plain"] != "visible" {
		t.Fatalf("non-secrets must survive: %v", red)
	}
}

func TestNormalizeContextKey(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1/users":  "api.example.com",
		"ws://api.example.com:8080/stream":  "api.example.com:8080",
		"grpc://localhost:50051/svc":        "localhost:50051",
		"localhost:50051":                   "localhost:50051",
		"":                                  "",
		" https://spaced.example.com/path ": "spaced.example.com",
	}
	for in, want := range cases {
		if got := NormalizeContextKey(in); got != want {
			t.Fatalf("NormalizeContextKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeContextKey_ElidesExplicitDefaultPort pins the rule that an
// explicit port matching the scheme's default is stripped so a key written
// with the default port and one written without it collide — the origin is
// the same, so credentials stored under one must be found via the other. A
// non-default port, and any string with no scheme (a format-defined address
// like gRPC's bare "host:port"), are returned unchanged. IPv6 hosts are
// covered so the bracketed form isn't corrupted by the suffix-strip.
func TestNormalizeContextKey_ElidesExplicitDefaultPort(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com:443": "api.example.com",
		"http://x:80":                 "x",
		"wss://x:443":                 "x",
		"ws://x:80":                   "x",
		"https://x:8443":              "x:8443",       // non-default port kept
		"10.0.0.1:443":                "10.0.0.1:443", // no scheme: unchanged
		"host:50051":                  "host:50051",   // no scheme: unchanged
		"https://[::1]:443":           "[::1]",        // IPv6 default port elided
		"https://[::1]:8443":          "[::1]:8443",   // IPv6 non-default port kept
		"https://[::1]":               "[::1]",        // IPv6 no port, unaffected
	}
	for in, want := range cases {
		if got := NormalizeContextKey(in); got != want {
			t.Errorf("NormalizeContextKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeContextKey_DefaultPortEquivalence pins the actual bug: a URL
// with an explicit default port and the same URL without one must produce
// the IDENTICAL key, so credentials stored via one binding source are found
// via another that writes the port explicitly (or vice versa).
func TestNormalizeContextKey_DefaultPortEquivalence(t *testing.T) {
	pairs := [][2]string{
		{"https://api.example.com:443", "https://api.example.com"},
		{"http://x:80", "http://x"},
		{"wss://x:443", "wss://x"},
		{"ws://x:80", "ws://x"},
	}
	for _, p := range pairs {
		a, b := NormalizeContextKey(p[0]), NormalizeContextKey(p[1])
		if a != b {
			t.Errorf("NormalizeContextKey(%q) = %q != NormalizeContextKey(%q) = %q, want equal", p[0], a, p[1], b)
		}
	}
}

// TestNormalizeEndpoint_ElidesExplicitDefaultPort mirrors the
// NormalizeContextKey case through NormalizeEndpoint — the actual runtime
// path StoreContextResolver uses — since NormalizeEndpoint parses the URL
// first and must carry the scheme through to the elision logic rather than
// dropping it (dropping it would silently disable elision for every URL).
func TestNormalizeEndpoint_ElidesExplicitDefaultPort(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com:443/v1": "api.example.com",
		"https://api.example.com/v1":     "api.example.com",
		"http://x:80":                    "x",
		"wss://x:443/stream":             "x",
		"https://x:8443":                 "x:8443",
		"10.0.0.1:443":                   "10.0.0.1:443",
		"host:50051":                     "host:50051",
	}
	for in, want := range cases {
		if got := NormalizeEndpoint(in); got != want {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStoreContextResolver_DefaultPortMissFixed is the integration-flavored
// proof: a credential stored under a target written WITHOUT the scheme's
// default port must be found when the challenge target is written WITH it
// (and vice versa), through the actual store-backed resolution path — before
// this fix the two forms produced different store keys and the credential
// was silently missed.
func TestStoreContextResolver_DefaultPortMissFixed(t *testing.T) {
	ctx := context.Background()

	t.Run("stored without port, challenged with explicit default port", func(t *testing.T) {
		// A different binding source once wrote the credential keyed off a
		// target with no explicit port.
		store := testStore{
			NormalizeEndpoint("https://api.example.com/v1/users"): {"bearerToken": "stored-tok"},
		}
		resolve := StoreContextResolver(store)
		details := &ContextRequiredDetails{
			Target: "https://api.example.com:443/v1/users",
			Alternatives: []ContextAlternative{
				{Requirements: []ContextRequirement{{Type: "auth.bearer"}}},
			},
		}
		got, err := resolve(ctx, details)
		if err != nil || ContextBearerToken(got) != "stored-tok" {
			t.Fatalf("got %v err=%v, want the stored credential resolved despite the explicit default port", got, err)
		}
	})

	t.Run("stored with explicit default port, challenged without", func(t *testing.T) {
		// The reverse: the credential was written keyed off a target that
		// spelled out the default port explicitly.
		store := testStore{
			NormalizeEndpoint("https://api.example.com:443/v1/users"): {"bearerToken": "stored-tok"},
		}
		resolve := StoreContextResolver(store)
		details := &ContextRequiredDetails{
			Target: "https://api.example.com/v1/users",
			Alternatives: []ContextAlternative{
				{Requirements: []ContextRequirement{{Type: "auth.bearer"}}},
			},
		}
		got, err := resolve(ctx, details)
		if err != nil || ContextBearerToken(got) != "stored-tok" {
			t.Fatalf("got %v err=%v, want the stored credential resolved", got, err)
		}
	})
}
