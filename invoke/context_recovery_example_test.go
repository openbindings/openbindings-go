package invoke_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// invokeWithContextRecovery is application code for the README example, not
// an SDK retry API. This application permits one redo of a single-input call.
func invokeWithContextRecovery[I, O any](
	ctx context.Context,
	start func(map[string]any) invoke.Invocation[I, O],
	input I,
	given map[string]any,
	resolve invoke.ContextResolver,
) (O, error) {
	var zero O
	for attempt := 0; ; attempt++ {
		out, err := func() (O, error) {
			call := start(given)
			defer call.Cancel() // Cancel this attempt before resolving or returning.
			if err := call.Write(ctx, input); err != nil {
				ie := invoke.AsInvocationError(err)
				if ie.Code != invoke.ErrCodeInputClosed && ie.Code != invoke.ErrCodeInvocationClosed {
					return zero, err
				}
				// Closure alone is not the outcome; a challenge may follow it.
			}
			_ = call.Close() // This application supplies exactly one input.
			return invoke.Single(ctx, call.Outputs())
		}()
		if err == nil {
			return out, nil
		}
		details := invoke.ContextRequiredFrom(invoke.AsInvocationError(err))
		if details == nil || attempt == 1 {
			return zero, err
		}
		resolved, rerr := resolve(ctx, details)
		if rerr != nil {
			return zero, rerr
		}
		selected, ok := invoke.MatchContextAlternative(resolved, details)
		if !ok {
			return zero, err
		}
		scoped := invoke.ScopeContext(resolved, details)
		if len(scoped) == 0 || !invoke.ContextSatisfies(scoped, details) {
			return zero, err // The resolver declined or supplied no usable fields.
		}
		given = mergeExampleContext(given, scoped, details.Alternatives[selected])
	}
}

// Replace the values selected by a satisfied alternative, preserving siblings.
// scoped has already passed ScopeContext. Merging copies containers on changed
// paths; it neither mutates sources nor detaches all retained values.
func mergeExampleContext(given, scoped map[string]any, selected invoke.ContextAlternative) map[string]any {
	merged := make(map[string]any, len(given)+len(scoped))
	maps.Copy(merged, given)
	wholeFields := map[string]bool{}
	for _, req := range selected.Requirements {
		if req.Type != "config.value" && !strings.HasPrefix(req.Type, "auth.") {
			wholeFields[req.Type] = true
		}
	}
	// Retire previous representations of the selected credential. Otherwise
	// an old named credential or accessToken could shadow a fresh flat fallback.
	credentialFields := map[string][]string{
		"auth.bearer": {"bearerToken"},
		"auth.apiKey": {"apiKey"},
		"auth.basic":  {"basic"},
		"auth.oauth2": {"accessToken", "bearerToken", "refreshToken", "clientSecret"},
	}
	for _, req := range selected.Requirements {
		if !strings.HasPrefix(req.Type, "auth.") {
			continue
		}
		for _, key := range credentialFields[req.Type] {
			delete(merged, key)
		}
		if req.Name != "" {
			for _, field := range []string{"credentials", "apiKeys"} {
				if names, ok := merged[field].(map[string]any); ok {
					names = maps.Clone(names)
					delete(names, req.Name)
					merged[field] = names
				}
			}
		}
	}
	for key, value := range scoped {
		if key == "configuration" && !wholeFields[key] {
			continue // Update only the configuration paths named below.
		}
		if (key == "credentials" || key == "apiKeys") && !wholeFields[key] {
			names := map[string]any{}
			prior, _ := merged[key].(map[string]any)
			maps.Copy(names, prior)
			maps.Copy(names, value.(map[string]any))
			value = names // Each named credential is replaced as a whole value.
		}
		merged[key] = value
	}
	for _, req := range selected.Requirements {
		if req.Type != "config.value" {
			continue
		}
		path := []string{"configuration", req.Extra["point"].(string)}
		pointer := req.Extra["path"].(string)
		if pointer != "" {
			unescape := strings.NewReplacer("~1", "/", "~0", "~")
			for _, token := range strings.Split(pointer[1:], "/") {
				path = append(path, unescape.Replace(token))
			}
		}
		var value any = scoped
		for _, key := range path {
			value = value.(map[string]any)[key]
		}
		merged = replaceExamplePath(merged, path, value)
	}
	return merged
}

