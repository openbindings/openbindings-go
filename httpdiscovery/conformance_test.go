package httpdiscovery_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/httpdiscovery"
)

const document = `{"openbindings":"0.2.0","operations":{"ping":{}}}`

func discover(t *testing.T, status int, contentType, body string) (*httpdiscovery.Result, error) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.RequestURI() != httpdiscovery.WellKnownPath {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Accept"); got != openbindings.MediaType+", application/json" {
			t.Errorf("Accept = %q", got)
		}
		// Explicitly empty suppresses net/http's automatic Content-Type.
		w.Header()["Content-Type"] = []string{contentType}
		w.Header().Set("WWW-Authenticate", `Bearer realm="discovery"`)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return (httpdiscovery.Client{}).Discover(t.Context(), s.URL)
}

func TestDISC_C01Representations(t *testing.T) {
	for _, mediaType := range []string{
		openbindings.MediaType, "application/json", "application/json; charset=utf-8",
		"application/vnd.openbindings+json; charset=utf-8", "", "text/plain",
	} {
		t.Run(mediaType, func(t *testing.T) {
			r, err := discover(t, 200, mediaType, document)
			if err != nil || r.Document == nil || r.Report.Conclusion != openbindings.ConclusionConformant {
				t.Fatalf("Discover = %+v, %v", r, err)
			}
			if string(r.Body) != document || r.StatusCode != 200 || r.RequestedURL != r.FinalURL {
				t.Fatalf("lost response evidence: %+v", r)
			}
		})
	}
}

func TestDISC_C02Redirects(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			// A different HTTPS origin also exercises the supplied client's TLS
			// transport and preservation of the requested and final URLs.
			destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/documents/obi" || r.Method != "GET" || r.Header.Get("Accept") != openbindings.MediaType+", application/json" {
					t.Errorf("redirect request = %s %s, %v", r.Method, r.URL, r.Header)
				}
				_, _ = w.Write([]byte(document))
			}))
			defer destination.Close()
			start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, destination.URL+"/documents/obi", code)
			}))
			defer start.Close()
			client := httpdiscovery.Client{HTTPClient: destination.Client()}
			r, err := client.Discover(t.Context(), start.URL)
			if err != nil || r.Document == nil || r.RequestedURL != start.URL+httpdiscovery.WellKnownPath || r.FinalURL != destination.URL+"/documents/obi" {
				t.Fatalf("redirect = %+v, %v", r, err)
			}
		})
	}
}

func TestDISC_C03Statuses(t *testing.T) {
	for _, code := range []int{204, 206, 300, 304, 400, 401, 403, 404, 429, 500, 503} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			r, err := discover(t, code, "application/json", document)
			var status *httpdiscovery.StatusError
			if !errors.As(err, &status) || status.StatusCode != code || errors.Is(err, httpdiscovery.ErrNotFound) != (code == 404) {
				t.Fatalf("HTTP %d = %+v, %v", code, r, err)
			}
			if r.StatusCode != code || r.Document != nil || r.Report.Evidence != nil || r.Body != nil {
				t.Fatalf("non-200 interpreted as a document: %+v", r)
			}
			if r.Header.Get("WWW-Authenticate") != `Bearer realm="discovery"` {
				t.Fatal("lost authentication challenge")
			}
		})
	}
}

