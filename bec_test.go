package openbindings

// Binding execution context (BEC) tests: the context store, the well-known
// context helpers, and the store-backed CONTEXT_REQUIRED resolver that
// composes the binding-invoker and context-store roles.

import (
	"context"
	"encoding/json"
	"strings"
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
		{Requirements: []ContextRequirement{{Type: "auth.bearer", Durable: testBoolPointer(true)}}},
		{Requirements: []ContextRequirement{{Type: "auth.apiKey", Durable: testBoolPointer(true)}}},
	},
}

func testBoolPointer(value bool) *bool { return &value }

func TestContextSatisfies(t *testing.T) {
	t.Run("any one alternative suffices (disjunctive)", func(t *testing.T) {
		if !ContextSatisfies(map[string]any{"bearerToken": "t"}, bearerOrAPIKey) {
			t.Fatal("bearer should satisfy")
		}
		if !ContextSatisfies(map[string]any{"apiKey": "k"}, bearerOrAPIKey) {
			t.Fatal("apiKey should satisfy")
		}
	})

	t.Run("flat credential cannot satisfy ambiguous named OR alternatives", func(t *testing.T) {
		details := &ContextRequiredDetails{
			Target: "api.example.com",
			Alternatives: []ContextAlternative{
				{Requirements: []ContextRequirement{{Type: "auth.bearer", Name: "schemeA"}}},
				{Requirements: []ContextRequirement{{Type: "auth.bearer", Name: "schemeB"}}},
			},
		}
		if ContextSatisfies(map[string]any{"bearerToken": "ambiguous"}, details) {
			t.Fatal("flat credential must not select one of multiple named schemes")
		}
		if !ContextSatisfies(map[string]any{"credentials": map[string]any{"schemeB": "specific"}}, details) {
			t.Fatal("scheme-scoped credential should satisfy its alternative")
		}
	})

	t.Run("all requirements within an alternative (conjunctive)", func(t *testing.T) {
		details := &ContextRequiredDetails{
			Target: "k",
			Alternatives: []ContextAlternative{
				{Requirements: []ContextRequirement{
					{Type: "auth.basic"},
					{Type: "config.value", Extra: map[string]any{"point": "server", "path": "/url"}},
				}},
			},
		}
		if ContextSatisfies(map[string]any{"basic": map[string]any{"username": "u"}}, details) {
			t.Fatal("partial conjunction must not satisfy")
		}
		ctx := map[string]any{
			"basic":         map[string]any{"username": "u", "password": "p"},
			"configuration": map[string]any{"server": map[string]any{"url": "https://api.example.com"}},
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

	for _, tc := range []struct {
		name    string
		durable *bool
	}{
		{name: "durability omitted"},
		{name: "durability false", durable: testBoolPointer(false)},
	} {
		t.Run("does not reuse stored context when "+tc.name, func(t *testing.T) {
			store := testStore{"api.example.com": {"bearerToken": "stored-tok"}}
			resolve := StoreContextResolver(store)
			details := &ContextRequiredDetails{
				Target: "https://api.example.com/v1",
				Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{
					{Type: "auth.bearer", Durable: tc.durable},
				}}},
			}
			got, err := resolve(ctx, details)
			if err != nil || got != nil {
				t.Fatalf("expected decline, got %v err=%v", got, err)
			}
		})
	}

	t.Run("selects only a wholly durable alternative", func(t *testing.T) {
		store := testStore{"api.example.com": {
			"bearerToken": "must-not-reuse",
			"apiKey":      "reusable",
		}}
		resolve := StoreContextResolver(store)
		details := &ContextRequiredDetails{
			Target: "https://api.example.com/v1",
			Alternatives: []ContextAlternative{
				{Requirements: []ContextRequirement{{Type: "auth.bearer", Durable: testBoolPointer(false)}}},
				{Requirements: []ContextRequirement{{Type: "auth.apiKey", Durable: testBoolPointer(true)}}},
			},
		}
		got, err := resolve(ctx, details)
		if err != nil || ContextAPIKey(got) != "reusable" || ContextBearerToken(got) != "" {
			t.Fatalf("got %v err=%v", got, err)
		}
	})

	t.Run("does not partially reuse a mixed durability AND-set", func(t *testing.T) {
		store := testStore{"api.example.com": {
			"bearerToken":   "stored",
			"configuration": map[string]any{"approval": map[string]any{"value": "yes"}},
		}}
		resolve := StoreContextResolver(store)
		details := &ContextRequiredDetails{
			Target: "https://api.example.com/v1",
			Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{
				{Type: "auth.bearer", Durable: testBoolPointer(true)},
				{Type: "config.value", Durable: testBoolPointer(false), Extra: map[string]any{"point": "approval", "path": ""}},
			}}},
		}
		got, err := resolve(ctx, details)
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
				{Requirements: []ContextRequirement{{Type: "auth.apiKey", Name: "serviceA", Durable: testBoolPointer(true)}}},
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
		"apiKey":      "flat-secret",
		"apiKeys":     map[string]any{"stripe": "sk_live_SECRET"},
		"basic":       map[string]any{"username": "u", "password": "pw"},
		"plain":       "visible",
	})
	if red["bearerToken"] != "[REDACTED]" {
		t.Fatalf("bearer not redacted: %v", red)
	}
	if red["apiKey"] != "[REDACTED]" {
		t.Fatalf("flat apiKey not redacted: %v", red)
	}
	if red["apiKeys"].(map[string]any)["stripe"] != "[REDACTED]" {
		t.Fatalf("scheme-scoped apiKeys[stripe] not redacted: %v", red)
	}
	if red["basic"].(map[string]any)["password"] != "[REDACTED]" {
		t.Fatalf("password not redacted: %v", red)
	}
	if red["basic"].(map[string]any)["username"] != "u" || red["plain"] != "visible" {
		t.Fatalf("unclassified values and structural identifiers must survive this generic helper: %v", red)
	}
}

