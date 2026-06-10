package openbindings

// Binding execution context (BEC) tests: the context store, the well-known
// context helpers, and the store-backed CONTEXT_REQUIRED resolver that
// composes the binding-invoker and context-store roles.

import (
	"context"
	"testing"
)

var bearerOrAPIKey = &ContextRequiredDetails{
	Key: "api.example.com",
	Alternatives: []ContextAlternative{
		{Requirements: []ContextRequirement{{Type: "auth.bearer"}}},
		{Requirements: []ContextRequirement{{Type: "auth.apiKey"}}},
	},
}

func TestMemoryStoreBasics(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	if v, err := store.Get(ctx, "missing"); err != nil || v != nil {
		t.Fatalf("expected nil for missing key, got %v err=%v", v, err)
	}

	if err := store.Set(ctx, "k1", map[string]any{"bearerToken": "abc"}); err != nil {
		t.Fatal(err)
	}
	v, err := store.Get(ctx, "k1")
	if err != nil || v["bearerToken"] != "abc" {
		t.Fatalf("got %v err=%v", v, err)
	}

	if err := store.Delete(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := store.Get(ctx, "k1"); v != nil {
		t.Fatalf("expected delete, got %v", v)
	}
}

func TestMemoryStoreDeepCopyIsolation(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	in := map[string]any{"nested": map[string]any{"value": "original"}}
	if err := store.Set(ctx, "k1", in); err != nil {
		t.Fatal(err)
	}
	in["nested"].(map[string]any)["value"] = "mutated-input"

	got, _ := store.Get(ctx, "k1")
	got["nested"].(map[string]any)["value"] = "mutated-output"

	fresh, _ := store.Get(ctx, "k1")
	if fresh["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("store not isolated: %v", fresh)
	}
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
			Key: "k",
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
			Key:          "k",
			Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.oauth2"}}}},
		}
		if !ContextSatisfies(map[string]any{"accessToken": "a"}, details) {
			t.Fatal("auth.oauth2 should map to accessToken")
		}
	})
}

func TestStoreContextResolver(t *testing.T) {
	ctx := context.Background()

	t.Run("returns stored context that satisfies", func(t *testing.T) {
		store := NewMemoryStore()
		_ = store.Set(ctx, "api.example.com", map[string]any{"bearerToken": "stored-tok"})
		resolve := StoreContextResolver(store)
		got, err := resolve(ctx, bearerOrAPIKey)
		if err != nil || ContextBearerToken(got) != "stored-tok" {
			t.Fatalf("got %v err=%v", got, err)
		}
	})

	t.Run("declines when nothing stored", func(t *testing.T) {
		resolve := StoreContextResolver(NewMemoryStore())
		got, err := resolve(ctx, bearerOrAPIKey)
		if err != nil || got != nil {
			t.Fatalf("expected decline, got %v err=%v", got, err)
		}
	})

	t.Run("declines when stored context is insufficient", func(t *testing.T) {
		store := NewMemoryStore()
		_ = store.Set(ctx, "api.example.com", map[string]any{"unrelated": "field"})
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
	store := NewMemoryStore()
	_ = store.Set(context.Background(), "api.example.com", map[string]any{"bearerToken": "stored"})

	mock := &mockBindingInvoker{opts: mockOpts{requireBearer: true}}
	op := newOpInvoker(mock, StoreContextResolver(store))
	call := op.Invoke(bg(), &OperationInvocationArgs{Interface: opTestInterface(), Operation: "getUser"})
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
	call := scoped.Invoke(bg(), &OperationInvocationArgs{Interface: opTestInterface(), Operation: "ping"})
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
