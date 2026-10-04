package httpdiscovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// WellKnownPath is relative to an origin, never to a resource path within it.
const WellKnownPath = "/.well-known/openbindings"

const defaultMaxDocumentBytes int64 = 1 << 20

const (
	statusBodyDrainLimit   = 2 << 10
	statusBodyDrainTimeout = 100 * time.Millisecond
)

var (
	// ErrNotFound means the endpoint answered 404. It never describes gated
	// discovery, a refused version, an invalid document, or an undecided check.
	ErrNotFound = errors.New("http discovery: no document published at this path")
	// ErrDocumentTooLarge means the response exceeded Client.MaxDocumentBytes.
	// It establishes nothing about the document's presence or conformance.
	ErrDocumentTooLarge = errors.New("http discovery: document exceeds size limit")
)

// StatusError reports any HTTP status other than 200. A 401 or 403 is gated
// discovery, not absence; Result.Header retains the authentication challenge.
// A 404 also matches ErrNotFound through errors.Is.
type StatusError struct{ StatusCode int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("http discovery: HTTP %d %s", e.StatusCode, http.StatusText(e.StatusCode))
}

func (e *StatusError) Is(target error) bool {
	return e.StatusCode == http.StatusNotFound && target == ErrNotFound
}

// Client retrieves documents from the HTTP discovery endpoint. Its zero value
// uses http.DefaultClient and a 1 MiB document limit. Configure it before use;
// an unchanged Client can be shared by concurrent calls.
type Client struct {
	// HTTPClient supplies transport, credentials, timeout, and redirect policy.
	// Nil uses http.DefaultClient, including its redirect limit and lack of an
	// overall timeout. Use a context deadline or configure Client.Timeout on the
	// supplied HTTP client. Scheme, network-range, and credential policies are
	// the application's, including on every redirect and network connection.
	// Discover does not modify this client or its transport.
	HTTPClient *http.Client
	// MaxDocumentBytes bounds the response body read from net/http, including
	// after its automatic decompression. Zero means 1 MiB; a negative value is
	// rejected. At most one extra byte is read to detect an oversized body.
	MaxDocumentBytes int64
}

// Result retains response metadata and, for a complete bounded 200 response,
// its exact bytes and core validation results. It is returned even alongside
// an error once an HTTP response is available. A transport error returns nil.
// Each call owns its result; the client retains no document or response cache.
type Result struct {
	RequestedURL string
	// FinalURL is empty if a custom RoundTripper omits Response.Request.URL.
	FinalURL   string
	StatusCode int
	Header     http.Header
	Body       []byte
	// Document is present when core can carry the body exactly. It can be
	// non-conformant when returned with an error; always check the error.
	Document *openbindings.Document
	// Report is zero until core validation runs, and on version refusal. A nil
	// error can accompany conformance-undetermined: use Report.Conclusion when
	// reporting conformance, never the fact that retrieval succeeded.
	Report openbindings.ValidationReport
}

// Endpoint constructs the well-known URL from an absolute HTTP(S) origin.
// A trailing slash is allowed; credentials, a resource path, query, and
// fragment are rejected. It never silently discards part of a resource URL.
func Endpoint(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("http discovery: invalid origin: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", errors.New("http discovery: origin must use HTTP or HTTPS")
	}
	if u.Hostname() == "" || u.Opaque != "" || u.User != nil ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") {
		return "", errors.New("http discovery: expected an origin without credentials, resource path, query, or fragment")
	}
	return (&url.URL{Scheme: strings.ToLower(u.Scheme), Host: u.Host, Path: WellKnownPath}).String(), nil
}

// Discover issues GET with the companion's Accept header. It accepts both OBI
// JSON and application/json; it also inspects bodies with other or missing
// Content-Type values. Media-type metadata never hides a version refusal.
// Only a 404 matches ErrNotFound. Other statuses, transport failures, limits,
// and invalid documents remain explicit errors (DISC-C-03 permits this).
// Short non-200 HTTP/1 bodies are discarded within a 2 KiB / 100 ms budget to
// permit connection reuse. Cleanup failure preserves the observed StatusError.
// As with other HTTP work, custom transports must honor request cancellation.
//
// A nil error means the body was decoded and no document-rule violation was
// established. Report can still be conformance-undetermined. When core cannot
// carry the body, an error matching openbindings.ErrInconclusive accompanies
// its report. Context cancellation interrupts HTTP work and is checked before
// and after core validation; core validation itself runs to completion.
func (c Client) Discover(ctx context.Context, origin string) (*Result, error) {
	limit := c.MaxDocumentBytes
	if limit < 0 {
		return nil, errors.New("http discovery: maximum document size must not be negative")
	}
	if limit == 0 {
		limit = defaultMaxDocumentBytes
	}
	endpoint, err := Endpoint(origin)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("http discovery: request: %w", err)
	}
	requestCtx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(requestCtx)
	req.Header.Set("Accept", openbindings.MediaType+", application/json")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http discovery: request: %w", err)
	}
	defer resp.Body.Close()
	result := &Result{
		RequestedURL: endpoint,
		StatusCode:   resp.StatusCode, Header: resp.Header.Clone(),
	}
	if resp.Request != nil && resp.Request.URL != nil {
		result.FinalURL = resp.Request.URL.String()
	}
	if resp.StatusCode != http.StatusOK {
		if resp.ProtoMajor < 2 && resp.ContentLength <= statusBodyDrainLimit {
			// Cancel only this request if an error body stalls. Read synchronously:
			// no background draining goroutine survives Discover. A transport must
			// honor the request context for response-body reads, as net/http does.
			timer := time.AfterFunc(statusBodyDrainTimeout, cancel)
			_, _ = io.CopyN(io.Discard, resp.Body, statusBodyDrainLimit)
			timer.Stop()
		}
		return result, &StatusError{StatusCode: resp.StatusCode}
	}
	readLimit := limit
	if readLimit < math.MaxInt64 {
		readLimit++
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, readLimit))
	if err != nil {
		return result, fmt.Errorf("http discovery: read response: %w", err)
	}
	if int64(len(body)) > limit {
		return result, fmt.Errorf("%w (%d bytes)", ErrDocumentTooLarge, limit)
	}
	result.Body = body
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("http discovery: %w", err)
	}
	result.Document, result.Report, err = openbindings.ValidateDocument(body)
	if err != nil {
		return result, fmt.Errorf("http discovery: document: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("http discovery: %w", err)
	}
	if result.Document == nil {
		return result, fmt.Errorf("http discovery: cannot represent the document: %w", openbindings.ErrInconclusive)
	}
	return result, nil
}