// TestRedactContext_CoversEveryCredentialField is the drift guard whose
// absence let the TS apiKeys leak ship: for EVERY field the credential
// redaction registry (credentialFieldNames)
// classifies as secret, place a distinctive sentinel in that field's proper
// shape, redact, serialize, and assert the sentinel appears NOWHERE. Adding a
// credential family to the registry without teaching RedactContext its shape
// fails this automatically. Mirrored by the TS redactContext.test.ts.
func TestRedactContext_CoversEveryCredentialField(t *testing.T) {
	// The secret's proper carriage per field; unlisted fields are flat.
	shaped := func(field, sentinel string) any {
		switch field {
		case "basic":
			return map[string]any{"username": "visible-user", "password": sentinel}
		case "apiKeys":
			return map[string]any{"stripe": sentinel, "twilio": sentinel + "-b"}
		default:
			return sentinel
		}
	}
	for field := range credentialFieldNames {
		sentinel := "SENTINEL_" + field + "_9f3ac1"
		ctx := map[string]any{
			field:      shaped(field, sentinel),
			"plainCfg": "keepme",
		}
		out, err := json.Marshal(RedactContext(ctx))
		if err != nil {
			t.Fatalf("marshal redacted %s context: %v", field, err)
		}
		if strings.Contains(string(out), sentinel) {
			t.Errorf("RedactContext leaked a %q secret: sentinel survived in %s", field, out)
		}
		if !strings.Contains(string(out), "keepme") {
			t.Errorf("RedactContext dropped an unclassified pass-through value for %q: %s", field, out)
		}
	}
	// Structural identifiers of nested credential fields survive (scheme
	// names, basic username); known secret values are scrubbed.
	red := RedactContext(map[string]any{
		"apiKeys": map[string]any{"stripe": "sk"},
		"basic":   map[string]any{"username": "alice", "password": "pw"},
	})
	if _, ok := red["apiKeys"].(map[string]any)["stripe"]; !ok {
		t.Errorf("apiKeys scheme name 'stripe' must survive redaction: %v", red)
	}
	if red["basic"].(map[string]any)["username"] != "alice" {
		t.Errorf("basic username must survive redaction: %v", red)
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

// TestNormalizeContextKey_StripsUserinfoAndFoldsCase pins the keying rule the
// binding-invoker README owns: normalize to the host — lowercased, userinfo
// excluded (RFC 3986). A password in userinfo must never ride into a store
// key (the one surface RedactContext cannot reach), and a case-variant host
// must derive the same key (DNS is case-insensitive) so a credential is not
// silently fragmented across casings.
func TestNormalizeContextKey_StripsUserinfoAndFoldsCase(t *testing.T) {
	cases := map[string]string{
		"https://API.example.com/v1/users":         "api.example.com",
		"https://alice:hunter2@API.example.com/v1": "api.example.com",
		"https://alice:hunter2@api.example.com":    "api.example.com",
		"https://u:p@Host.Example.COM:8443/x":      "host.example.com:8443",
		"https://user@API.example.com:443":         "api.example.com",
		"https://u:p@[2001:DB8::1]:8080":           "[2001:db8::1]:8080",
	}
	for in, want := range cases {
		got := NormalizeContextKey(in)
		if got != want {
			t.Errorf("NormalizeContextKey(%q) = %q, want %q", in, got, want)
		}
		// A userinfo password must never survive into the key.
		if strings.Contains(got, "hunter2") || strings.Contains(got, ":p@") {
			t.Errorf("NormalizeContextKey(%q) leaked userinfo into the key: %q", in, got)
		}
	}
}

// TestNormalizeKey_WriteReadAgree pins that the write helper
// (NormalizeContextKey) and the read/resolver helper (NormalizeEndpoint)
// derive the IDENTICAL key for the same input across mixed-case hosts,
// userinfo-bearing URLs, and port variants — otherwise a credential written
// one way is silently never resolved. The expected VALUES are also the
// cross-SDK contract, pinned identically in the TS normalizeContextKey tests.
func TestNormalizeKey_WriteReadAgree(t *testing.T) {
	// input → the one key both normalizers must produce (the cross-SDK value).
	cases := map[string]string{
		"https://API.example.com/v1":               "api.example.com",
		"https://alice:hunter2@API.example.com/v1": "api.example.com",
		"https://API.example.com:443":              "api.example.com",
		"https://API.example.com:8443/x":           "api.example.com:8443",
		"http://User@Host.EXAMPLE.com:80":          "host.example.com",
	}
	for in, want := range cases {
		k := NormalizeContextKey(in)
		e := NormalizeEndpoint(in)
		if k != want {
			t.Errorf("NormalizeContextKey(%q) = %q, want %q", in, k, want)
		}
		if e != want {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", in, e, want)
		}
		if k != e {
			t.Errorf("write/read key mismatch for %q: NormalizeContextKey=%q NormalizeEndpoint=%q", in, k, e)
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
				{Requirements: []ContextRequirement{{Type: "auth.bearer", Durable: testBoolPointer(true)}}},
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
				{Requirements: []ContextRequirement{{Type: "auth.bearer", Durable: testBoolPointer(true)}}},
			},
		}
		got, err := resolve(ctx, details)
		if err != nil || ContextBearerToken(got) != "stored-tok" {
			t.Fatalf("got %v err=%v, want the stored credential resolved", got, err)
		}
	})
}
