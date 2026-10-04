package httpdiscovery_test

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go/httpdiscovery"
)

func TestEndpoint(t *testing.T) {
	for origin, endpoint := range map[string]string{
		"https://example.test":      "https://example.test/.well-known/openbindings",
		"HTTP://localhost:8080/":    "http://localhost:8080/.well-known/openbindings",
		"https://[::1]:8443":        "https://[::1]:8443/.well-known/openbindings",
		"https://EXAMPLE.test:443/": "https://EXAMPLE.test:443/.well-known/openbindings",
	} {
		got, err := httpdiscovery.Endpoint(origin)
		if err != nil || got != endpoint {
			t.Errorf("Endpoint(%q) = %q, %v", origin, got, err)
		}
	}
	for _, origin := range []string{
		"", "example.test", "//example.test", "ftp://example.test", "https:", "https:///",
		"https://user:pass@example.test", "https://example.test/api", "https://example.test/%2f",
		"https://example.test?q=x", "https://example.test?", "https://example.test#part",
		"https://example.test#", "https://example.test:wrong", "https://exa mple.test", "https://%",
	} {
		if got, err := httpdiscovery.Endpoint(origin); err == nil || got != "" {
			t.Errorf("Endpoint(%q) = %q, %v; wanted rejection", origin, got, err)
		}
	}
}

func TestRedirectPolicy(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer s.Close()
	denied := errors.New("application redirect policy")
	for name, tc := range map[string]struct {
		policy func(*http.Request, []*http.Request) error
		want   error
		status int
	}{
		"default limit": {},
		"refuse":        {policy: func(*http.Request, []*http.Request) error { return denied }, want: denied},
		"last response": {policy: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, status: 302},
	} {
		t.Run(name, func(t *testing.T) {
			httpClient := &http.Client{CheckRedirect: tc.policy}
			client := httpdiscovery.Client{HTTPClient: httpClient}
			r, err := client.Discover(t.Context(), s.URL)
			if err == nil || errors.Is(err, httpdiscovery.ErrNotFound) {
				t.Fatalf("redirect policy lost: %+v, %v", r, err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("lost policy error: %v", err)
			}
			if tc.status != 0 {
				var status *httpdiscovery.StatusError
				if !errors.As(err, &status) || status.StatusCode != tc.status || r.StatusCode != tc.status {
					t.Fatalf("last response = %+v, %v", r, err)
				}
			} else if r != nil {
				t.Fatal("failed HTTP call should not return a usable response")
			}
		})
	}
}

type trackedBody struct {
	io.Reader
	closed bool
	read   int
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestBodyBoundsAndClosure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int64
		body     string
		tooLarge bool
	}{
		{"exact", int64(len(document)), document, false},
		{"one over", int64(len(document) - 1), document, true},
		{"default", 0, document, false},
		{"default over", 0, document + strings.Repeat(" ", 1<<20), true},
		{"int64 maximum", math.MaxInt64, document, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			client := httpdiscovery.Client{MaxDocumentBytes: tc.limit, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				// Response.Request is optional for a custom transport. Absence
				// must not cause a panic or invent a final URL after redirects.
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})}}
			r, err := client.Discover(t.Context(), "https://example.test")
			if errors.Is(err, httpdiscovery.ErrDocumentTooLarge) != tc.tooLarge || (!tc.tooLarge && err != nil) || errors.Is(err, httpdiscovery.ErrNotFound) {
				t.Fatalf("limit = %+v, %v", r, err)
			}
			if !body.closed || r.FinalURL != "" {
				t.Fatalf("closed=%v finalURL=%q", body.closed, r.FinalURL)
			}
			limit := tc.limit
			if limit == 0 {
				limit = 1 << 20
			}
			if limit < math.MaxInt64 && int64(body.read) > limit+1 {
				t.Fatalf("read %d bytes with limit %d", body.read, limit)
			}
			if tc.tooLarge && (r.Document != nil || r.Report.Evidence != nil || r.Body != nil) {
				t.Fatal("oversized prefix treated as a complete document")
			}
		})
	}
	for _, status := range []int{200, 401, 404, 500} {
		body := &trackedBody{Reader: failingReader{}}
		client := httpdiscovery.Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body}, nil
		})}}
		_, err := client.Discover(t.Context(), "https://example.test")
		if err == nil || !body.closed {
			t.Fatalf("status=%d err=%v closed=%v", status, err, body.closed)
		}
		if status == 200 && !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("lost read error: %v", err)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDecompressedBodyBound(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		_, _ = z.Write([]byte(document + strings.Repeat(" ", 10000)))
		_ = z.Close()
	}))
	defer s.Close()
	_, err := (httpdiscovery.Client{MaxDocumentBytes: 500}).Discover(t.Context(), s.URL)
	if !errors.Is(err, httpdiscovery.ErrDocumentTooLarge) {
		t.Fatalf("decompressed bound: %v", err)
	}
}

