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
