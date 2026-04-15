package openbindings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// mockPrompt creates a Prompt callback that returns pre-configured responses
// keyed by PromptOptions.Label.
func mockPrompt(responses map[string]string) func(ctx context.Context, msg string, opts *PromptOptions) (string, error) {
	return func(ctx context.Context, msg string, opts *PromptOptions) (string, error) {
		if v, ok := responses[opts.Label]; ok {
			return v, nil
		}
		return "", fmt.Errorf("unexpected prompt for %q", opts.Label)
	}
}

// ---------------------------------------------------------------------------
// ResolveSecurity tests
// ---------------------------------------------------------------------------

func TestResolveSecurity_Bearer(t *testing.T) {
	creds, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "bearer", Description: "API token"},
	}, &PlatformCallbacks{
		Prompt: mockPrompt(map[string]string{"bearerToken": "tok123"}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds["bearerToken"] != "tok123" {
		t.Errorf("expected tok123, got %v", creds["bearerToken"])
	}
}

func TestResolveSecurity_APIKey(t *testing.T) {
	creds, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "apiKey", Description: "Your API key"},
	}, &PlatformCallbacks{
		Prompt: mockPrompt(map[string]string{"apiKey": "key456"}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds["apiKey"] != "key456" {
		t.Errorf("expected key456, got %v", creds["apiKey"])
	}
}

func TestResolveSecurity_Basic(t *testing.T) {
	creds, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "basic"},
	}, &PlatformCallbacks{
		Prompt: mockPrompt(map[string]string{
			"username": "alice",
			"password": "secret",
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	basic, ok := creds["basic"].(map[string]any)
	if !ok {
		t.Fatalf("expected basic map, got %T", creds["basic"])
	}
	if basic["username"] != "alice" {
		t.Errorf("expected alice, got %v", basic["username"])
	}
	if basic["password"] != "secret" {
		t.Errorf("expected secret, got %v", basic["password"])
	}
}

func TestResolveSecurity_OAuth2Fallback(t *testing.T) {
	creds, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "oauth2", Description: "OAuth2 flow"},
	}, &PlatformCallbacks{
		Prompt: mockPrompt(map[string]string{"bearerToken": "oauth-tok"}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds["bearerToken"] != "oauth-tok" {
		t.Errorf("expected oauth-tok, got %v", creds["bearerToken"])
	}
}

func TestResolveSecurity_OAuth2PKCE(t *testing.T) {
	// Set up a mock token server
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.FormValue("grant_type"))
		}
		if r.FormValue("code") == "" {
			t.Error("missing code")
		}
		if r.FormValue("code_verifier") == "" {
			t.Error("missing code_verifier")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"access_token": "oauth_token_123",
			"token_type":   "Bearer",
		})
	}))
	defer tokenServer.Close()

	methods := []SecurityMethod{{
		Type:         "oauth2",
		AuthorizeURL: "https://auth.example.com/authorize",
		TokenURL:     tokenServer.URL,
	}}

	callbacks := &PlatformCallbacks{
		BrowserRedirect: func(ctx context.Context, authURL string) (*BrowserRedirectResult, error) {
			// Parse the auth URL to extract the state
			u, _ := url.Parse(authURL)
			state := u.Query().Get("state")
			// Simulate successful authorization
			return &BrowserRedirectResult{
				CallbackURL: fmt.Sprintf("http://localhost:0/callback?code=auth_code_abc&state=%s", state),
				RedirectURI: "http://localhost:0/callback",
			}, nil
		},
	}

	creds, err := ResolveSecurity(context.Background(), methods, callbacks, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds["bearerToken"] != "oauth_token_123" {
		t.Errorf("bearerToken = %v, want oauth_token_123", creds["bearerToken"])
	}
}

func TestResolveSecurity_UnknownTypeSkipped(t *testing.T) {
	creds, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "magic_sso"},
		{Type: "bearer"},
	}, &PlatformCallbacks{
		Prompt: mockPrompt(map[string]string{"bearerToken": "fallback"}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds["bearerToken"] != "fallback" {
		t.Errorf("expected fallback, got %v", creds["bearerToken"])
	}
}

func TestResolveSecurity_NoMethods(t *testing.T) {
	_, err := ResolveSecurity(context.Background(), nil, &PlatformCallbacks{}, nil)
	if err == nil {
		t.Fatal("expected error for empty methods")
	}
}

func TestResolveSecurity_NilCallbacks(t *testing.T) {
	_, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "bearer"},
	}, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil callbacks")
	}
}

func TestResolveSecurity_EmptyPromptSkipsMethod(t *testing.T) {
	_, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "bearer"},
	}, &PlatformCallbacks{
		Prompt: mockPrompt(map[string]string{"bearerToken": ""}),
	}, nil)
	if err == nil {
		t.Fatal("expected error when prompt returns empty string")
	}
}

