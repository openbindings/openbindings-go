package openbindings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveOBI(t *testing.T, iface *Interface) *httptest.Server {
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

func TestFetchInterface_DirectOBI(t *testing.T) {
	iface := makeTestInterface("svc", "ping")
	srv := serveOBI(t, iface)
	defer srv.Close()

	got, err := FetchInterface(context.Background(), srv.URL)
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

func TestFetchInterface_WellKnownDiscovery(t *testing.T) {
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

	got, err := FetchInterface(context.Background(), srv.URL)
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

func TestFetchInterface_ErrorWhenNoOBIAndNoSynthesizers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"some":"json","but":"not an OBI"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := FetchInterface(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error when no OBI is available and no synthesizers are supplied")
	}
}

func TestFetchInterface_EmptyTarget(t *testing.T) {
	_, err := FetchInterface(context.Background(), "")
	if err == nil {
		t.Error("expected error for empty target")
	}
}

func TestFetchInterface_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := FetchInterface(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error from 500 response with no synthesizers to fall back to")
	}
}

// Total resolution failure reports the WHOLE chain — direct, well-known,
// each synthesizer — plus the pass-the-spec-URL hint. Regression: only the
// last synthesizer's raw parse error surfaced, pointing a user who passed
// an API's HTML root at a third-party internals message.
func TestFetchInterface_FailureCarriesResolutionTrail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>welcome</html>"))
	}))
	defer srv.Close()

	_, err := FetchInterface(context.Background(), srv.URL, WithSynthesizers(failingSynthesizer{}))
	if err == nil {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"direct fetch:", WellKnownPath, "synthesize as fake@1.0:", "pass the spec document's own URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("trail should contain %q, got:\n%v", want, err)
		}
	}
}

func TestFetchInterface_SynthesizedResultRetainsCoverage(t *testing.T) {
	got, err := FetchInterface(context.Background(), "artifact://fake", WithSynthesizers(coverageFetchSynthesizer{}))
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

func (failingSynthesizer) BindingSpecs() []BindingSpecInfo {
	return []BindingSpecInfo{{BindingSpec: "fake@1.0"}}
}

func (failingSynthesizer) SynthesizeInterface(context.Context, *SynthesizeInput) (*Interface, error) {
	return nil, fmt.Errorf("invalid character '<' looking for beginning of value")
}

type coverageFetchSynthesizer struct{}

func (coverageFetchSynthesizer) BindingSpecs() []BindingSpecInfo {
	return []BindingSpecInfo{{BindingSpec: "fake.coverage@1"}}
}

func (coverageFetchSynthesizer) SynthesizeInterface(ctx context.Context, input *SynthesizeInput) (*Interface, error) {
	result, err := (coverageFetchSynthesizer{}).SynthesizeInterfaceWithCoverage(ctx, input)
	if err != nil {
		return nil, err
	}
	return result.Interface, nil
}

func (coverageFetchSynthesizer) SynthesizeInterfaceWithCoverage(_ context.Context, input *SynthesizeInput) (*SynthesizeResult, error) {
	location := input.Sources[0].Location
	iface := &Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"ping": {}},
		Sources: map[string]Source{
			"source": {BindingSpec: "fake.coverage@1", Location: location},
		},
		Bindings: map[string]BindingEntry{
			"ping.source": {Operation: "ping", Source: "source", Ref: "ping"},
		},
	}
	return NewSynthesisResult(iface, []SynthesisCoverageEntry{{
		SourceIndex:  0,
		SourceKey:    "source",
		SourceRef:    "ping",
		Scope:        SynthesisCoverageTarget,
		Status:       SynthesisRepresented,
		OperationKey: "ping",
		BindingKey:   "ping.source",
		BindingRef:   "ping",
	}}, true)
}
