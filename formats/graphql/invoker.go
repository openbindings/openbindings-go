// Package graphql implements the GraphQL binding format for OpenBindings.
//
// The package handles:
//   - Discovering GraphQL schemas via introspection
//   - Converting GraphQL types to OpenBindings interfaces
//   - Invoking queries, mutations, and subscriptions
package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

const FormatToken = "graphql"
const DefaultSourceName = "graphql"

// maxRedirects bounds the redirect chain a single request may follow.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

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

// Invoker handles binding invocation for GraphQL sources.
type Invoker struct {
	client  *http.Client
	mu      sync.RWMutex
	schemas map[string]*introspectionSchema // normalized endpoint -> cached schema
}

// NewInvoker creates a new GraphQL binding invoker.
func NewInvoker() *Invoker {
	return &Invoker{
		client:  newDefaultHTTPClient(),
		schemas: make(map[string]*introspectionSchema),
	}
}

// Formats returns the source formats supported by the GraphQL invoker.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "GraphQL APIs"}}
}

// cachedIntrospect returns a cached introspection result or performs a fresh
// introspection. The cache key is the FULL normalized endpoint
// (scheme://host/path): two GraphQL endpoints on one host must not share a
// schema; trailing-slash and host-case differences still collapse.
func (e *Invoker) cachedIntrospect(ctx context.Context, endpointURL string, headers map[string]string) (*introspectionSchema, error) {
	key := introspectionCacheKey(endpointURL)

	e.mu.RLock()
	if s, ok := e.schemas[key]; ok {
		e.mu.RUnlock()
		return s, nil
	}
	e.mu.RUnlock()

	schema, err := introspect(ctx, e.client, endpointURL, headers)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.schemas[key] = schema
	e.mu.Unlock()

	return schema, nil
}

var _ openbindings.BindingInvoker = (*Invoker)(nil)

// InvokeBinding invokes a GraphQL binding and returns the invocation handle
// synchronously; the work runs on its own goroutine. Queries and mutations
// yield one output; subscriptions yield one output per event. The variables
// object flows through the handle's Write channel.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

func (e *Invoker) run(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) {
	// Bound all I/O to the invocation's lifetime.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	rootType, fieldName, err := parseRef(args.Ref)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeInvalidRef, Message: err.Error()})
		return
	}

	headers := buildHTTPHeaders(args.Context)

	// Input is the GraphQL variables object. An operation-layer call for an
	// operation that declares no input (InputSchema == nil) closes input on
	// entry; otherwise read the single variables object the caller writes.
	var input any
	if args.Binding != nil && args.InputSchema == nil {
		_ = inv.CloseInput()
	} else {
		v, rerr := inv.ReadInput(bctx)
		switch {
		case errors.Is(rerr, io.EOF):
			// No variables written; dispatch with empty variables.
		case rerr != nil:
			inv.FireError(openbindings.AsInvocationError(rerr))
			return
		default:
			input = v
		}
		_ = inv.CloseInput()
	}

	// Build the query. A prebuilt `_query` const in the operation's input
	// schema skips introspection entirely; otherwise introspect (or parse
	// inline content) and synthesize the query from the field descriptor.
	var query string
	var variables map[string]any
	if q, ok := queryFromSchema(args.InputSchema); ok {
		query = q
		variables = inputToVariablesPassthrough(input)
	} else {
		schema, ierr := e.resolveSchema(bctx, args, headers)
		if ierr != nil {
			inv.FireError(ierr)
			return
		}
		q, vars, berr := buildQueryFromIntrospection(schema, rootType, fieldName, input)
		if berr != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeRefNotFound, Message: berr.Error()})
			return
		}
		query, variables = q, vars
	}

	if rootType == "Subscription" {
		streamSubscription(bctx, args.Source.Location, query, variables, headers, inv)
		return
	}

	// Query / mutation: unary HTTP dispatch.
	data, respHeaders, gqlErrors, err := doGraphQLHTTP(bctx, e.client, args.Source.Location, query, variables, headers)
	if err != nil {
		if bctx.Err() != nil {
			return
		}
		if he, ok := err.(*httpError); ok {
			ierr := openbindings.HTTPError(he.StatusCode, fmt.Sprintf("HTTP %d", he.StatusCode))
			if d, ok := ierr.Details.(map[string]any); ok && he.Body != "" {
				d["body"] = he.Body
			}
			inv.FireError(ierr)
			return
		}
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: err.Error()})
		return
	}

	_ = inv.SetHeader(toMetadata(respHeaders))

	if len(gqlErrors) > 0 {
		msgs := make([]string, len(gqlErrors))
		for i, ge := range gqlErrors {
			msgs[i] = ge.Message
		}
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: strings.Join(msgs, "; "),
			Details: map[string]any{"errors": gqlErrors},
		})
		return
	}

	var output any
	if data != nil {
		output = data[fieldName]
	}
	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// resolveSchema returns the introspection schema for the binding, from inline
