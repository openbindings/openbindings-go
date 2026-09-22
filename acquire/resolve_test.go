package acquire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/httpdiscovery"
	"github.com/openbindings/openbindings-go/synthesize"
)

func makeTestInterface(name string, ops ...string) *openbindings.Interface {
	iface := &openbindings.Interface{OpenBindings: "0.2.0", Name: name, Operations: map[string]openbindings.Operation{}}
	for _, op := range ops {
		iface.Operations[op] = openbindings.Operation{}
	}
	return iface
}

func serveOBI(t *testing.T, iface *openbindings.Interface) *httptest.Server {
	t.Helper()
	data, err := json.Marshal(iface)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
}

func TestResolve_DirectOBI(t *testing.T) {
	iface := makeTestInterface("svc", "ping")
	srv := serveOBI(t, iface)
	defer srv.Close()

	got, err := Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Interface == nil {
		t.Fatal("expected non-nil result")
	}
	if got.Synthesized {
		t.Error("direct OBI fetch should not be marked synthesized")
	}
	if _, ok := got.Interface.Operations["ping"]; !ok {
		t.Error("ping operation missing from fetched OBI")
	}
}

func TestResolve_WellKnownDiscovery(t *testing.T) {
	iface := makeTestInterface("svc", "ping")
	data, _ := json.Marshal(iface)

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openbindings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	got, err := Resolve(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Interface == nil {
		t.Fatal("expected non-nil result via well-known")
	}
	if got.Synthesized {
		t.Error("well-known discovery should not be marked synthesized")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestResolveDiscoversAtOriginNotTargetPath(t *testing.T) {
	var paths []string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		if request.URL.Path == httpdiscovery.WellKnownPath {
			return testResponse(request, http.StatusOK, `{"openbindings":"0.2.0","operations":{"ping":{}}}`), nil
		}
		return testResponse(request, http.StatusNotFound, ""), nil
	})}
	result, err := Resolve(context.Background(), "https://example.test/api", WithHTTPClient(client))
	if err != nil || result == nil || result.Interface == nil {
		t.Fatalf("Resolve = (%#v, %v)", result, err)
	}
	if len(paths) != 2 || paths[0] != "/api" || paths[1] != httpdiscovery.WellKnownPath {
		t.Fatalf("requested paths = %q", paths)
	}
}

func TestResolveDoesNotSynthesizeAfterGatedDiscoveryOrVersionRefusal(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{name: "gated", status: http.StatusUnauthorized, check: func(err error) bool { var e *httpdiscovery.GatedError; return errors.As(err, &e) }},
		{name: "version refusal", status: http.StatusOK, body: `{"openbindings":"0.3.0","operations":{}}`, check: func(err error) bool { var e *httpdiscovery.VersionRefusalError; return errors.As(err, &e) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == httpdiscovery.WellKnownPath {
					return testResponse(request, tt.status, tt.body), nil
				}
				return testResponse(request, http.StatusNotFound, ""), nil
			})}
			result, err := Resolve(context.Background(), "https://example.test", WithHTTPClient(client), WithSynthesizers(coverageFetchSynthesizer{}))
			if result != nil || !tt.check(err) {
				t.Fatalf("Resolve should preserve discovery outcome, got (%#v, %v)", result, err)
			}
		})
	}
}

func TestResolve_ErrorWhenNoOBIAndNoSynthesizers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"some":"json","but":"not an OBI"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := Resolve(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error when no OBI is available and no synthesizers are supplied")
	}
}

func TestResolve_EmptyTarget(t *testing.T) {
	_, err := Resolve(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty target")
	}
}

func TestResolve_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Resolve(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error from 500 response with no synthesizers to fall back to")
	}
}

// Total resolution failure reports the WHOLE chain — direct, well-known,
// each synthesizer — plus the pass-the-spec-URL hint. Regression: only the
// last synthesizer's raw parse error surfaced, pointing a user who passed
// an API's HTML root at a third-party internals message.
func TestResolve_FailureCarriesResolutionTrail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>welcome</html>"))
	}))
	defer srv.Close()

	_, err := Resolve(context.Background(), srv.URL, WithSynthesizers(failingSynthesizer{}))
	if err == nil {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"direct fetch:", httpdiscovery.WellKnownPath, "synthesize as fake@1.0:", "pass the spec document's own URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("trail should contain %q, got:\n%v", want, err)
		}
	}
}

func TestResolve_SynthesizedResultRetainsCoverage(t *testing.T) {
	got, err := Resolve(context.Background(), "artifact://fake", WithSynthesizers(coverageFetchSynthesizer{}))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Synthesized || got.Coverage == nil {
		t.Fatalf("synthesis evidence was discarded: %#v", got)
	}
	if !got.Coverage.Exhaustive || !got.Coverage.FullyRepresented || len(got.Coverage.Entries) != 1 {
		t.Fatalf("wrong coverage: %#v", got.Coverage)
	}
}

type failingSynthesizer struct{}

func (failingSynthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: "fake@1.0"}}
}

func (s failingSynthesizer) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(bindingSpecs, s.BindingSpecs())
}

func (failingSynthesizer) SynthesizeInterface(context.Context, *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	return nil, fmt.Errorf("invalid character '<' looking for beginning of value")
}

type coverageFetchSynthesizer struct{}

func (coverageFetchSynthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: "fake.coverage@1"}}
}

func (s coverageFetchSynthesizer) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(bindingSpecs, s.BindingSpecs())
}

func (coverageFetchSynthesizer) SynthesizeInterface(ctx context.Context, input *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	result, err := (coverageFetchSynthesizer{}).SynthesizeInterfaceWithCoverage(ctx, input)
	if err != nil {
		return nil, err
	}
	return result.Interface, nil
}

func (coverageFetchSynthesizer) SynthesizeInterfaceWithCoverage(_ context.Context, input *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	location := input.Sources[0].Location
	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{"ping": {}},
		Sources: map[string]openbindings.Source{
			"source": {BindingSpec: "fake.coverage@1", Location: location},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"ping.source": {Operation: "ping", Source: "source", Selector: "ping"},
		},
	}
	return synthesize.NewSynthesisResult(iface, []synthesize.SynthesisCoverageEntry{{
		SourceIndex:     0,
		SourceKey:       "source",
		SourceRef:       "ping",
		Scope:           synthesize.SynthesisCoverageTarget,
		Status:          synthesize.SynthesisRepresented,
		OperationKey:    "ping",
		BindingKey:      "ping.source",
		BindingSelector: "ping",
	}}, true)
}
