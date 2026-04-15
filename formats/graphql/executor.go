// Package graphql implements the GraphQL binding format for OpenBindings.
//
// The package handles:
//   - Discovering GraphQL schemas via introspection
//   - Converting GraphQL types to OpenBindings interfaces
//   - Executing queries, mutations, and subscriptions
package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

const FormatToken = "graphql"
const DefaultSourceName = "graphql"

// Executor handles binding execution for GraphQL sources.
type Executor struct {
	mu       sync.RWMutex
	schemas  map[string]*introspectionSchema // endpoint URL -> cached schema
}

// NewExecutor creates a new GraphQL binding executor.
func NewExecutor() *Executor {
	return &Executor{schemas: make(map[string]*introspectionSchema)}
}

// Formats returns the source formats supported by the GraphQL executor.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "GraphQL APIs"}}
}

// cachedIntrospect returns a cached introspection result or performs a fresh introspection.
// The cache key is the normalized endpoint URL so that trailing slashes and other
// trivial differences don't cause redundant introspection calls.
func (e *Executor) cachedIntrospect(ctx context.Context, endpointURL string, headers map[string]string) (*introspectionSchema, error) {
	key := normalizeEndpoint(endpointURL)
	if key == "" {
		key = endpointURL
	}

	e.mu.RLock()
	if s, ok := e.schemas[key]; ok {
		e.mu.RUnlock()
		return s, nil
	}
	e.mu.RUnlock()

	schema, err := introspect(ctx, endpointURL, headers)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.schemas[key] = schema
	e.mu.Unlock()

	return schema, nil
}

// ExecuteBinding executes a GraphQL binding, returning a channel of stream events.
// For subscriptions it yields events as they arrive; for queries and mutations it
// returns a single event.
func (e *Executor) ExecuteBinding(ctx context.Context, in *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	enriched := e.enrichContext(ctx, in)
	start := time.Now()

	rootType, fieldName, err := parseRef(enriched.Ref)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidRef, err.Error())), nil
	}

	headers := buildHTTPHeaders(enriched.Context, enriched.Options)

	// If inline content is provided, parse it as an introspection result
	// instead of making a network introspection call.
	var schema *introspectionSchema
	if enriched.Source.Content != nil {
		s, err := parseIntrospectionContent(enriched.Source.Content)
		if err != nil {
			return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeSourceLoadFailed,
				fmt.Sprintf("parse inline GraphQL content: %v", err))), nil
		}
		schema = s
	} else {
		s, err := e.cachedIntrospect(ctx, enriched.Source.Location, headers)
		if err != nil {
			if isAuthError(err) {
				return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeAuthRequired, err.Error())), nil
			}
			return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeSourceLoadFailed, err.Error())), nil
		}
		schema = s
	}

	query, variables, err := buildQuery(schema, rootType, fieldName, enriched.Input, enriched.InputSchema)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound, err.Error())), nil
	}

	if rootType == "Subscription" {
		ch, err := subscribeGraphQL(ctx, enriched.Source.Location, query, variables, headers)
		if err != nil {
			return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeConnectFailed, err.Error())), nil
		}
		return ch, nil
	}

	result := executeGraphQL(ctx, enriched.Source.Location, query, variables, fieldName, headers, start)

	// Auth retry: if the call returned auth_required and we have security
	// methods and callbacks, resolve credentials and retry once.
	if result.Error != nil && result.Error.Code == openbindings.ErrCodeAuthRequired &&
		len(enriched.Security) > 0 && enriched.Callbacks != nil {
		creds, resolveErr := openbindings.ResolveSecurity(ctx, enriched.Security, enriched.Callbacks, nil)
		if resolveErr == nil && creds != nil {
			cp := *enriched
			enriched = &cp
			merged := make(map[string]any)
			for k, v := range enriched.Context {
				merged[k] = v
			}
			for k, v := range creds {
				merged[k] = v
			}
			enriched.Context = merged

			if enriched.Store != nil {
				storeKey := normalizeEndpoint(enriched.Source.Location)
				if storeKey != "" {
					_ = enriched.Store.Set(ctx, storeKey, enriched.Context)
				}
			}

			headers = buildHTTPHeaders(enriched.Context, enriched.Options)
			result = executeGraphQL(ctx, enriched.Source.Location, query, variables, fieldName, headers, start)
		}
	}

	return openbindings.SingleEventChannel(result), nil
}

// enrichContext merges stored context with the incoming binding execution input.
func (e *Executor) enrichContext(ctx context.Context, in *openbindings.BindingExecutionInput) *openbindings.BindingExecutionInput {
	if in.Store == nil {
		return in
	}
	key := normalizeEndpoint(in.Source.Location)
	if key == "" {
		return in
	}
	stored, err := in.Store.Get(ctx, key)
	if err != nil || len(stored) == 0 {
		return in
	}
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
	return &cp
}

// executeGraphQL sends a query/mutation over HTTP and returns the result.
func executeGraphQL(ctx context.Context, endpointURL, query string, variables map[string]any, fieldName string, headers map[string]string, start time.Time) *openbindings.ExecuteOutput {
	data, gqlErrors, err := doGraphQLHTTP(ctx, endpointURL, query, variables, headers)
	if err != nil {
		if he, ok := err.(*httpError); ok {
			return openbindings.HTTPErrorOutput(start, he.StatusCode, he.Error())
		}
		return openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error())
	}

	if len(gqlErrors) > 0 {
		msgs := make([]string, len(gqlErrors))
		for i, e := range gqlErrors {
			msgs[i] = e.Message
		}
		return &openbindings.ExecuteOutput{
			Status:     200,
			DurationMs: time.Since(start).Milliseconds(),
			Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeExecutionFailed,
				Message: strings.Join(msgs, "; "),
			},
		}
	}

	// Extract the field-specific data from the response.
	var output any
	if data != nil {
		output = data[fieldName]
	}

	return &openbindings.ExecuteOutput{Output: output, Status: 200, DurationMs: time.Since(start).Milliseconds()}
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
	disc, err := discover(ctx, endpoint, nil)
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

// normalizeEndpoint extracts a stable context key from a GraphQL endpoint URL.
func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return openbindings.NormalizeContextKey(u.Host)
	}
	return openbindings.NormalizeContextKey(endpoint)
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

func isAuthError(err error) bool {
	if he, ok := err.(*httpError); ok {
		return he.StatusCode == 401 || he.StatusCode == 403
	}
	return false
}
