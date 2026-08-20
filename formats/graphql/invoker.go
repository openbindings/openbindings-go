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
	"sort"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

const BindingSpec = "openbindings.graphql@1"
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
//
// The introspection-schema cache is scoped to the Invoker instance (keyed
// by normalized endpoint) and lives as long as it does — scope instances
// per tenant to bound growth and avoid cross-tenant contamination in
// multi-tenant servers.
type Invoker struct {
	client  *http.Client
	mu      sync.RWMutex
	schemas map[string]*introspectionSchema // normalized endpoint -> cached schema
}

// NewInvoker creates a new GraphQL binding invoker with a default HTTP
// client. Use NewInvokerWithClient to inject a custom client (e.g., for
// tests, or to add a transport layer for tracing or auth).
func NewInvoker() *Invoker {
	return NewInvokerWithClient(nil)
}

// NewInvokerWithClient creates an Invoker that uses the supplied
// *http.Client for all outbound requests — queries, mutations,
// introspection, and the subscription WebSocket handshake. A nil client
// falls back to the default. The caller is responsible for configuring
// redirect policy, transport, and any other client-level behavior. No
// overall request timeout should be set on the client because the caller
// controls cancellation via context.
func NewInvokerWithClient(client *http.Client) *Invoker {
	if client == nil {
		client = newDefaultHTTPClient()
	}
	return &Invoker{
		client:  client,
		schemas: make(map[string]*introspectionSchema),
	}
}

// Formats returns the source formats supported by the GraphQL invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpec, Description: "GraphQL query and mutation application values"},
	}
}

// cachedIntrospect returns a cached introspection result or performs a fresh
// introspection. The cache key is the FULL normalized endpoint
// (scheme://host/path): two GraphQL endpoints on one host must not share a
// schema; trailing-slash and host-case differences still collapse.
func (e *Invoker) cachedIntrospect(ctx context.Context, endpointURL string, headers map[string]string) (*introspectionSchema, error) {
	key := introspectionCacheKey(endpointURL, headers)

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

var _ invoke.BindingInvoker = (*Invoker)(nil)
var _ invoke.BindingPreparer = (*Invoker)(nil)

// InvokeBinding invokes a GraphQL binding and returns the invocation handle
// synchronously; the work runs on its own goroutine. Queries and mutations
// yield one output; subscriptions yield one output per event. The variables
// object flows through the handle's Write channel.
func (e *Invoker) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	inv := invoke.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

func (e *Invoker) run(ctx context.Context, args *invoke.BindingInvocationArgs, inv *invoke.InvocationImpl[any, any]) {
	// Bound all I/O to the invocation's lifetime.
	bctx, stop := invoke.DoneContext(ctx, inv.Done())
	defer stop()

	rootType, fieldName, err := parseRef(args.Ref)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeInvalidRef})
		return
	}
	if args.Source.BindingSpec != BindingSpec {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
		return
	}
	if args.Source.BindingSpec == BindingSpec && rootType == "subscription" {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeInvalidRef})
		return
	}

	if err := validateHTTPLocation(args.Source.Location); err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
		return
	}

	cfg, err := readConfiguration(args.Context)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
		return
	}
	if cfg.Document == nil {
		inv.FireError(invoke.NewContextRequiredError(

			configurationRequirement(args.Source.Location, "document", "supply the exact GraphQL executable document and optional operationName"),
		))
		return
	}
	if rootType == "subscription" && cfg.SubscriptionTarget == "" {
		inv.FireError(invoke.NewContextRequiredError(

			configurationRequirement(args.Source.Location, "subscriptionTarget", "supply an absolute ws or wss GraphQL subscription target"),
		))
		return
	}
	document, err := parseExecutableDocument(cfg.Document.Source)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
		return
	}
	httpHeaders, err := cfg.httpHeaders(args.Context)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
		return
	}
	var websocketHeaders http.Header
	if rootType == "subscription" {
		websocketHeaders, err = cfg.websocketHeaders(args.Context)
		if err != nil {
			inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
			return
		}
	}

	schema, ierr := e.resolveSchema(bctx, args, httpHeaders)
	if ierr != nil {
		inv.FireError(ierr)
		return
	}
	if _, err := resolveField(schema, rootType, fieldName); err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeRefNotFound})
		return
	}

	var variables map[string]any
	if args.InputSchema == nil {
		_ = inv.CloseInput()
	} else {
		value, rerr := inv.ReadInput(bctx)
		switch {
		case errors.Is(rerr, io.EOF):
		case rerr != nil:
			inv.FireError(invoke.AsInvocationError(rerr))
			return
		default:
			object, ok := value.(map[string]any)
			if !ok {
				inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeValidationFailed})
				return
			}
			variables = object
		}
		_ = inv.CloseInput()
	}

	responseKey, err := document.responseKey(cfg.Document.OperationName, rootType, fieldName, variables, schema)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
		return
	}

	if rootType == "subscription" {

		streamSubscription(
			bctx, e.client, cfg.SubscriptionTarget, cfg.Document, variables,
			websocketHeaders, cfg.Protocol.ConnectionInit, cfg.Protocol.ConnectionInitSet,
			args.DeliveryUnitLimit(), inv,
		)
		return
	}

	result, err := doGraphQLHTTP(
		bctx, e.client, args.Source.Location, cfg.Document.Source,
		cfg.Document.OperationName, variables, httpHeaders, args.DeliveryUnitLimit(),
	)
	if err != nil {
		if bctx.Err() != nil {
			return
		}
		if he, ok := err.(*httpError); ok {

			inv.FireError(he.invocationError())
			return
		}
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeResponseError})
		return
	}

	emitProjectedGraphQLResult(inv, result, responseKey)
}