func replaceExamplePath(source map[string]any, path []string, value any) map[string]any {
	result := make(map[string]any, len(source)+1)
	maps.Copy(result, source)
	if len(path) == 1 {
		result[path[0]] = value // Objects and arrays are atomic at this boundary.
	} else {
		child, _ := source[path[0]].(map[string]any)
		result[path[0]] = replaceExamplePath(child, path[1:], value)
	}
	return result
}

func recoveryChallenge() *invoke.ContextRequiredDetails {
	return &invoke.ContextRequiredDetails{
		Target: "example-service",
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{
			{Type: "auth.bearer"},
		}}},
	}
}

// credentialEchoInvoker illustrates a binding that reports a live challenge.
type credentialEchoInvoker struct{ echoInvoker }

func (e credentialEchoInvoker) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	if invoke.ContextBearerToken(args.Context) != "example-token" {
		return invoke.NewErroredInvocation[any, any](invoke.NewContextRequiredError(recoveryChallenge()))
	}
	return e.echoInvoker.InvokeBinding(ctx, args)
}

func ExampleInvoke_contextRecovery() {
	ctx := context.Background()
	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Name:         openbindings.Present("Echo"),
		Operations:   map[string]openbindings.Operation{"echo": {}},
		Sources:      map[string]openbindings.Source{"echo": {BindingSpec: "echo@1.0", Location: openbindings.Present("mem://echo")}},
		Bindings:     map[string]openbindings.BindingEntry{"echo.main": {Operation: "echo", Source: "echo", Selector: openbindings.Present("echo")}},
	}
	opInv := invoke.NewOperationInvoker(credentialEchoInvoker{})
	start := func(given map[string]any) invoke.Invocation[any, any] {
		return invoke.Invoke(ctx, opInv, iface,
			invoke.NewOperationSignature[any, any]("echo"), invoke.WithContext(given))
	}
	resolve := func(context.Context, *invoke.ContextRequiredDetails) (map[string]any, error) {
		return map[string]any{"bearerToken": "example-token"}, nil
	}
	out, err := invokeWithContextRecovery(ctx, start, any("hello"), nil, resolve)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(out)
	// Output: map[echoed:hello]
}

// observeRecoveryCall lets tests place a challenge before or after Write and
// observe cleanup without changing the real invocation's queue/error behavior.
type observeRecoveryCall[I, O any] struct {
	invoke.Invocation[I, O]
	afterWrite func(error)
	cancelled  bool
}

func (c *observeRecoveryCall[I, O]) Write(ctx context.Context, input I) error {
	err := c.Invocation.Write(ctx, input)
	if c.afterWrite != nil {
		c.afterWrite(err)
	}
	return err
}

func (c *observeRecoveryCall[I, O]) Cancel() {
	c.cancelled = true
	c.Invocation.Cancel()
}

func TestContextRecoveryExampleChallengeTiming(t *testing.T) {
	for _, timing := range []string{"before-write", "after-write", "input-closed-before-challenge"} {
		t.Run(timing, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				given := map[string]any{"bearerToken": "expired", "application": "kept"}
				var calls []*observeRecoveryCall[any, any]
				var contexts []map[string]any
				var writeErr error
				start := func(values map[string]any) invoke.Invocation[any, any] {
					contexts = append(contexts, values)
					h := invoke.NewInvocationImpl[any, any](ctx)
					call := &observeRecoveryCall[any, any]{Invocation: h}
					calls = append(calls, call)
					if len(calls) == 1 {
						challenge := invoke.NewContextRequiredError(recoveryChallenge())
						if timing == "before-write" {
							h.FireError(challenge)
						} else if timing == "input-closed-before-challenge" {
							_ = h.CloseInput()
						}
						call.afterWrite = func(err error) {
							writeErr = err
							h.FireError(challenge)
						}
					} else {
						go func() {
							input, err := h.ReadInput(ctx)
							if err != nil {
								return
							}
							// Wait for caller EOF: the example must close its input.
							_, _ = h.ReadInput(ctx)
							_ = h.EmitOutput(input)
							h.CloseOutput()
						}()
					}
					return call
				}
				resolutions := 0
				resolve := func(_ context.Context, details *invoke.ContextRequiredDetails) (map[string]any, error) {
					resolutions++
					if !calls[0].cancelled || !reflect.DeepEqual(details, recoveryChallenge()) {
						t.Fatal("resolution began without retiring the attempt or preserving challenge details")
					}
					return map[string]any{"bearerToken": "fresh", "apiKey": "unrelated"}, nil
				}
				out, err := invokeWithContextRecovery(ctx, start, any("input"), given, resolve)
				synctest.Wait()
				if err != nil || out != "input" || len(calls) != 2 || resolutions != 1 {
					t.Fatalf("out=%v err=%v attempts=%d resolutions=%d", out, err, len(calls), resolutions)
				}
				wantWrite := ""
				if timing == "before-write" {
					wantWrite = invoke.ErrCodeContextRequired
				} else if timing == "input-closed-before-challenge" {
					wantWrite = invoke.ErrCodeInputClosed
				}
				if writeErr == nil && wantWrite != "" || writeErr != nil && invoke.AsInvocationError(writeErr).Code != wantWrite {
					t.Fatalf("wrong challenge timing: Write returned %v", writeErr)
				}
				wantContext := map[string]any{"bearerToken": "fresh", "application": "kept"}
				if !reflect.DeepEqual(contexts[1], wantContext) || given["bearerToken"] != "expired" {
					t.Fatalf("context lost, broadened or mutated: %v; original: %v", contexts, given)
				}
				for _, call := range calls {
					if !call.cancelled {
						t.Fatal("attempt was not retired")
					}
				}
			})
		})
	}
}