// source content or via a (cached) network introspection.
func (e *Invoker) resolveSchema(ctx context.Context, args *openbindings.BindingInvocationArgs, headers map[string]string) (*introspectionSchema, *openbindings.InvocationError) {
	if args.Source.Content != nil {
		s, err := parseIntrospectionContent(args.Source.Content)
		if err != nil {
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceLoadFailed,
				Message: fmt.Sprintf("parse inline GraphQL content: %v", err),
			}
		}
		return s, nil
	}
	s, err := e.cachedIntrospect(ctx, args.Source.Location, headers)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && (he.StatusCode == 401 || he.StatusCode == 403) {
			// 401 → ERR_AUTH_REQUIRED, 403 → ERR_PERMISSION_DENIED (TS
			// parity), with the response body in Details for diagnosis.
			ierr := &openbindings.InvocationError{
				Code:    openbindings.HTTPErrorCode(he.StatusCode),
				Message: err.Error(),
				Details: map[string]any{"status": he.StatusCode},
			}
			if he.Body != "" {
				ierr.Details.(map[string]any)["body"] = he.Body
			}
			return nil, ierr
		}
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeSourceLoadFailed, Message: err.Error()}
	}
	return s, nil
}

// toMetadata clones HTTP response headers into invocation Metadata.
func toMetadata(h http.Header) openbindings.Metadata {
	md := make(openbindings.Metadata, len(h))
	for k, vs := range h {
		md[k] = append([]string(nil), vs...)
	}
	return md
}

// Creator handles interface creation from GraphQL endpoints.
type Creator struct{}

// NewCreator creates a new GraphQL interface creator.
func NewCreator() *Creator { return &Creator{} }

// Formats returns the source formats supported by the GraphQL creator.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "GraphQL APIs"}}
}

// CreateInterface introspects a GraphQL endpoint and converts to an OpenBindings interface.
func (c *Creator) CreateInterface(ctx context.Context, in *openbindings.CreateInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]
	endpoint := src.Location
	if endpoint == "" {
		return nil, fmt.Errorf("GraphQL source requires a location (endpoint URL)")
	}
	disc, err := discover(ctx, newDefaultHTTPClient(), endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("GraphQL introspection: %w", err)
	}
	iface, err := convertToInterface(disc.schema, endpoint)
	if err != nil {
		return nil, fmt.Errorf("GraphQL convert: %w", err)
	}
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

// introspectionCacheKey normalizes an endpoint URL to a schema cache key
// preserving the full target (scheme://host/path[?query]) — keying by host
// alone would let two endpoints on one host share a schema (wrong results).
// Host case and a trailing slash still collapse to one key.
func introspectionCacheKey(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	key := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key
}

// parseIntrospectionContent parses inline Source.Content as a GraphQL
// introspection result. Accepts the __schema object directly or wrapped
// in {"data": {"__schema": ...}} (the standard introspection response shape).
func parseIntrospectionContent(content any) (*introspectionSchema, error) {
	raw, err := openbindings.ContentToBytes(content)
	if err != nil {
		return nil, fmt.Errorf("convert content: %w", err)
	}

	// Try the full response shape first: {"data": {"__schema": ...}}
	var fullResponse struct {
		Data struct {
			Schema *introspectionSchema `json:"__schema"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &fullResponse); err == nil && fullResponse.Data.Schema != nil {
		return fullResponse.Data.Schema, nil
	}

	// Try the __schema object directly: {"__schema": ...}
	var wrapper struct {
		Schema *introspectionSchema `json:"__schema"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Schema != nil {
		return wrapper.Schema, nil
	}

	// Try as a bare introspectionSchema.
	var schema introspectionSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("unrecognized introspection content format")
	}
	if schema.QueryType == nil && len(schema.Types) == 0 {
		return nil, fmt.Errorf("unrecognized introspection content format")
	}
	return &schema, nil
}
