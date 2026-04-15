package openbindings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrAuthCancelled is returned by ResolveSecurity when a platform callback
// signals that the user cancelled the authentication prompt. Callers can
// check for this with errors.Is to distinguish cancellation from failure.
var ErrAuthCancelled = errors.New("authentication cancelled")

// IsAuthCancelled reports whether err represents a user cancellation.
// Platform callback implementations should wrap or return ErrAuthCancelled
// to signal that the user dismissed the prompt.
func IsAuthCancelled(err error) bool {
	return errors.Is(err, ErrAuthCancelled)
}

// ResolveSecurity walks the given security methods in preference order and
// uses the available platform callbacks to interactively acquire credentials.
// Returns the acquired credentials as a context map (using well-known field
// names: bearerToken, apiKey, basic), or an error if no method could be resolved.
//
// If a callback returns ErrAuthCancelled (or a wrapped form of it), the entire
// resolution loop aborts immediately and returns the cancellation error.
//
// This is a utility function that can be called at any time -- on auth error,
// proactively before execution, from a CLI login command, or from any code
// that needs credentials for a set of security methods.
//
// Unknown method types are skipped. If no method can be resolved (because the
// required callbacks are unavailable or the user provides no value), an error
// is returned.
func ResolveSecurity(ctx context.Context, methods []SecurityMethod, callbacks *PlatformCallbacks, httpClient *http.Client) (map[string]any, error) {
	if len(methods) == 0 {
		return nil, fmt.Errorf("no security methods provided")
	}
	if callbacks == nil {
		return nil, fmt.Errorf("no platform callbacks available")
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	for _, method := range methods {
		creds, err := resolveMethod(ctx, method, callbacks, httpClient)
		if err != nil {
			if IsAuthCancelled(err) {
				return nil, err // user cancelled, abort immediately
			}
			continue // method couldn't be resolved, try next
		}
		if creds != nil {
			return creds, nil
		}
	}

	return nil, fmt.Errorf("no security method could be resolved (tried %d methods)", len(methods))
}

func resolveMethod(ctx context.Context, method SecurityMethod, callbacks *PlatformCallbacks, httpClient *http.Client) (map[string]any, error) {
	switch method.Type {
	case "bearer":
		return resolveBearerMethod(ctx, method, callbacks)
	case "oauth2":
		return resolveOAuth2Method(ctx, method, callbacks, httpClient)
	case "basic":
		return resolveBasicMethod(ctx, method, callbacks)
	case "apiKey":
		return resolveAPIKeyMethod(ctx, method, callbacks)
	default:
		return nil, fmt.Errorf("unknown security method type %q", method.Type)
	}
}

func resolveBearerMethod(ctx context.Context, method SecurityMethod, callbacks *PlatformCallbacks) (map[string]any, error) {
	if callbacks.Prompt == nil {
		return nil, fmt.Errorf("prompt callback not available")
	}

	desc := method.Description
	if desc == "" {
		desc = "Enter bearer token"
	}

	value, err := callbacks.Prompt(ctx, desc, &PromptOptions{
		Label:  "bearerToken",
		Secret: true,
	})
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, fmt.Errorf("empty bearer token")
	}

	return map[string]any{"bearerToken": value}, nil
}

func resolveOAuth2Method(ctx context.Context, method SecurityMethod, callbacks *PlatformCallbacks, httpClient *http.Client) (map[string]any, error) {
	// Try BrowserRedirect for the full PKCE flow
	if callbacks.BrowserRedirect != nil && method.AuthorizeURL != "" && method.TokenURL != "" {
		token, err := performPKCEFlow(ctx, method, callbacks.BrowserRedirect, httpClient)
		if err == nil {
			return map[string]any{"bearerToken": token}, nil
		}
		// Fall through to prompt if PKCE fails
	}

	// Fallback: prompt for bearer token
	if callbacks.Prompt == nil {
		return nil, fmt.Errorf("no callback available for OAuth2 authentication")
	}
	desc := method.Description
	if desc == "" {
		desc = "Enter OAuth2 bearer token"
	}
	value, err := callbacks.Prompt(ctx, desc, &PromptOptions{
		Label:  "bearerToken",
		Secret: true,
	})
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, fmt.Errorf("empty bearer token")
	}
	return map[string]any{"bearerToken": value}, nil
}