func TestContextRecoveryExampleStops(t *testing.T) {
	resolverErr := errors.New("resolver failed")
	for _, scenario := range []string{"invalid-input", "operation-error", "resolver-error", "resolver-declines", "unscoped-resolution", "repeated-challenge"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				var calls []*observeRecoveryCall[any, any]
				var input any = "input"
				wantCode, wantCalls, wantResolutions := invoke.ErrCodeContextRequired, 1, 1
				if scenario == "invalid-input" {
					input = make(chan int)
					wantCode, wantResolutions = invoke.ErrCodeTypeMismatch, 0
				} else if scenario == "operation-error" {
					wantCode, wantResolutions = invoke.ErrCodeExecutionFailed, 0
				} else if scenario == "repeated-challenge" {
					wantCalls = 2
				}
				start := func(map[string]any) invoke.Invocation[any, any] {
					h := invoke.NewInvocationImpl[any, any](ctx)
					if scenario == "operation-error" {
						h.FireError(invoke.NewInvocationError(invoke.ErrCodeExecutionFailed))
					} else if scenario != "invalid-input" {
						h.FireError(invoke.NewContextRequiredError(recoveryChallenge()))
					}
					call := &observeRecoveryCall[any, any]{Invocation: h}
					calls = append(calls, call)
					return call
				}
				resolutions := 0
				resolve := func(context.Context, *invoke.ContextRequiredDetails) (map[string]any, error) {
					resolutions++
					switch scenario {
					case "resolver-error":
						return nil, resolverErr
					case "resolver-declines":
						return nil, nil
					case "unscoped-resolution":
						return map[string]any{"apiKey": "unrelated"}, nil
					default:
						return map[string]any{"bearerToken": "fresh"}, nil
					}
				}
				_, err := invokeWithContextRecovery(ctx, start, input, nil, resolve)
				synctest.Wait()
				if scenario == "resolver-error" {
					if !errors.Is(err, resolverErr) {
						t.Fatalf("resolver error lost: %v", err)
					}
				} else if err == nil || invoke.AsInvocationError(err).Code != wantCode {
					t.Fatalf("unexpected outcome: %v", err)
				}
				if len(calls) != wantCalls || resolutions != wantResolutions {
					t.Fatalf("attempts=%d resolutions=%d", len(calls), resolutions)
				}
				for _, call := range calls {
					if !call.cancelled {
						t.Fatal("attempt was not retired")
					}
				}
			})
		})
	}
}

func TestContextRecoveryExamplePreservesNestedContext(t *testing.T) {
	given := map[string]any{
		"credentials": map[string]any{"first": "kept", "second": "expired"},
		"configuration": map[string]any{
			"decode": "kept",
			"server": map[string]any{"variables": map[string]any{"region": "old", "tenant": "kept"}},
		},
	}
	resolved := map[string]any{
		"credentials": map[string]any{"second": "fresh", "third": "unrelated"},
		"configuration": map[string]any{
			"server": map[string]any{"variables": map[string]any{"region": "new", "secret": "unrelated"}},
		},
	}
	details := &invoke.ContextRequiredDetails{
		Target: "example-service",
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{
			{Type: "auth.apiKey", Name: "second"},
			invoke.NewConfigValueRequirement("server", "/variables/region", "", nil, nil),
		}}},
	}
	merged := mergeExampleContext(given, invoke.ScopeContext(resolved, details), details.Alternatives[0])
	want := map[string]any{
		"credentials": map[string]any{"first": "kept", "second": "fresh"},
		"configuration": map[string]any{
			"decode": "kept",
			"server": map[string]any{"variables": map[string]any{"region": "new", "tenant": "kept"}},
		},
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("nested context lost or broadened: %v", merged)
	}
	if given["credentials"].(map[string]any)["second"] != "expired" ||
		given["configuration"].(map[string]any)["server"].(map[string]any)["variables"].(map[string]any)["region"] != "old" ||
		resolved["credentials"].(map[string]any)["first"] != nil {
		t.Fatal("merge mutated its sources")
	}
}

