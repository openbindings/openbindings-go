package httpdiscovery

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

// WellKnownPath is the path defined by the OpenBindings HTTP Discovery
// companion specification. It is relative to an origin, never to a resource
// path within that origin.
const WellKnownPath = "/.well-known/openbindings"

const defaultMaxDocumentBytes int64 = 1 << 20

// GatedError reports discovery protected by authentication or authorization.
// The endpoint may or may not publish an interface behind the gate.
type GatedError struct{ StatusCode int }

func (e *GatedError) Error() string {
	return fmt.Sprintf("http discovery: gated (HTTP %d)", e.StatusCode)
}

// VersionRefusalError reports a published document whose declared Core version
// this SDK refuses to process. It is distinct from an absent interface.
type VersionRefusalError struct{ Version string }

func (e *VersionRefusalError) Error() string {
	return fmt.Sprintf("http discovery: published interface declares unsupported version %q", e.Version)
}

// StatusError reports an HTTP response other than 200, 401, 403, or 404.
type StatusError struct{ StatusCode int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("http discovery: HTTP %d", e.StatusCode)
}

// Option configures one discovery request.
type Option func(*options)

type options struct {
	client   *http.Client
	maxBytes int64
}

// WithHTTPClient supplies transport, authentication, and redirect policy.
// A nil client uses http.DefaultClient.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) { o.client = client }
}

// WithMaxDocumentBytes sets the maximum response-body size. The default is
// 1 MiB. A non-positive limit is rejected before making a request.
func WithMaxDocumentBytes(limit int64) Option {
	return func(o *options) { o.maxBytes = limit }
}

// Endpoint returns the well-known URL for an HTTP(S) origin. The input must
// identify an origin, not a resource path: only an empty path or "/" is allowed.
func Endpoint(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("http discovery: invalid origin: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("http discovery: origin must use HTTP or HTTPS")
	}
	if u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("http discovery: expected an origin without credentials, path, query, or fragment")
	}
	return (&url.URL{Scheme: strings.ToLower(u.Scheme), Host: u.Host, Path: WellKnownPath}).String(), nil
}

// Discover retrieves a published interface from the origin's well-known path.
// A 404 returns (nil, false, nil). A 401/403, refused Core version, malformed
// response, transport failure, or other HTTP status returns an error. The
// returned document uses Core parsing; its retrieval URL is not its identity
// or a reference base. Callers may run additional Core validation as needed.
func Discover(ctx context.Context, origin string, opts ...Option) (*openbindings.Interface, bool, error) {
	o := options{client: http.DefaultClient, maxBytes: defaultMaxDocumentBytes}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.maxBytes <= 0 {
		return nil, false, fmt.Errorf("http discovery: maximum document size must be positive")
	}
	if o.client == nil {
		o.client = http.DefaultClient
	}
	endpoint, err := Endpoint(origin)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", openbindings.MediaType+", application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("http discovery: request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, false, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, false, &GatedError{StatusCode: resp.StatusCode}
	case http.StatusOK:
		// Continue below.
	default:
		return nil, false, &StatusError{StatusCode: resp.StatusCode}
	}

	readLimit := o.maxBytes
	if readLimit < math.MaxInt64 {
		readLimit++ // Read one extra byte to distinguish an oversized body.
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return nil, false, fmt.Errorf("http discovery: read response: %w", err)
	}
	if int64(len(body)) > o.maxBytes {
		return nil, false, fmt.Errorf("http discovery: interface document exceeds %d-byte limit", o.maxBytes)
	}
	var raw map[string]any
	if err := jsonvalue.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("http discovery: response is not JSON: %w", err)
	}
	if !openbindings.IsOBInterface(raw) {
		return nil, false, fmt.Errorf("http discovery: response is not an OBI document")
	}
	version := raw["openbindings"].(string) // IsOBInterface checked this type.
	if supported, err := openbindings.IsSupportedVersion(version); err == nil && !supported {
		return nil, false, &VersionRefusalError{Version: version}
	}
	iface, err := openbindings.ParseDocument(body)
	if err != nil {
		return nil, false, fmt.Errorf("http discovery: OBI response: %w", err)
	}
	return iface, true, nil
}