func performPKCEFlow(ctx context.Context, method SecurityMethod, browserRedirect func(context.Context, string) (*BrowserRedirectResult, error), httpClient *http.Client) (string, error) {
	// 1. Generate code verifier (32 random bytes, base64url encoded)
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", fmt.Errorf("generate code verifier: %w", err)
	}
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// 2. Generate code challenge (SHA256 of verifier, base64url encoded)
	challengeHash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])

	// 3. Generate state for CSRF protection
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)

	// 4. Build authorization URL
	authURL, err := url.Parse(method.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("parse authorize URL: %w", err)
	}
	params := authURL.Query()
	params.Set("response_type", "code")
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	if method.ClientID != "" {
		params.Set("client_id", method.ClientID)
	}
	if len(method.Scopes) > 0 {
		params.Set("scope", strings.Join(method.Scopes, " "))
	}
	authURL.RawQuery = params.Encode()

	// 5. Call BrowserRedirect
	result, err := browserRedirect(ctx, authURL.String())
	if err != nil {
		return "", fmt.Errorf("browser redirect: %w", err)
	}

	// 6. Parse callback URL and verify state before extracting code (CSRF first).
	callbackURL, err := url.Parse(result.CallbackURL)
	if err != nil {
		return "", fmt.Errorf("parse callback URL: %w", err)
	}
	callbackState := callbackURL.Query().Get("state")
	if callbackState != state {
		return "", fmt.Errorf("state mismatch in OAuth2 callback (CSRF protection)")
	}

	// 7. Extract authorization code.
	code := callbackURL.Query().Get("code")
	if code == "" {
		if errMsg := callbackURL.Query().Get("error"); errMsg != "" {
			errDesc := callbackURL.Query().Get("error_description")
			if errDesc != "" {
				return "", fmt.Errorf("authorization denied: %s: %s", errMsg, errDesc)
			}
			return "", fmt.Errorf("authorization denied: %s", errMsg)
		}
		return "", fmt.Errorf("no authorization code in callback URL")
	}

	// 8. Exchange code for token
	tokenParams := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {codeVerifier},
	}
	if method.ClientID != "" {
		tokenParams.Set("client_id", method.ClientID)
	}
	if result.RedirectURI != "" {
		tokenParams.Set("redirect_uri", result.RedirectURI)
	}

	tokenReq, err := http.NewRequestWithContext(ctx, "POST", method.TokenURL, strings.NewReader(tokenParams.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenReq.Header.Set("Accept", "application/json")

	tokenResp, err := httpClient.Do(tokenReq)
	if err != nil {
		return "", fmt.Errorf("token exchange request: %w", err)
	}
	defer func() { _ = tokenResp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(tokenResp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return "", fmt.Errorf("read token response: %w", err)
	}

	if tokenResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed: HTTP %d: %s", tokenResp.StatusCode, string(body))
	}

	var tokenResult struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tokenResult); err != nil {
		return "", fmt.Errorf("parse token response: %w", err)
	}
	if tokenResult.Error != "" {
		return "", fmt.Errorf("token exchange error: %s: %s", tokenResult.Error, tokenResult.ErrorDesc)
	}
	if tokenResult.AccessToken == "" {
		return "", fmt.Errorf("no access_token in token response")
	}

	return tokenResult.AccessToken, nil
}

func resolveBasicMethod(ctx context.Context, method SecurityMethod, callbacks *PlatformCallbacks) (map[string]any, error) {
	if callbacks.Prompt == nil {
		return nil, fmt.Errorf("prompt callback not available")
	}

	username, err := callbacks.Prompt(ctx, "Enter username", &PromptOptions{
		Label: "username",
	})
	if err != nil {
		return nil, err
	}

	password, err := callbacks.Prompt(ctx, "Enter password", &PromptOptions{
		Label:  "password",
		Secret: true,
	})
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"basic": map[string]any{
			"username": username,
			"password": password,
		},
	}, nil
}

func resolveAPIKeyMethod(ctx context.Context, method SecurityMethod, callbacks *PlatformCallbacks) (map[string]any, error) {
	if callbacks.Prompt == nil {
		return nil, fmt.Errorf("prompt callback not available")
	}

	desc := method.Description
	if desc == "" {
		desc = "Enter API key"
	}

	value, err := callbacks.Prompt(ctx, desc, &PromptOptions{
		Label:  "apiKey",
		Secret: true,
	})
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, fmt.Errorf("empty API key")
	}

	return map[string]any{"apiKey": value}, nil
}