func TestDISC_C03EstablishedViolations(t *testing.T) {
	for name, body := range map[string]string{
		"not JSON":        `<html>not an OBI</html>`,
		"not an object":   `null`,
		"missing version": `{"operations":{}}`,
		"unknown member":  `{"openbindings":"0.2.0","operations":{},"unknown":1}`,
		"duplicate":       `{"openbindings":"0.2.0","operations":{},"operations":{}}`,
		"dependency":      `{"openbindings":"0.2.0","operations":{},"dependencies":{"d":{"operation":"missing"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, err := discover(t, 200, "application/json", body)
			var violation *openbindings.ValidationError
			if !errors.As(err, &violation) || errors.Is(err, httpdiscovery.ErrNotFound) || r.Report.Conclusion != openbindings.ConclusionNonConformant {
				t.Fatalf("violation = %+v, %v", r, err)
			}
			if string(r.Body) != body || len(r.Report.Findings) == 0 || r.Report.Revision == "" {
				t.Fatal("lost core validation evidence or source bytes")
			}
		})
	}
}

func undeterminedDocument() string {
	return `{"openbindings":"0.2.0","operations":{},"schemas":{"deep":` +
		strings.Repeat(`{"not":`, 260) + `{}` + strings.Repeat(`}`, 260) + `}}`
}

func TestDISC_C03InconclusiveIsNotAbsence(t *testing.T) {
	t.Run("model cannot carry it", func(t *testing.T) {
		r, err := discover(t, 200, "application/json", `{"openbindings":"0.2.0","operations":{},"x-value":"\ud800"}`)
		if !errors.Is(err, openbindings.ErrInconclusive) || errors.Is(err, httpdiscovery.ErrNotFound) || r.Document != nil || r.Report.Conclusion != openbindings.ConclusionConformanceUndetermined {
			t.Fatalf("inconclusive = %+v, %v", r, err)
		}
	})
	t.Run("document with an undecided rule", func(t *testing.T) {
		r, err := discover(t, 200, "application/json", undeterminedDocument())
		if err != nil || r.Document == nil || r.Report.Conclusion != openbindings.ConclusionConformanceUndetermined {
			t.Fatalf("undetermined = %+v, %v", r, err)
		}
	})
}

func TestDISC_C04VersionRefusal(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.3.0", "1.0.0", "0.2.0-rc.1"} {
		t.Run(version, func(t *testing.T) {
			// An unusual media type and unfamiliar members must not obscure a
			// version refusal by judging the document under current semantics.
			r, err := discover(t, 200, "text/plain", fmt.Sprintf(`{"openbindings":%q,"future":true}`, version))
			var refusal *openbindings.VersionRefusalError
			if !errors.As(err, &refusal) || refusal.Version != version || errors.Is(err, httpdiscovery.ErrNotFound) || r.Report.Evidence != nil || r.Document != nil {
				t.Fatalf("refusal = %+v, %v", r, err)
			}
		})
	}
}

func TestDISC_S01S02Publication(t *testing.T) {
	data := []byte("  " + document + "\n")
	handler, err := httpdiscovery.NewHandler(data, httpdiscovery.HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The constructor's snapshot must survive the caller reusing its buffer.
	clear(data)
	for _, accept := range []string{"", "text/html", "application/xml", "*/*", openbindings.MediaType + ", application/json"} {
		t.Run(accept, func(t *testing.T) {
			req := httptest.NewRequest("GET", httpdiscovery.WellKnownPath, nil)
			req.Header.Set("Accept", accept)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != 200 || w.Header().Get("Content-Type") != openbindings.MediaType || w.Body.String() != "  "+document+"\n" {
				t.Fatalf("response = %d %v %q", w.Code, w.Header(), w.Body.String())
			}
		})
	}
}

func TestDISC_S01RequiresConformance(t *testing.T) {
	for name, tc := range map[string]struct{ body, category string }{
		"empty":        {"", "violation"},
		"not JSON":     {"not JSON", "violation"},
		"invalid":      {`{"openbindings":"0.2.0","unknown":1}`, "violation"},
		"refused":      {`{"openbindings":"0.3.0","operations":{}}`, "version"},
		"undetermined": {undeterminedDocument(), "inconclusive"},
		"unreadable":   {`{"openbindings":"0.2.0","x-value":"\ud800"}`, "inconclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			handler, err := httpdiscovery.NewHandler([]byte(tc.body), httpdiscovery.HandlerOptions{})
			if err == nil || handler != nil {
				t.Fatalf("published without established conformance: %v, %v", handler, err)
			}
			var violation *openbindings.ValidationError
			var refusal *openbindings.VersionRefusalError
			if errors.As(err, &violation) != (tc.category == "violation") || errors.As(err, &refusal) != (tc.category == "version") || errors.Is(err, openbindings.ErrInconclusive) != (tc.category == "inconclusive") {
				t.Fatalf("wrong error category: %v", err)
			}
		})
	}
}

func TestDISC_S03AbsentAndGatedDeployment(t *testing.T) {
	published, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{404, 401, 403} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			mux := http.NewServeMux()
			if code != 404 {
				mux.Handle(httpdiscovery.WellKnownPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer example" {
						w.Header().Set("WWW-Authenticate", "Bearer")
						w.WriteHeader(code)
						return
					}
					published.ServeHTTP(w, r)
				}))
			}
			s := httptest.NewServer(mux)
			defer s.Close()
			r, err := (httpdiscovery.Client{}).Discover(t.Context(), s.URL)
			var status *httpdiscovery.StatusError
			if !errors.As(err, &status) || r.StatusCode != code || errors.Is(err, httpdiscovery.ErrNotFound) != (code == 404) {
				t.Fatalf("deployment = %+v, %v", r, err)
			}
			if code != 404 {
				client := httpdiscovery.Client{HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					copy := req.Clone(req.Context())
					copy.Header.Set("Authorization", "Bearer example")
					return http.DefaultTransport.RoundTrip(copy)
				})}}
				r, err := client.Discover(t.Context(), s.URL)
				if err != nil || r.Document == nil {
					t.Fatalf("authenticated discovery = %+v, %v", r, err)
				}
			}
		})
	}
}

func TestDISC_S04CORSPolicy(t *testing.T) {
	for _, origin := range []string{"", "*", "https://client.example"} {
		t.Run(origin, func(t *testing.T) {
			handler, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{AllowOrigin: origin})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", httpdiscovery.WellKnownPath, nil)
			req.Header.Set("Origin", "https://unrelated.example")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != origin {
				t.Fatalf("CORS response = %d %v", w.Code, w.Header())
			}
			if w.Header().Get("Access-Control-Allow-Credentials") != "" {
				t.Fatal("handler invented a credential policy")
			}
		})
	}
}

func TestCompanionAuthority(t *testing.T) {
	// CI checks out this revision for core's existing corpus gate. These
	// companion cases run independently; this check binds them to the named
	// companion text instead of silently following an edited draft.
	const revision = "04a84131295dc8c305b4f048d2e129f84fb023de"
	const digest = "e33492b180dc36b35bc86d39c9ed1f2f59fbfff7272dff2b7bf16a4edaeae91d"
	corpus := os.Getenv("OB_SPEC_CORPUS")
	if corpus == "" {
		if os.Getenv("OB_CORPUS_REQUIRED") == "1" {
			t.Fatal("OB_SPEC_CORPUS must name the checked-out authority in CI")
		}
		t.Skip("set OB_SPEC_CORPUS to verify the companion text against " + revision)
	}
	data, err := os.ReadFile(filepath.Join(corpus, "..", "http-discovery.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != digest {
		t.Fatalf("companion text differs from spec revision %s: %s", revision, got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
