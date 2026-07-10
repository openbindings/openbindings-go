package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// httpClientWithHeaders must layer the MCP header injector over a caller's
// transport so a corporate proxy / mTLS / custom-CA client is honored, while
// preserving the client's own Timeout, redirect policy, and cookie jar.
func TestHTTPClientWithHeaders_PreservesBaseAndInjects(t *testing.T) {
	marker := &http.Transport{}
	base := &http.Client{Transport: marker, Timeout: 7 * time.Second}

	hc := httpClientWithHeaders(base, map[string]string{"Authorization": "Bearer t"})

	if hc.Timeout != 7*time.Second {
		t.Errorf("Timeout not preserved: got %v", hc.Timeout)
	}
	ht, ok := hc.Transport.(*headerTransport)
	if !ok {
		t.Fatalf("transport must be the *headerTransport injector, got %T", hc.Transport)
	}
	if ht.base != marker {
		t.Error("caller transport must become the header injector's round-trip base")
	}
	if ht.headers["Authorization"] != "Bearer t" {
		t.Error("auth headers not carried into the injector")
	}
}

func TestHTTPClientWithHeaders_NilBaseUsesDefaults(t *testing.T) {
	hc := httpClientWithHeaders(nil, nil)
	ht, ok := hc.Transport.(*headerTransport)
	if !ok {
		t.Fatalf("transport must be the *headerTransport injector, got %T", hc.Transport)
	}
	if ht.base != http.DefaultTransport {
		t.Error("nil base must fall back to http.DefaultTransport")
	}
}

// End-to-end proof: a real round trip must pass through the injected base
// transport AND carry the injected headers.
func TestHTTPClientWithHeaders_RoundTripUsesBase(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	baseUsed := false
	base := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		baseUsed = true
		return http.DefaultTransport.RoundTrip(r)
	})}

	hc := httpClientWithHeaders(base, map[string]string{"Authorization": "Bearer secret"})
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if !baseUsed {
		t.Error("injected base transport was not used for the round trip")
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization header not injected: got %q", gotAuth)
	}
}

// WithHTTPClient on the invoker must reach the session pool that every
// invocation draws from.
func TestWithHTTPClient_FlowsToPool(t *testing.T) {
	custom := &http.Client{Transport: &http.Transport{}}
	inv := NewInvoker(WithHTTPClient(custom))
	defer func() { _ = inv.Close() }()
	if inv.pool.baseClient != custom {
		t.Error("WithHTTPClient must set the pool's base client")
	}
}