func TestCancellationAndTransportErrors(t *testing.T) {
	t.Run("canceled before request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := (httpdiscovery.Client{}).Discover(ctx, "https://example.invalid")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	})
	t.Run("cancel during body", func(t *testing.T) {
		started := make(chan struct{})
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		}))
		defer s.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := (httpdiscovery.Client{}).Discover(ctx, s.URL); done <- err }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("request did not reach server")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) || errors.Is(err, httpdiscovery.ErrNotFound) {
				t.Fatalf("body cancellation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled body read did not return")
		}
	})
	t.Run("transport error", func(t *testing.T) {
		failure := errors.New("transport failed")
		client := httpdiscovery.Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, failure })}}
		r, err := client.Discover(t.Context(), "https://example.test")
		if r != nil || !errors.Is(err, failure) || errors.Is(err, httpdiscovery.ErrNotFound) {
			t.Fatalf("transport failure: %+v, %v", r, err)
		}
	})
}

func TestConfigurationRejectedBeforeRequest(t *testing.T) {
	client := httpdiscovery.Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid configuration reached the transport")
		return nil, errors.New("unexpected request")
	})}}
	client.MaxDocumentBytes = -1
	if _, err := client.Discover(t.Context(), "https://example.test"); err == nil {
		t.Fatal("negative limit accepted")
	}
	client.MaxDocumentBytes = 0
	if _, err := client.Discover(t.Context(), "https://example.test/resource"); err == nil {
		t.Fatal("resource URL accepted")
	}
	if _, err := client.Discover(nil, "https://example.test"); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestDocumentContextAndConcurrentIsolation(t *testing.T) {
	const rich = `{"openbindings":"0.2.0","operations":{"op":{"input":{"$ref":"#/schemas/input"}}},"schemas":{"input":{"type":"string"}},"sources":{"s":{"kind":"unknown@1","content":{"url":"https://example.invalid/source"}}},"bindings":{"b":{"operation":"op","source":"s","content":{"target":"../invoke"}}},"dependencies":{"d":{"operation":"op","kinds":["unknown@1"]}},"x-extra":{"kept":true}}`
	handler, err := httpdiscovery.NewHandler([]byte(rich), httpdiscovery.HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	defer s.Close()
	client := httpdiscovery.Client{}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			r, err := client.Discover(t.Context(), s.URL)
			if err != nil {
				t.Error(err)
				return
			}
			if string(r.Body) != rich || r.Document.Dependencies["d"].Operation != "op" {
				t.Error("document altered")
				return
			}
			refs, err := r.Document.References()
			if err != nil || len(refs) != 1 || refs[0].Value != "#/schemas/input" || refs[0].Target != "/schemas/input" {
				t.Errorf("retrieval changed references: %+v, %v", refs, err)
			}
			// Caller-owned results must not race with other requests or mutate
			// the handler's snapshot, even while the same client is reused.
			clear(r.Body)
			r.Header.Set("Content-Type", "changed")
			delete(r.Document.Dependencies, "d")
			delete(r.Report.Evidence, "OBI-D-01")
		})
	}
	wg.Wait()
	if requests.Load() != 32 {
		t.Fatalf("unexpected caching or resource requests: %d", requests.Load())
	}
}