func TestContextRecoveryExampleCancelsAfterConversionFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		h := invoke.NewInvocationImpl[any, any](ctx)
		// Conversion fails while the producer is still live, so Single alone
		// does not retire this invocation.
		_ = h.EmitOutput("not-an-integer")
		call := &observeRecoveryCall[any, int]{Invocation: invoke.NewTypedInvocation[any, int](h)}
		start := func(map[string]any) invoke.Invocation[any, int] { return call }
		resolve := func(context.Context, *invoke.ContextRequiredDetails) (map[string]any, error) {
			t.Fatal("conversion failure triggered context resolution")
			return nil, nil
		}
		_, err := invokeWithContextRecovery(ctx, start, any("input"), nil, resolve)
		var conversion *invoke.ValueConversionError
		if !errors.As(err, &conversion) || !call.cancelled {
			t.Fatalf("conversion failure lost or attempt left live: %v", err)
		}
		select {
		case <-h.Done():
		default:
			t.Fatal("underlying invocation was not cancelled")
		}
	})
}

func TestContextRecoveryExampleReplacesRequestedObject(t *testing.T) {
	for _, pointer := range []string{"", "/nested", "/slash~1key/~0name"} {
		t.Run(pointer, func(t *testing.T) {
			path := []string{"configuration", "connection"}
			if pointer != "" {
				for _, token := range strings.Split(pointer[1:], "/") {
					path = append(path, strings.NewReplacer("~1", "/", "~0", "~").Replace(token))
				}
			}
			given := replaceExamplePath(map[string]any{"application": "kept"}, path, map[string]any{"obsolete": true})
			given["configuration"].(map[string]any)["other"] = "kept"
			resolved := replaceExamplePath(nil, path, map[string]any{"mode": "new"})
			details := &invoke.ContextRequiredDetails{Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{
				invoke.NewConfigValueRequirement("connection", pointer, "", map[string]any{
					"type": "object", "properties": map[string]any{"mode": map[string]any{"const": "new"}},
					"required": []any{"mode"}, "additionalProperties": false,
				}, nil),
			}}}}
			scoped := invoke.ScopeContext(resolved, details)
			if !invoke.ContextSatisfies(scoped, details) {
				t.Fatal("fixture resolution must satisfy the challenge")
			}
			merged := mergeExampleContext(given, scoped, details.Alternatives[0])
			if !invoke.ContextSatisfies(merged, details) || merged["application"] != "kept" || merged["configuration"].(map[string]any)["other"] != "kept" {
				t.Fatalf("replacement invalid or unrelated context lost: %v", merged)
			}
			var original any = given
			for _, key := range path {
				original = original.(map[string]any)[key]
			}
			if !reflect.DeepEqual(original, map[string]any{"obsolete": true}) {
				t.Fatal("original object was mutated")
			}
		})
	}
}

func TestContextRecoveryExampleReplacesCredentialRepresentation(t *testing.T) {
	for _, tc := range []struct {
		kind, field string
		old, fresh  any
	}{
		{"auth.bearer", "bearerToken", "expired", "fresh"},
		{"auth.basic", "basic", map[string]any{"username": "old", "password": "old", "obsolete": true}, map[string]any{"username": "new", "password": "new"}},
		{"auth.oauth2", "bearerToken", map[string]any{"accessToken": "expired", "refreshToken": "old"}, "fresh"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			details := &invoke.ContextRequiredDetails{Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{{Type: tc.kind, Name: "service"}}}}}
			given := map[string]any{"credentials": map[string]any{"service": tc.old, "other": "kept"}, "accessToken": "expired", "application": "kept"}
			resolved := map[string]any{tc.field: tc.fresh}
			scoped := invoke.ScopeContext(resolved, details)
			merged := mergeExampleContext(given, scoped, details.Alternatives[0])
			if !invoke.ContextSatisfies(merged, details) {
				t.Fatalf("replacement missing: %v", merged)
			}
			if !reflect.DeepEqual(merged[tc.field], tc.fresh) || merged["credentials"].(map[string]any)["service"] != nil || merged["credentials"].(map[string]any)["other"] != "kept" || merged["application"] != "kept" {
				t.Fatalf("old credential shadowed resolution or sibling lost: %v", merged)
			}
			if tc.kind == "auth.oauth2" && merged["accessToken"] != nil {
				t.Fatal("old accessToken shadows new bearerToken")
			}
			if !reflect.DeepEqual(given["credentials"].(map[string]any)["service"], tc.old) {
				t.Fatal("merge modified original credentials")
			}
		})
	}
}

