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

// FetchedInterface is the result of FetchInterface.
type FetchedInterface struct {
	Interface *Interface
	// Synthesized is true when the OBI was synthesized from a non-OBI
	// source (e.g. an OpenAPI document) via a creator.
	Synthesized bool
}

// FetchOption configures FetchInterface.
type FetchOption func(*fetchOptions)

type fetchOptions struct {
	client   *http.Client
	creators []InterfaceCreator
}

// WithFetchHTTPClient sets a custom HTTP client for the fetch.
func WithFetchHTTPClient(c *http.Client) FetchOption {
	return func(o *fetchOptions) { o.client = c }
}

// WithCreators provides creators for synthesizing OBIs from non-OBI
// sources (OpenAPI, AsyncAPI, etc.). When the URL doesn't serve an
// OBI directly and well-known discovery fails, each creator is tried
// in turn.
func WithCreators(creators ...InterfaceCreator) FetchOption {
	return func(o *fetchOptions) { o.creators = creators }
}

// FetchInterface resolves an OBI from a URL. For HTTP URLs, it tries
// a direct fetch first, then well-known discovery at
// /.well-known/openbindings. If neither yields an OBI and creators are
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

	if len(o.creators) == 0 {
		return nil, fmt.Errorf("no OBI available at %s and no creators supplied for synthesis", sanitizeURL(target))
	}

	combined := CombineCreators(o.creators...)
	var lastErr error
	for _, fi := range combined.Formats() {
		iface, err := combined.CreateInterface(ctx, &CreateInput{
			Sources: []CreateSource{{Format: fi.Token, Location: target}},
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
	return nil, fmt.Errorf("no creator could synthesize an interface from %s", sanitizeURL(target))
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
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