func TestResolveSecurity_PreferenceOrder(t *testing.T) {
	// First successful method wins. Bearer is first so it should be used,
	// not apiKey.
	creds, err := ResolveSecurity(context.Background(), []SecurityMethod{
		{Type: "bearer"},
		{Type: "apiKey"},
	}, &PlatformCallbacks{
		Prompt: mockPrompt(map[string]string{
			"bearerToken": "first-wins",
			"apiKey":      "second",
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds["bearerToken"] != "first-wins" {
		t.Errorf("expected first-wins, got %v", creds["bearerToken"])
	}
	if _, hasAPIKey := creds["apiKey"]; hasAPIKey {
		t.Error("apiKey should not be present when bearer succeeded first")
	}
}

// ---------------------------------------------------------------------------
// SecurityMethod JSON round-trip
// ---------------------------------------------------------------------------

func TestSecurityMethod_JSONRoundTrip(t *testing.T) {
	original := SecurityMethod{
		Type:         "oauth2",
		Description:  "OAuth2 flow",
		AuthorizeURL: "https://auth.example.com/authorize",
		TokenURL:     "https://auth.example.com/token",
		Scopes:       []string{"read", "write"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SecurityMethod
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type: got %q, want %q", decoded.Type, original.Type)
	}
	if decoded.Description != original.Description {
		t.Errorf("Description: got %q, want %q", decoded.Description, original.Description)
	}
	if decoded.AuthorizeURL != original.AuthorizeURL {
		t.Errorf("AuthorizeURL: got %q, want %q", decoded.AuthorizeURL, original.AuthorizeURL)
	}
	if decoded.TokenURL != original.TokenURL {
		t.Errorf("TokenURL: got %q, want %q", decoded.TokenURL, original.TokenURL)
	}
	if len(decoded.Scopes) != len(original.Scopes) {
		t.Fatalf("Scopes length: got %d, want %d", len(decoded.Scopes), len(original.Scopes))
	}
	for i, s := range decoded.Scopes {
		if s != original.Scopes[i] {
			t.Errorf("Scopes[%d]: got %q, want %q", i, s, original.Scopes[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Security pass-through via ExecuteOperation
// ---------------------------------------------------------------------------

func TestExecuteOperation_SecurityPassThrough(t *testing.T) {
	var capturedSecurity []SecurityMethod

	executor := &mockExecutor{
		formats: []FormatInfo{{Token: "test"}},
		executeFn: func(_ context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
			capturedSecurity = in.Security
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Data: "ok"}
			close(ch)
			return ch, nil
		},
	}

	exec := NewOperationExecutor(executor)

	iface := &Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]Operation{
			"getUser": {},
		},
		Sources: map[string]Source{
			"api": {Format: "test", Location: "https://api.example.com"},
		},
		Bindings: map[string]BindingEntry{
			"getUser.api": {Operation: "getUser", Source: "api", Ref: "#/paths/users/get", Security: "default"},
		},
		Security: map[string][]SecurityMethod{
			"default": {
				{Type: "bearer", Description: "Bearer token"},
				{Type: "apiKey", Description: "API key", Name: "X-API-Key", In: "header"},
			},
		},
	}

	ch, err := exec.ExecuteOperation(context.Background(), &OperationExecutionInput{
		Interface: iface,
		Operation: "getUser",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	if len(capturedSecurity) != 2 {
		t.Fatalf("expected 2 security methods, got %d", len(capturedSecurity))
	}
	if capturedSecurity[0].Type != "bearer" {
		t.Errorf("expected bearer, got %q", capturedSecurity[0].Type)
	}
	if capturedSecurity[1].Type != "apiKey" {
		t.Errorf("expected apiKey, got %q", capturedSecurity[1].Type)
	}
	if capturedSecurity[1].Name != "X-API-Key" {
		t.Errorf("expected X-API-Key, got %q", capturedSecurity[1].Name)
	}
	if capturedSecurity[1].In != "header" {
		t.Errorf("expected header, got %q", capturedSecurity[1].In)
	}
}

// ---------------------------------------------------------------------------
// HTTPErrorOutput / httpErrorCode tests
// ---------------------------------------------------------------------------

func TestHTTPErrorCode_401(t *testing.T) {
	out := HTTPErrorOutput(time.Now(), 401, "401 Unauthorized")
	if out.Error == nil {
		t.Fatal("expected error")
	}
	if out.Error.Code != ErrCodeAuthRequired {
		t.Errorf("expected %s, got %s", ErrCodeAuthRequired, out.Error.Code)
	}
	if out.Status != 401 {
		t.Errorf("expected 401, got %d", out.Status)
	}
}

func TestHTTPErrorCode_403(t *testing.T) {
	out := HTTPErrorOutput(time.Now(), 403, "403 Forbidden")
	if out.Error == nil {
		t.Fatal("expected error")
	}
	if out.Error.Code != ErrCodePermissionDenied {
		t.Errorf("expected %s, got %s", ErrCodePermissionDenied, out.Error.Code)
	}
	if out.Status != 403 {
		t.Errorf("expected 403, got %d", out.Status)
	}
}

func TestHTTPErrorCode_500(t *testing.T) {
	out := HTTPErrorOutput(time.Now(), 500, "500 Internal Server Error")
	if out.Error == nil {
		t.Fatal("expected error")
	}
	if out.Error.Code != ErrCodeExecutionFailed {
		t.Errorf("expected %s, got %s", ErrCodeExecutionFailed, out.Error.Code)
	}
	if out.Status != 500 {
		t.Errorf("expected 500, got %d", out.Status)
	}
}