func TestContextRecoveryExampleReadsCompletedOutput(t *testing.T) {
	h := invoke.NewInvocationImpl[any, any](t.Context())
	_ = h.EmitOutput("completed")
	h.CloseOutput()
	call := &observeRecoveryCall[any, any]{Invocation: h, afterWrite: func(err error) {
		if err == nil || invoke.AsInvocationError(err).Code != invoke.ErrCodeInvocationClosed {
			t.Fatalf("expected completed-before-write timing, got %v", err)
		}
	}}
	start := func(map[string]any) invoke.Invocation[any, any] { return call }
	resolve := func(context.Context, *invoke.ContextRequiredDetails) (map[string]any, error) {
		t.Fatal("completed invocation triggered resolution")
		return nil, nil
	}
	out, err := invokeWithContextRecovery(t.Context(), start, any("input"), nil, resolve)
	if err != nil || out != "completed" || !call.cancelled {
		t.Fatalf("completion lost or attempt not retired: %v, %v", out, err)
	}
}

func TestContextRecoveryExampleReplacesWholeNamedCredential(t *testing.T) {
	details := &invoke.ContextRequiredDetails{Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{{Type: "auth.oauth2", Name: "service"}}}}}
	given := map[string]any{
		"credentials":  map[string]any{"service": map[string]any{"accessToken": "old", "refreshToken": "obsolete"}, "other": "kept"},
		"refreshToken": "obsolete-flat",
	}
	resolved := map[string]any{"credentials": map[string]any{"service": map[string]any{"accessToken": "fresh"}}}
	merged := mergeExampleContext(given, invoke.ScopeContext(resolved, details), details.Alternatives[0])
	want := map[string]any{"credentials": map[string]any{"service": map[string]any{"accessToken": "fresh"}, "other": "kept"}}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("credential retained old members or aliases: %v", merged)
	}
	if given["credentials"].(map[string]any)["service"].(map[string]any)["refreshToken"] != "obsolete" {
		t.Fatal("merge modified the original credential")
	}
}

func TestContextRecoveryExampleRetainsSelectedAlternative(t *testing.T) {
	details := &invoke.ContextRequiredDetails{Target: "example-service", Alternatives: []invoke.ContextAlternative{
		{Requirements: []invoke.ContextRequirement{{Type: "auth.bearer", Name: "a"}}},
		{Requirements: []invoke.ContextRequirement{{Type: "auth.bearer", Name: "b"}}},
		{Requirements: []invoke.ContextRequirement{{Type: "auth.oauth2", Name: "oauth"}}},
	}}
	given := map[string]any{"credentials": map[string]any{"a": "kept", "oauth": map[string]any{"accessToken": "old-named"}}, "accessToken": "old-flat"}
	attempts := 0
	start := func(values map[string]any) invoke.Invocation[any, any] {
		attempts++
		h := invoke.NewInvocationImpl[any, any](t.Context())
		if attempts == 1 {
			h.FireError(invoke.NewContextRequiredError(details))
		} else {
			if invoke.ContextNamedCredential(values, "a") != "kept" || invoke.ContextNamedCredential(values, "oauth") != nil || values["accessToken"] != nil || values["bearerToken"] != "fresh" {
				t.Fatalf("selected credential stale or unrelated credential lost: %v", values)
			}
			_ = h.EmitOutput("ok")
			h.CloseOutput()
		}
		return h
	}
	resolve := func(context.Context, *invoke.ContextRequiredDetails) (map[string]any, error) {
		return map[string]any{"bearerToken": "fresh"}, nil
	}
	out, err := invokeWithContextRecovery(t.Context(), start, any("input"), given, resolve)
	if err != nil || out != "ok" || attempts != 2 {
		t.Fatalf("recovery failed: %v, %v, attempts=%d", out, err, attempts)
	}
}
