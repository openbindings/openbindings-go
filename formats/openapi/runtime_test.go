package openapi

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestRuntimeInvokesArtifactWithoutOBI(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got, want := req.URL.String(), "https://api.example.test/v1/users/42"; got != want {
			t.Fatalf("request URL = %q, want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"42"}`)),
			Request:    req,
		}, nil
	})}
	runtime := NewRuntimeWithClient(client)
	call := runtime.Invoke(context.Background(), &RuntimeInvocationArgs{
		Source: RuntimeSource{Content: openbindings.TextContent(`{
			"openapi":"3.1.0",
			"info":{"title":"Standalone runtime","version":"1"},
			"servers":[{"url":"https://api.example.test/v1"}],
			"paths":{"/users/{id}":{"get":{
				"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
				"responses":{"200":{"description":"user","content":{"application/json":{"schema":{"type":"object"}}}}}
			}}}
		}`)},
		Ref: "#/paths/~1users~1{id}/get",
	})

	if err := call.Write(context.Background(), map[string]any{"id": "42"}); err != nil {
		t.Fatal(err)
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := openbindings.Single(context.Background(), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if got := output.(map[string]any)["id"]; got != "42" {
		t.Fatalf("output id = %#v, want 42", got)
	}
}

func TestRuntimePrepareDerivesArtifactPrerequisites(t *testing.T) {
	runtime := NewRuntime()
	details, err := runtime.Prepare(context.Background(), &RuntimeInvocationArgs{
		Source: RuntimeSource{Content: openbindings.TextContent(`{
			"openapi":"3.1.0",
			"info":{"title":"Standalone runtime","version":"1"},
			"servers":[{"url":"https://api.example.test/v1"}],
			"paths":{"/users":{"get":{"security":[{"token":[]}],"responses":{"204":{"description":"done"}}}}},
			"components":{"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}}
		}`)},
		Ref: "#/paths/~1users/get",
	})
	if err != nil {
		t.Fatal(err)
	}
	if details == nil || details.Target != "https://api.example.test/v1" {
		t.Fatalf("details = %#v", details)
	}
	if len(details.Alternatives) != 1 || len(details.Alternatives[0].Requirements) != 1 || details.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Fatalf("requirements = %#v", details.Alternatives)
	}
}

func TestRuntimeAppliesResolvedBearerContext(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got, want := req.Header.Get("Authorization"), "Bearer resolved-token"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	runtime := NewRuntimeWithClient(client)
	call := runtime.Invoke(context.Background(), &RuntimeInvocationArgs{
		Source: RuntimeSource{Content: openbindings.TextContent(`{
			"openapi":"3.1.0",
			"info":{"title":"Secured runtime","version":"1"},
			"servers":[{"url":"https://api.example.test"}],
			"paths":{"/users":{"get":{"security":[{"token":[]}],"responses":{"204":{"description":"done"}}}}},
			"components":{"securitySchemes":{"token":{"type":"http","scheme":"bearer"}}}
		}`)},
		Ref:     "#/paths/~1users/get",
		Context: map[string]any{"bearerToken": "resolved-token"},
	})
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := call.Outputs().Read(context.Background()); err != io.EOF {
		t.Fatalf("output terminal = %v, want EOF", err)
	}
}

func TestRuntimeAppliesInstalledArtifactSecurityHandler(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got, want := req.Header.Get("Authorization"), "Digest adapter-proof"; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     "204 No Content",
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	runtime := NewRuntimeWithOptions(RuntimeOptions{
		HTTPClient: client,
		SecurityHandlers: map[string]SecurityHandler{
			"digestAuth": func(request *http.Request, context SecurityHandlerContext) error {
				if context.SchemeName != "digestAuth" {
					t.Fatalf("scheme name = %q", context.SchemeName)
				}
				request.Header.Set("Authorization", "Digest adapter-proof")
				return nil
			},
		},
	})
	args := &RuntimeInvocationArgs{
		Source: RuntimeSource{Content: openbindings.TextContent(`{
			"openapi":"3.1.0",
			"info":{"title":"Secured runtime","version":"1"},
			"servers":[{"url":"https://api.example.test"}],
			"paths":{"/users":{"get":{"security":[{"digestAuth":[]}],"responses":{"204":{"description":"done"}}}}},
			"components":{"securitySchemes":{"digestAuth":{"type":"http","scheme":"digest"}}}
		}`)},
		Ref: "#/paths/~1users/get",
	}
	details, err := runtime.Prepare(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if details != nil {
		t.Fatalf("installed handler left prerequisites: %#v", details)
	}
	call := runtime.Invoke(context.Background(), args)
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := call.Outputs().Read(context.Background()); err != io.EOF {
		t.Fatalf("output terminal = %v, want EOF", err)
	}
}
