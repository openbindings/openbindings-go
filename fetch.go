package openbindings

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxFetchBytes caps how much of a fetched interface document is read
// (1 MiB — matched byte-for-byte by the TS SDK). Exceeding it is a loud
// error, never a truncation.
const maxFetchBytes = 1 << 20

// FetchedInterface is the result of FetchInterface.
type FetchedInterface struct {
	Interface *Interface
	// Synthesized is true when the OBI was synthesized from a non-OBI
	// source (e.g. an OpenAPI document) via a synthesizer.
	Synthesized bool
}

// FetchOption configures FetchInterface.
type FetchOption func(*fetchOptions)

type fetchOptions struct {
	client       *http.Client
	synthesizers []InterfaceSynthesizer
}

// WithFetchHTTPClient sets a custom HTTP client for the fetch.
func WithFetchHTTPClient(c *http.Client) FetchOption {
	return func(o *fetchOptions) { o.client = c }
}

// WithSynthesizers provides synthesizers for synthesizing OBIs from non-OBI
// sources (OpenAPI, AsyncAPI, etc.). When the URL doesn't serve an
// OBI directly and well-known discovery fails, each synthesizer is tried
// in turn.
func WithSynthesizers(synthesizers ...InterfaceSynthesizer) FetchOption {
	return func(o *fetchOptions) { o.synthesizers = synthesizers }
}

// FetchInterface resolves an OBI from a URL. For HTTP URLs, it tries
// a direct fetch first, then well-known discovery at
// /.well-known/openbindings. If neither yields an OBI and synthesizers are
// supplied, it synthesizes from the URL's content (e.g. an OpenAPI doc).
//
// Returns an error if the OBI cannot be acquired.
func FetchInterface(ctx context.Context, target string, opts ...FetchOption) (*FetchedInterface, error) {
	o := &fetchOptions{client: http.DefaultClient}
	for _, opt := range opts {
		opt(o)
	}

	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("openbindings: FetchInterface: empty target")
	}

	if IsHTTPURL(target) {
		iface, err := tryFetchOBI(ctx, o.client, target)
		if err == nil && iface != nil {
			return &FetchedInterface{Interface: iface}, nil
		}

		if !shouldSkipWellKnownDiscovery(target) {
			wellKnown := strings.TrimRight(target, "/") + WellKnownPath
			iface, err := tryFetchOBI(ctx, o.client, wellKnown)
			if err == nil && iface != nil {
				return &FetchedInterface{Interface: iface}, nil
			}
		}
	}

	if len(o.synthesizers) == 0 {
		return nil, fmt.Errorf("no OBI available at %s and no synthesizers supplied for synthesis", sanitizeURL(target))
	}

	combined := CombineSynthesizers(o.synthesizers...)
	var lastErr error
	for _, fi := range combined.Formats() {
		iface, err := combined.SynthesizeInterface(ctx, &SynthesizeInput{
			Sources: []SynthesizeSource{{Format: fi.Token, Location: target}},
		})
		if err != nil {
			lastErr = err
			continue
		}
		if iface != nil && len(iface.Operations) > 0 {
			return &FetchedInterface{Interface: iface, Synthesized: true}, nil
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no synthesizer could synthesize an interface from %s", sanitizeURL(target))
}

// tryFetchOBI attempts to fetch and parse a URL as an OBI document.
// Returns nil (no error) when the URL returns valid JSON that isn't
// an OBI document — letting the caller fall through to discovery /
// synthesis.
func tryFetchOBI(ctx context.Context, client *http.Client, target string) (*Interface, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", MediaType+", application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Cap the fetch at maxFetchBytes, LOUDLY: read one byte past the cap so
	// an oversized document is a clear size error, never a silent truncation
	// that surfaces as a confusing parse failure.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxFetchBytes {
		return nil, fmt.Errorf("interface document exceeds the %d-byte fetch cap", maxFetchBytes)
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	if !IsOBInterface(raw) {
		return nil, nil
	}

	return ParseDocument(body)
}

func sanitizeURL(u string) string {
	if idx := strings.Index(u, "?"); idx >= 0 {
		return u[:idx]
	}
	return u
}

func shouldSkipWellKnownDiscovery(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	return strings.HasSuffix(path, ".json") ||
		strings.HasSuffix(path, ".yaml") ||
		strings.HasSuffix(path, ".yml") ||
		strings.Contains(path, "/openapi") ||
		strings.Contains(path, "/swagger") ||
		strings.Contains(path, "/asyncapi") ||
		strings.HasSuffix(path, WellKnownPath)
}
