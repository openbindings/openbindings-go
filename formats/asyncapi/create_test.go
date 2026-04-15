package asyncapi

import (
	"context"
	"sort"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// helper wraps createInterfaceWithDoc for simpler test calls
func testCreateInterface(t *testing.T, doc *Document, location string) openbindings.Interface {
	t.Helper()
	iface, err := createInterfaceWithDoc(context.Background(), &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{{Format: FormatToken, Location: location}},
	}, doc)
	if err != nil {
		t.Fatal(err)
	}
	return *iface
}

func TestCreateInterface_CopiesMetadata(t *testing.T) {
	doc := &Document{
		AsyncAPI:   "3.0.0",
		Info:       Info{Title: "Test API", Version: "1.0.0", Description: "A test"},
		Operations: map[string]Operation{},
	}

	iface := testCreateInterface(t, doc, "")
	if iface.Name != "Test API" {
		t.Errorf("Name = %q, want %q", iface.Name, "Test API")
	}
	if iface.Version != "1.0.0" {
		t.Errorf("Version = %q, want %q", iface.Version, "1.0.0")
	}
	if iface.Description != "A test" {
		t.Errorf("Description = %q, want %q", iface.Description, "A test")
	}
}

func TestCreateInterface_CreatesOperationsAlphabetically(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Operations: map[string]Operation{
			"zeta":  {Action: "send", Channel: ChannelRef{Ref: "#/channels/ch"}},
			"alpha": {Action: "receive", Channel: ChannelRef{Ref: "#/channels/ch"}},
		},
		Channels: map[string]Channel{"ch": {Address: "/ch"}},
	}

	iface := testCreateInterface(t, doc, "")
	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["alpha"]; !ok {
		t.Error("expected operation 'alpha'")
	}
	if _, ok := iface.Operations["zeta"]; !ok {
		t.Error("expected operation 'zeta'")
	}
}

func TestCreateInterface_CreatesBindingsWithRefs(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Operations: map[string]Operation{
			"sendMsg": {Action: "send", Channel: ChannelRef{Ref: "#/channels/messages"}},
		},
		Channels: map[string]Channel{"messages": {Address: "/messages"}},
	}

	iface := testCreateInterface(t, doc, "")
	key := "sendMsg." + DefaultSourceName
	binding, ok := iface.Bindings[key]
	if !ok {
		t.Fatalf("expected binding %q", key)
	}
	if binding.Ref != "#/operations/sendMsg" {
		t.Errorf("ref = %q, want %q", binding.Ref, "#/operations/sendMsg")
	}
	if binding.Operation != "sendMsg" {
		t.Errorf("operation = %q, want %q", binding.Operation, "sendMsg")
	}
}

func TestCreateInterface_SourceLocationConditional(t *testing.T) {
	doc := &Document{AsyncAPI: "3.0.0", Operations: map[string]Operation{}}

	withLoc := testCreateInterface(t, doc, "https://example.com/spec.json")
	if withLoc.Sources[DefaultSourceName].Location != "https://example.com/spec.json" {
		t.Errorf("with location: got %q", withLoc.Sources[DefaultSourceName].Location)
	}

	withoutLoc := testCreateInterface(t, doc, "")
	if withoutLoc.Sources[DefaultSourceName].Location != "" {
		t.Errorf("without location: got %q, want empty", withoutLoc.Sources[DefaultSourceName].Location)
	}
}

func TestCreateInterface_NoOperations(t *testing.T) {
	doc := &Document{AsyncAPI: "3.0.0", Operations: map[string]Operation{}}
	iface := testCreateInterface(t, doc, "")
	if len(iface.Operations) != 0 {
		t.Errorf("expected 0 operations, got %d", len(iface.Operations))
	}
}

func TestCreateInterface_FormatToken(t *testing.T) {
	doc := &Document{AsyncAPI: "3.0.0", Operations: map[string]Operation{}}
	iface := testCreateInterface(t, doc, "")
	if iface.Sources[DefaultSourceName].Format != "asyncapi@3.0" {
		t.Errorf("format = %q, want asyncapi@3.0", iface.Sources[DefaultSourceName].Format)
	}
}

