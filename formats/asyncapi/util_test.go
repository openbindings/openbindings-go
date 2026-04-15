package asyncapi

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestParseRef_BareID(t *testing.T) {
	got, err := parseRef("sendMessage")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sendMessage" {
		t.Errorf("parseRef(%q) = %q, want %q", "sendMessage", got, "sendMessage")
	}
}

func TestParseRef_HashOperations(t *testing.T) {
	got, err := parseRef("#/operations/receiveEvents")
	if err != nil {
		t.Fatal(err)
	}
	if got != "receiveEvents" {
		t.Errorf("parseRef(%q) = %q, want %q", "#/operations/receiveEvents", got, "receiveEvents")
	}
}

func TestParseRef_Empty(t *testing.T) {
	_, err := parseRef("")
	if err == nil {
		t.Error("expected error for empty ref")
	}
}

func TestResolveServer_HTTPServer(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	url, proto, err := resolveServer(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if proto != "https" {
		t.Errorf("protocol = %q, want https", proto)
	}
	if url != "https://api.example.com" {
		t.Errorf("url = %q, want https://api.example.com", url)
	}
}

func TestResolveServer_MetadataOverride(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	opts := &openbindings.ExecutionOptions{
		Metadata: map[string]any{"baseURL": "http://localhost:8080"},
	}
	url, proto, err := resolveServer(doc, opts)
	if err != nil {
		t.Fatal(err)
	}
	if proto != "http" {
		t.Errorf("protocol = %q, want http", proto)
	}
	if url != "http://localhost:8080" {
		t.Errorf("url = %q, want http://localhost:8080", url)
	}
}

func TestResolveServer_NoServers(t *testing.T) {
	doc := &Document{}
	_, _, err := resolveServer(doc, nil)
	if err == nil {
		t.Error("expected error for doc with no servers")
	}
}

func TestResolveServerKey_NormalizesHost(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https"},
		},
	}
	key := resolveServerKey(doc)
	if key != "api.example.com" {
		t.Errorf("resolveServerKey = %q, want api.example.com", key)
	}
}

func TestResolveServerKey_WithPort(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"dev": {Host: "localhost:8080", Protocol: "http"},
		},
	}
	key := resolveServerKey(doc)
	if key != "localhost:8080" {
		t.Errorf("resolveServerKey = %q, want localhost:8080", key)
	}
}

func TestResolveServerKey_TrimsTrailingSlash(t *testing.T) {
	doc := &Document{
		Servers: map[string]Server{
			"prod": {Host: "api.example.com", Protocol: "https", PathName: "/api/"},
		},
	}
	key := resolveServerKey(doc)
	if key != "api.example.com" {
		t.Errorf("resolveServerKey = %q, want api.example.com", key)
	}
}
