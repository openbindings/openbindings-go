package httpdiscovery_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/openbindings/openbindings-go/httpdiscovery"
)

func TestHandlerMethodsAndRouting(t *testing.T) {
	handler, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"HEAD", httpdiscovery.WellKnownPath, 200},
		{"POST", httpdiscovery.WellKnownPath, 405},
		{"OPTIONS", httpdiscovery.WellKnownPath, 405},
		{"GET", "/elsewhere", 404},
		{"GET", httpdiscovery.WellKnownPath + "/", 404},
		{"GET", httpdiscovery.WellKnownPath + "?unused=1", 200},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s = %d", tc.method, tc.path, w.Code)
		}
		if tc.method == "HEAD" && (w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(len(document))) {
			t.Error("HEAD did not describe the GET representation without a body")
		}
		if tc.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
			t.Error("405 missing Allow")
		}
	}
}

func TestHandlerRejectsInvalidHeader(t *testing.T) {
	handler, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{AllowOrigin: "*\r\nInjected: value"})
	if err == nil || handler != nil {
		t.Fatal("header injection accepted")
	}
}

func TestHandlerRejectsInvalidCORSOrigin(t *testing.T) {
	invalid := []string{
		"https://client.example/", "https://client.example/path",
		"https://a.example, https://b.example", "https://a.example,b.example",
		"https://a.example https://b.example", "https://user:pass@client.example",
		"https://client.example?", "https://client.example?q=x",
		"https://client.example#", "https://client.example#part",
		" https://client.example", "https://client.example ", " *", "null ",
		"client.example", "//client.example", "https:", "https:///",
		"https://*.example", "https://clïent.example", "https://%63lient.example",
		"https://client.example:", "https://client.example:wrong", "https://client.example:65536",
		"https://[invalid]", "https://[::1]extra", "https://::1", "https://[fe80::1%25en0]",
		"https://client.example\\path",
	}
	for c := byte(0); c < 32; c++ {
		invalid = append(invalid, "https://client.example"+string(c))
	}
	invalid = append(invalid, "https://client.example\x7f")
	for _, origin := range invalid {
		t.Run(fmt.Sprintf("%q", origin), func(t *testing.T) {
			handler, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{AllowOrigin: origin})
			if err == nil || handler != nil {
				t.Fatalf("invalid AllowOrigin %q accepted", origin)
			}
		})
	}
}

func TestHandlerCORSOriginsOnWire(t *testing.T) {
	for _, origin := range []string{
		"", "*", "null", "https://client.example", "http://localhost:8080",
		"https://127.0.0.1:8443", "https://[2001:db8::1]:8443", "https://xn--bcher-kva.example",
		"chrome-extension://abcdefghijklmnop",
	} {
		t.Run(origin, func(t *testing.T) {
			handler, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{AllowOrigin: origin})
			if err != nil {
				t.Fatal(err)
			}
			s := httptest.NewServer(handler)
			defer s.Close()
			resp, err := s.Client().Get(s.URL + httpdiscovery.WellKnownPath)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || string(body) != document {
				t.Fatalf("publication: status=%d body=%q err=%v", resp.StatusCode, body, err)
			}
			if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
				t.Fatalf("AllowOrigin = %q, want %q", got, origin)
			}
		})
	}
}
