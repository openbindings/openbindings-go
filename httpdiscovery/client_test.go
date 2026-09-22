package httpdiscovery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const testOBI = `{"openbindings":"0.2.0","operations":{"ping":{}}}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(request *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestEndpointUsesOriginRoot(t *testing.T) {
	got, err := Endpoint("https://example.test:8443/")
	if err != nil || got != "https://example.test:8443/.well-known/openbindings" {
		t.Fatalf("Endpoint = %q, %v", got, err)
	}
	for _, origin := range []string{
		"https://example.test/api", "https://example.test/?token=secret",
		"https://user:pass@example.test", "ftp://example.test", "https://example.test/#fragment",
	} {
		if _, err := Endpoint(origin); err == nil {
			t.Errorf("Endpoint(%q) accepted a non-origin URL", origin)
		}
	}
}

func TestDiscoverRequestsPublishedOBI(t *testing.T) {
	for _, contentType := range []string{"application/vnd.openbindings+json", "application/json"} {
		t.Run(contentType, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != "https://example.test/.well-known/openbindings" || request.Method != http.MethodGet {
					t.Errorf("unexpected request: %s %s", request.Method, request.URL)
				}
				if got := request.Header.Get("Accept"); got != "application/vnd.openbindings+json, application/json" {
					t.Errorf("Accept = %q", got)
				}
				return response(request, http.StatusOK, contentType, testOBI), nil
			})}
			iface, found, err := Discover(context.Background(), "https://example.test", WithHTTPClient(client))
			if err != nil || !found || iface == nil {
				t.Fatalf("Discover = (%#v, %t, %v)", iface, found, err)
			}
			if _, ok := iface.Operations["ping"]; !ok {
				t.Fatal("published operation missing")
			}
		})
	}
}

func TestDiscoverOutcomesStayDistinct(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
		absent bool
	}{
		{name: "absent", status: http.StatusNotFound, absent: true},
		{name: "unauthorized", status: http.StatusUnauthorized, check: func(err error) bool {
			var e *GatedError
			return errors.As(err, &e) && e.StatusCode == http.StatusUnauthorized
		}},
		{name: "forbidden", status: http.StatusForbidden, check: func(err error) bool {
			var e *GatedError
			return errors.As(err, &e) && e.StatusCode == http.StatusForbidden
		}},
		{name: "server failure", status: http.StatusInternalServerError, check: func(err error) bool {
			var e *StatusError
			return errors.As(err, &e) && e.StatusCode == http.StatusInternalServerError
		}},
		{name: "refused version", status: http.StatusOK, body: `{"openbindings":"0.3.0","operations":{}}`, check: func(err error) bool { var e *VersionRefusalError; return errors.As(err, &e) && e.Version == "0.3.0" }},
		{name: "invalid document", status: http.StatusOK, body: `{"not":"an OBI"}`, check: func(err error) bool { return err != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return response(request, tt.status, "application/json", tt.body), nil
			})}
			iface, found, err := Discover(context.Background(), "https://example.test", WithHTTPClient(client))
			if iface != nil || found {
				t.Fatalf("unexpected document: (%#v, %t)", iface, found)
			}
			if tt.absent {
				if err != nil {
					t.Fatalf("404 should be absence: %v", err)
				}
			} else if !tt.check(err) {
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}

func TestDiscoverFollowsClientRedirectPolicy(t *testing.T) {
	var hops int
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		hops++
		if hops == 1 {
			resp := response(request, http.StatusFound, "", "")
			resp.Header.Set("Location", "https://cdn.example.test/interface.json")
			return resp, nil
		}
		if request.URL.String() != "https://cdn.example.test/interface.json" {
			t.Errorf("redirect target = %s", request.URL)
		}
		return response(request, http.StatusOK, "application/json", testOBI), nil
	})}
	_, found, err := Discover(context.Background(), "https://example.test", WithHTTPClient(client))
	if err != nil || !found || hops != 2 {
		t.Fatalf("redirected discovery = (found %t, err %v, hops %d)", found, err, hops)
	}
}

func TestDiscoverAppliesDocumentLimit(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return response(request, http.StatusOK, "application/json", testOBI), nil
	})}
	_, found, err := Discover(context.Background(), "https://example.test", WithHTTPClient(client), WithMaxDocumentBytes(8))
	if found || err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("over-limit discovery = (found %t, err %v)", found, err)
	}
}
