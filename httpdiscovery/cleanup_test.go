package httpdiscovery_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openbindings/openbindings-go/httpdiscovery"
)

func assertStatus(t *testing.T, result *httpdiscovery.Result, err error, code int) {
	t.Helper()
	var status *httpdiscovery.StatusError
	if !errors.As(err, &status) || status.StatusCode != code || result == nil || result.StatusCode != code {
		t.Fatalf("status result: %+v, %v; want %d", result, err, code)
	}
	if errors.Is(err, httpdiscovery.ErrNotFound) != (code == 404) {
		t.Fatalf("absence classification changed: %v", err)
	}
	if result.Body != nil || result.Document != nil || result.Report.Evidence != nil {
		t.Fatal("non-200 body interpreted as a document")
	}
}

func TestStatusBodyConnectionReuse(t *testing.T) {
	for _, code := range []int{401, 404, 503} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/chunked=%v", code, chunked), func(t *testing.T) {
				var connections atomic.Int32
				s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("WWW-Authenticate", `Bearer realm="discovery"`)
					if !chunked {
						w.Header().Set("Content-Length", strconv.Itoa(len(document)))
					}
					w.WriteHeader(code)
					if chunked {
						w.(http.Flusher).Flush()
					}
					_, _ = io.WriteString(w, document)
				}))
				s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						connections.Add(1)
					}
				}
				s.Start()
				defer s.Close()
				client := httpdiscovery.Client{HTTPClient: s.Client()}
				for i := 0; i < 20; i++ {
					result, err := client.Discover(t.Context(), s.URL)
					assertStatus(t, result, err, code)
					if result.Header.Get("WWW-Authenticate") != `Bearer realm="discovery"` || result.FinalURL != s.URL+httpdiscovery.WellKnownPath {
						t.Fatal("response metadata lost")
					}
				}
				if got := connections.Load(); got != 1 {
					t.Fatalf("20 short responses used %d connections; want 1", got)
				}
			})
		}
	}
}

func TestStatusBodyCleanupBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		length   int64
		proto    int
		reader   io.Reader
		wantRead int
	}{
		{"small", 5, 1, strings.NewReader("error"), 5},
		{"at cap", 2048, 1, strings.NewReader(strings.Repeat("x", 2048)), 2048},
		{"known large", 2049, 1, strings.NewReader(strings.Repeat("x", 2049)), 0},
		{"unknown endless", -1, 1, endlessReader{}, 2048},
		{"read error", -1, 1, failingReader{}, 0},
		{"http2", -1, 2, endlessReader{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: tc.reader}
			client := httpdiscovery.Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 404, Body: body, ContentLength: tc.length, ProtoMajor: tc.proto}, nil
			})}}
			result, err := client.Discover(t.Context(), "https://example.test")
			assertStatus(t, result, err, 404)
			if !body.closed || body.read != tc.wantRead {
				t.Fatalf("closed=%v read=%d; want closed and %d bytes", body.closed, body.read, tc.wantRead)
			}
		})
	}
}

type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestStatusBodyCleanupCancellation(t *testing.T) {
	for _, mode := range []string{"cleanup budget", "trickling body", "caller cancellation", "client timeout"} {
		t.Run(mode, func(t *testing.T) {
			bodyStarted := make(chan struct{})
			serverCanceled := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "20")
				w.WriteHeader(401)
				w.(http.Flusher).Flush()
				if mode == "trickling body" {
					tick := time.NewTicker(10 * time.Millisecond)
					defer tick.Stop()
					for {
						select {
						case <-tick.C:
							_, _ = io.WriteString(w, "x")
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							close(serverCanceled)
							return
						}
					}
				}
				<-r.Context().Done()
				close(serverCanceled)
			}))
			defer s.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			httpClient := s.Client()
			transport := httpClient.Transport
			var body *signalingBody
			httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp, err := transport.RoundTrip(req)
				if err == nil {
					body = &signalingBody{ReadCloser: resp.Body, started: bodyStarted}
					resp.Body = body
				}
				return resp, err
			})
			if mode == "client timeout" {
				httpClient.Timeout = 50 * time.Millisecond
			}
			type outcome struct {
				result *httpdiscovery.Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := (httpdiscovery.Client{HTTPClient: httpClient}).Discover(ctx, s.URL)
				done <- outcome{result, err}
			}()
			select {
			case <-bodyStarted:
			case <-time.After(5 * time.Second):
				cancel()
				t.Fatal("response did not arrive")
			}
			if mode == "caller cancellation" {
				cancel()
			}
			select {
			case got := <-done:
				assertStatus(t, got.result, got.err, 401)
				if body == nil || !body.closed {
					t.Fatal("cleanup did not close the response body")
				}
			case <-time.After(time.Second):
				cancel()
				t.Fatal("bounded cleanup did not return")
			}
			if mode != "caller cancellation" && ctx.Err() != nil {
				t.Fatal("cleanup canceled caller's context")
			}
			select {
			case <-serverCanceled:
			case <-time.After(time.Second):
				t.Fatal("stalled response was not canceled and closed")
			}
		})
	}
}

type signalingBody struct {
	io.ReadCloser
	started chan struct{}
	closed  bool
}

func (b *signalingBody) Close() error {
	b.closed = true
	return b.ReadCloser.Close()
}

func (b *signalingBody) Read(p []byte) (int, error) {
	if b.started != nil {
		close(b.started)
		b.started = nil
	}
	return b.ReadCloser.Read(p)
}