// PrepareBinding reports required configuration without parsing a source,
// reading caller input, introspecting, or dispatching.
func (e *Invoker) PrepareBinding(_ context.Context, args *invoke.BindingInvocationArgs) (*invoke.ContextRequiredDetails, error) {
	rootType, _, err := parseRef(args.Ref)
	if err != nil {
		return nil, nil
	}
	if err := validateHTTPLocation(args.Source.Location); err != nil {
		return nil, nil
	}
	cfg, err := readConfiguration(args.Context)
	if err != nil {
		return nil, err
	}
	if cfg.Document == nil {
		return configurationRequirement(args.Source.Location, "document", "supply the exact GraphQL executable document and optional operationName"), nil
	}
	if rootType == "subscription" && cfg.SubscriptionTarget == "" {
		return configurationRequirement(args.Source.Location, "subscriptionTarget", "supply an absolute ws or wss GraphQL subscription target"), nil
	}
	return nil, nil
}

// resolveSchema returns the introspection schema for the binding, from inline
// source content or via a (cached) network introspection.
func (e *Invoker) resolveSchema(ctx context.Context, args *invoke.BindingInvocationArgs, headers map[string]string) (*introspectionSchema, *invoke.InvocationError) {
	if args.Source.Content != nil {
		s, err := parseIntrospectionContent(args.Source.Content)
		if err != nil {
			return nil, &invoke.InvocationError{
				Code: invoke.ErrCodeSourceLoadFailed,
			}
		}
		return s, nil
	}
	s, err := e.cachedIntrospect(ctx, args.Source.Location, headers)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) {
			ierr := he.invocationError()
			if he.StatusCode != 401 && he.StatusCode != 403 {
				ierr.Code = invoke.ErrCodeSourceLoadFailed
			}
			return nil, ierr
		}
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
	}
	return s, nil
}

// Synthesizer handles interface synthesis from GraphQL endpoints.
type Synthesizer struct{}

// NewSynthesizer creates a new GraphQL interface synthesizer.
func NewSynthesizer() *Synthesizer { return &Synthesizer{} }

var (
	_ synthesize.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ synthesize.CoverageSynthesizer  = (*Synthesizer)(nil)
	_ synthesize.SourceInspector      = (*Synthesizer)(nil)
)

// Formats returns the source formats supported by the GraphQL synthesizer.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpec, Description: "GraphQL query and mutation application values"},
	}
}

// SynthesizeInterface introspects a GraphQL endpoint and converts to an OpenBindings interface.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	iface, _, err := c.synthesizeObserved(ctx, in)
	return iface, err
}

