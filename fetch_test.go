package openbindings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestFetchInterface_ErrorWhenNoOBIAndNoCreators(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"some":"json","but":"not an OBI"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	_, err := FetchInterface(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error when no OBI is available and no creators are supplied")
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
		t.Error("expected error from 500 response with no creators to fall back to")
	}
}
