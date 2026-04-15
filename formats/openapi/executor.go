// Package openapi implements the OpenAPI binding format for OpenBindings.
//
// The package handles:
//   - Converting OpenAPI 3.x documents to OpenBindings interfaces
//   - Executing operations via HTTP requests
//   - Describing context requirements via getContextSchema
package openapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// FormatToken identifies this package as an OpenAPI 3.x handler.
const FormatToken = "openapi@^3.0.0"

// DefaultSourceName is the default source key used when registering an OpenAPI source in an OBI.
const DefaultSourceName = "openapi"

// maxRedirects bounds the redirect chain a single HTTP request may follow.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

// newDefaultHTTPClient constructs an HTTP client with the executor's default
// redirect policy and no overall timeout (the caller controls cancellation
// via context). Each Executor gets its own client so multiple Executors can
// be configured independently and tests can substitute clients without
// reaching into package-level globals.
func newDefaultHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// Executor handles binding execution for OpenAPI 3.x sources.
//
// Each Executor owns an HTTP client (*http.Client is safe for concurrent use
// by multiple goroutines, so all calls on a single Executor share one client)
// and a per-instance document cache keyed by source location. The cache is
// scoped to the Executor instance to avoid cross-tenant contamination in
// multi-tenant servers.
type Executor struct {
	client   *http.Client
	mu       sync.RWMutex
	docCache map[string]*openapi3.T
}

// NewExecutor creates a new OpenAPI binding executor with a default HTTP
// client. Use NewExecutorWithClient to inject a custom client (e.g., for
// tests, or to add a transport layer for tracing or auth).
func NewExecutor() *Executor {
	return &Executor{
		client:   newDefaultHTTPClient(),
		docCache: make(map[string]*openapi3.T),
	}
}

// NewExecutorWithClient creates an Executor that uses the supplied
// *http.Client for all outbound requests. The caller is responsible for
// configuring redirect policy, transport, and any other client-level
// behavior. No overall request timeout should be set on the client because
// the caller controls cancellation via context.
func NewExecutorWithClient(client *http.Client) *Executor {
	if client == nil {
		client = newDefaultHTTPClient()
	}
	return &Executor{
		client:   client,
		docCache: make(map[string]*openapi3.T),
	}
}

// cachedLoadDocument loads an OpenAPI doc, caching by location within a process.
// When content is provided, the cache is bypassed and updated with the fresh parse.
func (e *Executor) cachedLoadDocument(location string, content any) (*openapi3.T, error) {
	if location != "" && content == nil {
		e.mu.RLock()
		if doc, ok := e.docCache[location]; ok {
			e.mu.RUnlock()
			return doc, nil
		}
		e.mu.RUnlock()
	}

	doc, err := loadDocument(location, content)
	if err != nil {
		return nil, err
	}

	if location != "" {
		e.mu.Lock()
		e.docCache[location] = doc
		e.mu.Unlock()
	}
	return doc, nil
}

// Formats returns the binding format tokens this executor supports.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "OpenAPI 3.x HTTP APIs"}}
}

// ExecuteBinding executes an HTTP request based on an OpenAPI binding.
func (e *Executor) ExecuteBinding(ctx context.Context, in *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	doc, err := e.cachedLoadDocument(in.Source.Location, in.Source.Content)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeSourceLoadFailed, err.Error())), nil
	}

	enriched := in
	if in.Store != nil {
		key, keyErr := resolveServerKey(doc, in.Source.Location)
		if keyErr == nil && key != "" {
			if stored, sErr := in.Store.Get(ctx, key); sErr == nil && len(stored) > 0 {
				cp := *in
				if len(in.Context) == 0 {
					cp.Context = stored
				} else {
					merged := make(map[string]any, len(stored)+len(in.Context))
					for k, v := range stored {
						merged[k] = v
					}
					for k, v := range in.Context {
						merged[k] = v
					}
					cp.Context = merged
				}
				enriched = &cp
			}
		}
	}

	result, stream := executeBindingWithDoc(ctx, e.client, enriched, doc)
	if stream != nil {
		// SSE response: hand the streaming channel directly to the caller.
		// Auth retry is not attempted on streaming responses because the
		// server has already begun emitting events; an auth failure on a
		// streaming endpoint would surface as a non-2xx status BEFORE the
		// stream begins, in which case `stream` is nil and we fall through
		// to the unary code path below.
		return stream, nil
	}

	// Auth retry: if the API returned auth_required and we have security methods
	// and callbacks, resolve credentials and retry once.
	if result.Error != nil && result.Error.Code == openbindings.ErrCodeAuthRequired &&
		len(enriched.Security) > 0 && enriched.Callbacks != nil {
		creds, resolveErr := openbindings.ResolveSecurity(ctx, enriched.Security, enriched.Callbacks, nil)
		if resolveErr == nil && creds != nil {
			if enriched == in {
				cp := *in
				enriched = &cp
			}
			merged := make(map[string]any)
			for k, v := range enriched.Context {
				merged[k] = v
			}
			for k, v := range creds {
				merged[k] = v
			}
			enriched.Context = merged

			if enriched.Store != nil {
				storeKey, keyErr := resolveServerKey(doc, enriched.Source.Location)
				if keyErr == nil && storeKey != "" {
					_ = enriched.Store.Set(ctx, storeKey, enriched.Context)
				}
			}

			retryResult, retryStream := executeBindingWithDoc(ctx, e.client, enriched, doc)
			if retryStream != nil {
				return retryStream, nil
			}
			result = retryResult
		}
	}

	return openbindings.SingleEventChannel(result), nil
}

// Creator handles interface creation from OpenAPI documents.
type Creator struct{}

// NewCreator creates a new OpenAPI interface creator.
func NewCreator() *Creator {
	return &Creator{}
}

// Formats returns the format tokens this creator supports.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "OpenAPI 3.x HTTP APIs"}}
}

// CreateInterface converts an OpenAPI document to an OpenBindings interface.
func (c *Creator) CreateInterface(ctx context.Context, in *openbindings.CreateInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]
	doc, err := loadDocument(src.Location, src.Content)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	iface := convertDocToInterface(doc, src.Location)
	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}
	return &iface, nil
}

// resolveServerKey extracts the API identity URL from the OpenAPI doc's servers array
// and normalizes it to a stable context store key.
func resolveServerKey(doc *openapi3.T, sourceLocation string) (string, error) {
	if len(doc.Servers) == 0 || doc.Servers[0].URL == "" {
		return "", fmt.Errorf("OpenAPI document has no servers array; cannot determine API identity for context lookup")
	}
	serverURL := doc.Servers[0].URL
	if strings.HasPrefix(serverURL, "http://") || strings.HasPrefix(serverURL, "https://") {
		return openbindings.NormalizeContextKey(strings.TrimRight(serverURL, "/")), nil
	}
	if openbindings.IsHTTPURL(sourceLocation) {
		parsed, err := url.Parse(sourceLocation)
		if err == nil {
			origin := parsed.Scheme + "://" + parsed.Host
			return openbindings.NormalizeContextKey(strings.TrimRight(origin+serverURL, "/")), nil
		}
	}
	return "", fmt.Errorf("OpenAPI server URL %q is relative and source location is not an HTTP URL", serverURL)
}