func TestCreateInterface_OAuth2SecurityScheme(t *testing.T) {
	doc := &Document{
		AsyncAPI: "3.0.0",
		Info:     Info{Title: "OAuth2 Test", Version: "1.0.0"},
		Servers: map[string]Server{
			"main": {
				Host:     "api.example.com",
				Protocol: "https",
				Security: []map[string][]string{
					{"myOAuth": {}},
				},
			},
		},
		Channels: map[string]Channel{
			"things": {Address: "/things"},
		},
		Operations: map[string]Operation{
			"getThings": {
				Action:  "receive",
				Channel: ChannelRef{Ref: "#/channels/things"},
			},
		},
		Components: &Components{
			SecuritySchemes: map[string]SecurityScheme{
				"myOAuth": {
					Type:        "oauth2",
					Description: "OAuth2 auth-code flow",
					Flows: &OAuthFlows{
						AuthorizationCode: &OAuthFlow{
							AuthorizationURL: "https://auth.example.com/authorize",
							TokenURL:         "https://auth.example.com/token",
							Scopes: map[string]string{
								"read":  "Read access",
								"write": "Write access",
							},
						},
					},
				},
			},
		},
	}

	iface := testCreateInterface(t, doc, "")

	if iface.Security == nil {
		t.Fatal("expected security entries, got nil")
	}

	methods, ok := iface.Security["myOAuth"]
	if !ok {
		t.Fatalf("expected security key 'myOAuth', got keys: %v", iface.Security)
	}
	if len(methods) != 1 {
		t.Fatalf("expected 1 method, got %d", len(methods))
	}

	m := methods[0]
	if m.Type != "oauth2" {
		t.Errorf("Type = %q, want %q", m.Type, "oauth2")
	}
	if m.Description != "OAuth2 auth-code flow" {
		t.Errorf("Description = %q, want %q", m.Description, "OAuth2 auth-code flow")
	}
	if m.AuthorizeURL != "https://auth.example.com/authorize" {
		t.Errorf("AuthorizeURL = %q, want %q", m.AuthorizeURL, "https://auth.example.com/authorize")
	}
	if m.TokenURL != "https://auth.example.com/token" {
		t.Errorf("TokenURL = %q, want %q", m.TokenURL, "https://auth.example.com/token")
	}

	wantScopes := []string{"read", "write"}
	got := make([]string, len(m.Scopes))
	copy(got, m.Scopes)
	sort.Strings(got)
	if len(got) != len(wantScopes) {
		t.Fatalf("Scopes = %v, want %v", got, wantScopes)
	}
	for i := range wantScopes {
		if got[i] != wantScopes[i] {
			t.Errorf("Scopes[%d] = %q, want %q", i, got[i], wantScopes[i])
		}
	}
}

func TestConvertSecurityScheme_OAuth2FallbackOrder(t *testing.T) {
	// When only clientCredentials flow is present, it should be used.
	scheme := SecurityScheme{
		Type: "oauth2",
		Flows: &OAuthFlows{
			ClientCredentials: &OAuthFlow{
				TokenURL: "https://auth.example.com/token",
				Scopes: map[string]string{
					"admin": "Admin access",
				},
			},
		},
	}
	m := convertSecurityScheme(scheme)
	if m.TokenURL != "https://auth.example.com/token" {
		t.Errorf("TokenURL = %q, want %q", m.TokenURL, "https://auth.example.com/token")
	}
	if len(m.Scopes) != 1 || m.Scopes[0] != "admin" {
		t.Errorf("Scopes = %v, want [admin]", m.Scopes)
	}
}

func TestConvertSecurityScheme_OAuth2NoFlows(t *testing.T) {
	// When no flows are present, should still return oauth2 type without panic.
	scheme := SecurityScheme{Type: "oauth2", Description: "bare"}
	m := convertSecurityScheme(scheme)
	if m.Type != "oauth2" {
		t.Errorf("Type = %q, want %q", m.Type, "oauth2")
	}
	if m.AuthorizeURL != "" {
		t.Errorf("AuthorizeURL = %q, want empty", m.AuthorizeURL)
	}
}