// SynthesizeInterfaceWithCoverage accounts for every non-introspection root
// field in the same schema observation that produced the OBI.
func (c *Synthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	iface, schema, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	if schema == nil {
		return synthesize.NewSynthesisResult(iface, []synthesize.SynthesisCoverageEntry{}, true)
	}
	return synthesize.NewSynthesisResult(iface, graphQLSynthesisCoverage(schema, iface, iface.Sources[DefaultSourceName].BindingSpec), true)
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *synthesize.SynthesizeInput) (*openbindings.Interface, *introspectionSchema, error) {
	if in == nil || len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		return &skeleton, nil, err
	}
	if len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		return &skeleton, nil, err
	}
	if len(in.Sources) > 1 {
		return nil, nil, synthesize.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.BindingSpec != BindingSpec {
		return nil, nil, fmt.Errorf("synthesizer supports exact binding specification %q, got %q", BindingSpec, src.BindingSpec)
	}
	endpoint := src.Location
	if err := validateHTTPLocation(endpoint); err != nil {
		return nil, nil, err
	}
	if src.OutputLocation != "" {
		if err := validateHTTPLocation(src.OutputLocation); err != nil {
			return nil, nil, fmt.Errorf("outputLocation: %w", err)
		}
	}

	var schema *introspectionSchema
	var artifactContent json.RawMessage
	var err error
	if src.Content != nil {
		schema, err = parseIntrospectionContent(src.Content)
		artifactContent = append(json.RawMessage(nil), src.Content...)
	} else {
		schema, err = introspect(ctx, newDefaultHTTPClient(), endpoint, nil)
		if err == nil && src.Embed {
			artifactContent, err = json.Marshal(map[string]any{"data": map[string]any{"__schema": schema}})
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("GraphQL introspection: %w", err)
	}
	iface, err := convertToInterface(schema, endpoint, src.BindingSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("GraphQL convert: %w", err)
	}
	if artifactContent != nil {
		entry := iface.Sources[DefaultSourceName]
		entry.Content = artifactContent
		iface.Sources[DefaultSourceName] = entry
	}
	if err := synthesize.FinalizeSynthesis(&iface, in, DefaultSourceName, src.BindingSpec); err != nil {
		return nil, nil, err
	}
	return &iface, schema, nil
}

func graphQLSynthesisCoverage(schema *introspectionSchema, iface *openbindings.Interface, bindingSpec string) []synthesize.SynthesisCoverageEntry {
	type item struct {
		key     string
		binding openbindings.BindingEntry
	}
	items := make([]item, 0, len(iface.Bindings))
	for key, binding := range iface.Bindings {
		items = append(items, item{key: key, binding: binding})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].binding.Ref < items[j].binding.Ref
	})
	entries := make([]synthesize.SynthesisCoverageEntry, 0, len(items))
	for _, item := range items {
		requirements := []string{"document"}
		if strings.HasPrefix(item.binding.Ref, "subscription/") {
			requirements = append(requirements, "subscriptionTarget")
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex:  0,
			SourceRef:    item.binding.Ref,
			Scope:        synthesize.SynthesisCoverageTarget,
			Status:       synthesize.SynthesisRepresented,
			OperationKey: item.binding.Operation,
			BindingKey:   item.key,
			BindingRef:   item.binding.Ref,
			Requirements: requirements,
		})
	}
	if bindingSpec == BindingSpec {
		tm := schema.typeMap()
		rootName := schema.rootTypeName("subscription")
		if root := tm[rootName]; root != nil {
			fields := append([]field(nil), root.Fields...)
			sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
			for _, field := range fields {
				if strings.HasPrefix(field.Name, "__") {
					continue
				}
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: "subscription/" + field.Name,
					Scope: synthesize.SynthesisCoverageTarget, Status: synthesize.SynthesisExcluded,
					ReasonCode: "graphql.subscription_lifecycle_not_representable", Rule: "GQL-P-04",
					Message: "subscription events may carry partial data and errors while the native stream continues; the first-revision candidate does not approximate that lifecycle",
				})
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].SourceRef < entries[j].SourceRef })
	}
	return entries
}

func emitProjectedGraphQLResult(inv *invoke.InvocationImpl[any, any], result *graphQLHTTPResult, responseKey string) {
	data, _ := result.Body["data"].(map[string]any)
	value, present := data[responseKey]
	_, hasErrors := result.Body["errors"]
	if present {
		if err := inv.EmitOutput(value); err != nil {
			return
		}
	}
	if hasErrors {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeExecutionFailed,
		})
		return
	}
	if !present {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeResponseError,
		})
		return
	}
	inv.CloseOutput()
}

func graphQLErrorMessage(response map[string]any) string {
	errors, _ := response["errors"].([]any)
	if len(errors) > 0 {
		if first, ok := errors[0].(map[string]any); ok {
			if message, ok := first["message"].(string); ok && message != "" {
				return message
			}
		}
	}
	return "GraphQL execution completed unsuccessfully"
}

// introspectionCacheKey normalizes an endpoint URL to a schema cache key
// preserving the full target (scheme://host/path[?query]) — keying by host
// alone would let two endpoints on one host share a schema (wrong results).
// Host case and a trailing slash still collapse to one key.
func introspectionCacheKey(endpoint string, headerSets ...map[string]string) string {
	var headers map[string]string
	if len(headerSets) > 0 {
		headers = headerSets[0]
	}
	endpoint = strings.TrimSpace(endpoint)
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	key := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	for _, name := range names {
		for actual, value := range headers {
			if strings.EqualFold(actual, name) {
				key += "\x00" + name + ":" + value
				break
			}
		}
	}
	return key
}

// parseIntrospectionContent parses inline Source.Content as a GraphQL
// introspection execution result. Revision 1 accepts only one successful
// result object with no errors member and object data.__schema.
func parseIntrospectionContent(content json.RawMessage) (*introspectionSchema, error) {
	if openbindings.ContentKind(content) != "object" {
		return nil, fmt.Errorf("content must be an introspection execution-result object, got %s", openbindings.ContentKind(content))
	}
	raw, err := openbindings.ContentToBytes(content)
	if err != nil {
		return nil, fmt.Errorf("convert content: %w", err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse introspection execution result: %w", err)
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("content must be an introspection execution-result object")
	}
	if _, hasErrors := result["errors"]; hasErrors {
		return nil, fmt.Errorf("introspection content must not contain an errors member")
	}
	data, ok := result["data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("introspection content must contain object data")
	}
	schemaValue, ok := data["__schema"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("introspection content must contain object data.__schema")
	}
	schemaRaw, _ := json.Marshal(schemaValue)
	var schema introspectionSchema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return nil, fmt.Errorf("parse data.__schema: %w", err)
	}
	if len(schema.Types) == 0 {
		return nil, fmt.Errorf("introspection content data.__schema has no types")
	}
	return &schema, nil
}
