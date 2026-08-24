package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

func invocationSchema() *introspectionSchema {
	schema := minimalSchema()
	schema.SubscriptionType = &typeRef{Kind: "OBJECT", Name: "RootSubscription"}
	schema.Types = append(schema.Types, fullType{
		Kind: "OBJECT", Name: "RootSubscription",
		Fields: []field{{Name: "updates", Type: typeRef{Kind: "SCALAR", Name: "String"}}},
	})
	return schema
}

func pinnedInvocationSource(t *testing.T, location string) invoke.InvocationSource {
	t.Helper()
	content, err := json.Marshal(map[string]any{"data": map[string]any{"__schema": invocationSchema()}})
	if err != nil {
		t.Fatal(err)
	}
	return invoke.InvocationSource{BindingSpec: BindingSpec, Location: location, Content: content}
}

func collectInvocation(ctx context.Context, call invoke.Invocation[any, any], input any, inputPresent bool) ([]any, *invoke.InvocationError) {
	if inputPresent {
		_ = call.Write(ctx, input)
	}
	_ = call.Close()
	var outputs []any
	reader := call.Outputs()
	for {
		value, err := reader.Read(ctx)
		if errors.Is(err, io.EOF) {
			return outputs, nil
		}
		if err != nil {
			var invocationErr *invoke.InvocationError
			_ = errors.As(err, &invocationErr)
			return outputs, invocationErr
		}
		outputs = append(outputs, value)
	}
}

func graphqlContext(document any) map[string]any {
	return map[string]any{"configuration": map[string]any{"document": document}}
}

func TestHTTPInvocationPreservesDocumentVariablesAndPartialApplicationValue(t *testing.T) {
	requests := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests <- body
		w.Header().Set("Content-Type", "application/graphql-response+json; charset=utf-8")
		w.Header().Set("X-Request-ID", "req-1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   map[string]any{"viewer": nil},
			"errors": []any{map[string]any{"message": "resolver warning"}},
		})
	}))
	defer srv.Close()

	document := map[string]any{
		"source":        "query Viewer($id: ID!) { viewer(id: $id) }",
		"operationName": "Viewer",
	}
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:      pinnedInvocationSource(t, srv.URL),
		Selector:    "query/viewer",
		InputSchema: map[string]any{"type": "object"},
		Context:     graphqlContext(document),
	})
	input := map[string]any{"id": "u-1", "unused": 7, "_query": "ordinary variable"}
	outputs, invocationErr := collectInvocation(context.Background(), call, input, true)
	if len(outputs) != 1 || outputs[0] != nil || invocationErr == nil || invocationErr.Code != invoke.ErrCodeExecutionFailed {
		t.Fatalf("outputs = %#v, err = %v", outputs, invocationErr)
	}
	request := <-requests
	if request["query"] != document["source"] || request["operationName"] != "Viewer" {
		t.Fatalf("request = %#v", request)
	}
	variables := request["variables"].(map[string]any)
	if variables["_query"] != "ordinary variable" || variables["unused"] != float64(7) {
		t.Fatalf("variables were filtered: %#v", variables)
	}
}

func TestHTTPInvocationOmitsAbsentVariables(t *testing.T) {
	requests := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		requests <- body
		w.Header().Set("Content-Type", "application/graphql-response+json")
		_, _ = w.Write([]byte(`{"data":{"health":"ok"}}`))
	}))
	defer srv.Close()
	call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source:   pinnedInvocationSource(t, srv.URL),
		Selector: "query/health",
		Context:  graphqlContext("query { health }"),
	})
	outputs, invocationErr := collectInvocation(context.Background(), call, nil, false)
	if invocationErr != nil || len(outputs) != 1 {
		t.Fatalf("outputs = %#v, err = %v", outputs, invocationErr)
	}
	if outputs[0] != "ok" {
		t.Fatalf("output = %#v, want root application value", outputs[0])
	}
	if _, present := (<-requests)["variables"]; present {
		t.Fatal("absent input manufactured variables")
	}
}

func TestHTTPMediaClassification(t *testing.T) {
	for _, tc := range []struct {
		name, media string
	}{
		{"graphql response media", "application/graphql-response+json"},
		{"legacy json media", "application/json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.media)
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":[{"message":"request rejected"}]}`))
			}))
			defer srv.Close()
			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   pinnedInvocationSource(t, srv.URL),
				Selector: "query/viewer",
				Context:  graphqlContext("query { viewer }"),
			})
			outputs, invocationErr := collectInvocation(context.Background(), call, nil, false)
			if invocationErr == nil || len(outputs) != 0 {
				t.Fatalf("native error envelope must not become an operation output: outputs=%#v err=%v", outputs, invocationErr)
			}
		})
	}
}

func TestPreDispatchChallengesAndRefusalsHaveNoIO(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer srv.Close()
	invoker := NewInvoker()

	details, err := invoker.PrepareBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: pinnedInvocationSource(t, srv.URL), Selector: "query/viewer",
	})
	if err != nil || details == nil || details.Alternatives[0].Requirements[0].Extra["point"] != "document" {
		t.Fatalf("prepare = %#v, %v", details, err)
	}
	missing := invoker.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: pinnedInvocationSource(t, srv.URL), Selector: "query/viewer",
	})
	_, missingErr := collectInvocation(context.Background(), missing, nil, false)
	if missingErr == nil || missingErr.Code != invoke.ErrCodeContextRequired {
		t.Fatalf("missing document = %v", missingErr)
	}

	mismatch := invoker.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: pinnedInvocationSource(t, srv.URL), Selector: "query/viewer",
		Context: graphqlContext("query { health }"),
	})
	_, mismatchErr := collectInvocation(context.Background(), mismatch, nil, false)
	if mismatchErr == nil || mismatchErr.Code != invoke.ErrCodeSourceConfigError {
		t.Fatalf("mismatch = %v", mismatchErr)
	}

	collision := invoker.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: pinnedInvocationSource(t, srv.URL), Selector: "query/viewer",
		Context: map[string]any{"configuration": map[string]any{
			"document": "query { viewer }",
			"protocolFields": map[string]any{
				"httpHeaders": map[string]any{"content-TYPE": "text/plain"},
			},
		}},
	})
	_, collisionErr := collectInvocation(context.Background(), collision, nil, false)
	if collisionErr == nil || collisionErr.Code != invoke.ErrCodeSourceConfigError {
		t.Fatalf("collision = %v", collisionErr)
	}

	unnamedCredential := invoker.InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
		Source: pinnedInvocationSource(t, srv.URL), Selector: "query/viewer",
		Context: map[string]any{
			"bearerToken": "ambiguous",
			"configuration": map[string]any{
				"document": "query { viewer }",
			},
		},
	})
	_, credentialErr := collectInvocation(context.Background(), unnamedCredential, nil, false)
	if credentialErr == nil || credentialErr.Code != invoke.ErrCodeSourceConfigError {
		t.Fatalf("unnamed credential = %v", credentialErr)
	}
	if requests.Load() != 0 {
		t.Fatalf("pre-dispatch failures made %d requests", requests.Load())
	}
}
