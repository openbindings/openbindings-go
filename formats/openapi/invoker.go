// Package openapi implements the OpenAPI binding format for OpenBindings.
//
// The package handles:
//   - Converting native Swagger 2.0 and OpenAPI 3.x documents to OpenBindings interfaces
//   - Invoking operations via HTTP requests through the Invocation handle
//   - Deriving context requirements (CONTEXT_REQUIRED negotiation) from the
//     document's securitySchemes
package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
)

// Registered OpenAPI binding-specification identifiers. Swagger 2.0 and
// OpenAPI 3.0/3.1/3.2 are complete and warranted.
const (
	BindingSpecOpenAPI20 = "openbindings.openapi-2.0@1"
	BindingSpecOpenAPI30 = "openbindings.openapi-3.0@1"
	BindingSpecOpenAPI31 = "openbindings.openapi-3.1@1"
	BindingSpecOpenAPI32 = "openbindings.openapi-3.2@1"

	// ErrCodeUnsupportedBindingSpec reports a registered family whose engine
	// is absent, or a binding invocation that names no exact family token.
	ErrCodeUnsupportedBindingSpec = "ERR_UNSUPPORTED_BINDING_SPEC"
)

type openAPIBindingSpecRegistration struct {
	editions           map[string]bool
	requestImplemented bool
	responseComplete   bool
}

var openAPIBindingSpecRegistry = map[string]openAPIBindingSpecRegistration{
	BindingSpecOpenAPI20: {
		requestImplemented: true,
		responseComplete:   true,
		editions:           map[string]bool{"2.0": true},
	},
	BindingSpecOpenAPI30: {
		requestImplemented: true,
		responseComplete:   true,
		editions: map[string]bool{
			"3.0.0": true, "3.0.1": true, "3.0.2": true,
			"3.0.3": true, "3.0.4": true,
		},
	},
	BindingSpecOpenAPI31: {
		requestImplemented: true,
		responseComplete:   true,
		editions:           map[string]bool{"3.1.0": true, "3.1.1": true, "3.1.2": true},
	},
	BindingSpecOpenAPI32: {
		requestImplemented: true,
		responseComplete:   true,
		editions:           map[string]bool{"3.2.0": true},
	},
}

// Numbered "revision" labels retained in some internal helper/test filenames
// are development-history markers, not published binding-spec revisions.

func hasRoutedInputs(bindingSpec string) bool {
	return isRequestImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasMediaFidelity(bindingSpec string) bool {
	return isRequestImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasResponseFidelity(bindingSpec string) bool {
	registration, ok := openAPIBindingSpecRegistry[bindingSpec]
	return ok && registration.responseComplete
}

func hasDynamicObjectCarriage(bindingSpec string) bool {
	return isRequestImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasWholeJSONCarriage(bindingSpec string) bool {
	return isRequestImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasSchemaOmittedOAS30ByteCarriage(bindingSpec string) bool {
	return isRequestImplementedOpenAPIBindingSpec(bindingSpec)
}

// isImplementedOpenAPIBindingSpec is the response-complete warranting gate
// consumed by capability advertisement and checkBindingSpecs.
func isImplementedOpenAPIBindingSpec(bindingSpec string) bool {
	registration, ok := openAPIBindingSpecRegistry[bindingSpec]
	return ok && registration.responseComplete
}

func isRequestImplementedOpenAPIBindingSpec(bindingSpec string) bool {
	registration, ok := openAPIBindingSpecRegistry[bindingSpec]
	return ok && registration.requestImplemented
}

func openAPIRule(bindingSpec, rule string) string {
	switch bindingSpec {
	case BindingSpecOpenAPI20:
		return "OAPI20-" + rule
	case BindingSpecOpenAPI30:
		return "OAPI30-" + rule
	case BindingSpecOpenAPI31:
		return "OAPI31-" + rule
	case BindingSpecOpenAPI32:
		return "OAPI32-" + rule
	default:
		return "OAPI-" + rule
	}
}

func effectiveRequestBodySynthesisRule(bindingSpec string) string {
	if bindingSpec == BindingSpecOpenAPI30 {
		return "S-22"
	}
	return "S-21"
}

func unsupportedBindingSpecError(bindingSpec string) *invoke.InvocationError {
	data := map[string]any{
		"bindingSpec": bindingSpec,
	}
	if bindingSpec == "" {
		data["message"] = "name an exact OpenAPI family token in Source.BindingSpec"
	}
	return invoke.NewInvocationErrorWithData(ErrCodeUnsupportedBindingSpec, data)
}

// DefaultSourceName is the default source key used when registering an OpenAPI source in an OBI.
const DefaultSourceName = "openapi"

// newDefaultHTTPClient constructs an HTTP client with the invoker's default
// redirect policy and no overall timeout (the caller controls cancellation
// via context). Each Invoker gets its own client so multiple Invokers can
// be configured independently and tests can substitute clients without
// reaching into package-level globals.
func newDefaultHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Preserve Content-Encoding for the standalone client's OpenAPI response
	// governance; the standard transport's implicit gzip decoder would erase it
	// before the governing Header Object and configured decoder can inspect it.
	transport.DisableCompression = true
	return &http.Client{
		Transport: transport,
		// Redirects may rewrite an artifact-declared method or discard its
		// encoded body. The candidate defines no semantics-preserving follow
		// rule, so the conforming default observes the redirect response.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Runtime is a Core-invocation-shaped compatibility facade over the
// standalone OpenAPI client engine. New OpenAPI-only applications should use
// github.com/openbindings/openapi-client/go directly.
//
// Each Runtime owns an HTTP client and the standalone artifact engine.
// *http.Client and the engine's per-instance document cache are safe for
// concurrent use.
type Runtime struct {
	client             *http.Client
	nativeClientsMu    sync.RWMutex
	nativeClients      map[string]*openapiclient.Client
	securityHandlers   map[string]SecurityHandler
	parameterConvert   ParameterConversion
	requestCodings     map[string]ContentEncoder
	responseCodings    map[string]ContentDecoder
	requestCharacters  map[string]CharacterEncoder
	responseCharacters map[string]CharacterDecoder
	redirect           openapiclient.RedirectPolicy
}

// ContentEncoder and ContentDecoder are deterministic, whole-representation
// HTTP content-coding capabilities. The binding applies request encoders in
// field order and response decoders in reverse field order.
type ContentEncoder = openapiclient.ContentEncoder
type ContentDecoder = openapiclient.ContentDecoder
type CharacterEncoder = openapiclient.CharacterEncoder
type CharacterDecoder = openapiclient.CharacterDecoder
type ParameterConversion = openapiclient.ParameterConverter

// SecurityHandlerContext describes the authored OpenAPI security scheme an
// extension handler is applying.
type SecurityHandlerContext = openapiclient.SecurityHandlerContext

// SecurityHandler applies an artifact-authored security mechanism that the
// built-in OpenAPI credential adapter does not implement.
type SecurityHandler = openapiclient.SecurityHandler

// RuntimeOptions configures the OpenBindings bridge without changing Core
// invocation context or the synthesized OBI contract.
type RuntimeOptions struct {
	HTTPClient       *http.Client
	SecurityHandlers map[string]SecurityHandler
	// ParameterConversion is the OpenAPI bindings' deterministic non-string
	// scalar conversion point. Swagger 2.0 also consults it for null; strings
	// pass unchanged and nil means no conversion is configured.
	ParameterConversion        ParameterConversion
	RequestContentCodings      map[string]ContentEncoder
	ResponseContentCodings     map[string]ContentDecoder
	RequestCharacterEncodings  map[string]CharacterEncoder
	ResponseCharacterEncodings map[string]CharacterDecoder
	Redirect                   openapiclient.RedirectPolicy
}

// RuntimeSource identifies an OpenAPI artifact without requiring an OBI.
type RuntimeSource struct {
	// BindingSpec selects the exact OpenBindings OpenAPI binding candidate.
	// It must name an implemented family token exactly.
	BindingSpec string
	Location    string
	Content     json.RawMessage
}

// RuntimeInvocationArgs invoke a directly selected OpenAPI operation. Input
// values flow through the returned cardinality-agnostic Invocation handle.
type RuntimeInvocationArgs struct {
	Source               RuntimeSource
	Selector             string
	Context              map[string]any
	Hooks                *invoke.InvokeHooks
	Site                 *invoke.InvokeSite
	MaxDeliveryUnitBytes int64
}

// Invoker is the thin OpenBindings binding-invoker adapter over Runtime.
// Embedding keeps the existing direct binding API source-compatible while the
// artifact-centric Invoke and Prepare methods remain independently usable.
type Invoker struct {
	*Runtime
}

var (
	_ invoke.BindingInvoker  = (*Invoker)(nil)
	_ invoke.BindingPreparer = (*Invoker)(nil)
)

// NewRuntime creates the compatibility runtime with the binding's default
// HTTP client and redirect policy.
func NewRuntime() *Runtime {
	return NewRuntimeWithOptions(RuntimeOptions{})
}

// NewRuntimeWithClient creates a standalone OpenAPI runtime using client.
func NewRuntimeWithClient(client *http.Client) *Runtime {
	return NewRuntimeWithOptions(RuntimeOptions{HTTPClient: client})
}

// NewRuntimeWithOptions creates the compatibility runtime with explicit
// artifact-level extension handlers and transport configuration.
func NewRuntimeWithOptions(options RuntimeOptions) *Runtime {
	client := options.HTTPClient
	if client == nil {
		client = newDefaultHTTPClient()
	}
	return &Runtime{
		client:             client,
		nativeClients:      map[string]*openapiclient.Client{},
		securityHandlers:   cloneSecurityHandlers(options.SecurityHandlers),
		parameterConvert:   options.ParameterConversion,
		requestCodings:     options.RequestContentCodings,
		responseCodings:    options.ResponseContentCodings,
		requestCharacters:  options.RequestCharacterEncodings,
		responseCharacters: options.ResponseCharacterEncodings,
		redirect:           options.Redirect,
	}
}

func cloneSecurityHandlers(handlers map[string]SecurityHandler) map[string]SecurityHandler {
	if len(handlers) == 0 {
		return nil
	}
	result := make(map[string]SecurityHandler, len(handlers))
	for name, handler := range handlers {
		result[name] = handler
	}
	return result
}

// NewInvoker creates a new OpenAPI binding invoker with a default HTTP
// client. Use NewInvokerWithClient to inject a custom client (e.g., for
// tests, or to add a transport layer for tracing or auth).
func NewInvoker() *Invoker {
	return &Invoker{Runtime: NewRuntime()}
}

// NewInvokerWithClient creates an Invoker that uses the supplied
// *http.Client for all outbound requests. The caller is responsible for
// configuring redirect policy, transport, and any other client-level
// behavior. No overall request timeout should be set on the client because
// the caller controls cancellation via context.
func NewInvokerWithClient(client *http.Client) *Invoker {
	return &Invoker{Runtime: NewRuntimeWithClient(client)}
}

// NewInvokerWithOptions creates an OpenBindings adapter with explicit
// artifact-level security handlers and transport configuration.
func NewInvokerWithOptions(options RuntimeOptions) *Invoker {
	return &Invoker{Runtime: NewRuntimeWithOptions(options)}
}

// BindingSpecs returns the binding-spec identifiers this invoker supports.
func (e *Runtime) BindingSpecs() []openbindings.BindingSpecInfo {
	return openAPIBindingSpecInfos()
}

func (e *Runtime) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(bindingSpecs, openAPIBindingSpecInfos())
}

func openAPIBindingSpecInfos() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpecOpenAPI20, Description: "OpenAPI 2.0 (Swagger) HTTP APIs"},
		{BindingSpec: BindingSpecOpenAPI30, Description: "OpenAPI 3.0 HTTP APIs"},
		{BindingSpec: BindingSpecOpenAPI31, Description: "OpenAPI 3.1 HTTP APIs"},
		{BindingSpec: BindingSpecOpenAPI32, Description: "OpenAPI 3.2 HTTP APIs"},
	}
}

// Invoke runs a directly selected OpenAPI operation without requiring an OBI
// document or OpenBindings operation-selection machinery.
func (e *Runtime) Invoke(ctx context.Context, args *RuntimeInvocationArgs) invoke.Invocation[any, any] {
	return e.invokeBinding(ctx, runtimeBindingArgs(args))
}

// Prepare inspects a directly selected operation's runtime prerequisites
// without network I/O.
func (e *Runtime) Prepare(ctx context.Context, args *RuntimeInvocationArgs) (*invoke.ContextRequiredDetails, error) {
	return e.prepareBinding(ctx, runtimeBindingArgs(args))
}

func runtimeBindingArgs(args *RuntimeInvocationArgs) *invoke.BindingInvocationArgs {
	if args == nil {
		args = &RuntimeInvocationArgs{}
	}
	return &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{
			BindingSpec: args.Source.BindingSpec,
			Location:    args.Source.Location,
			Content:     args.Source.Content,
		},
		Selector:             args.Selector,
		Context:              args.Context,
		Hooks:                args.Hooks,
		Site:                 args.Site,
		MaxDeliveryUnitBytes: args.MaxDeliveryUnitBytes,
	}
}

func bridgeHooks(args *invoke.BindingInvocationArgs, bindingSpec string) *openapiclient.Hooks {
	if args.Hooks == nil {
		return nil
	}
	return &openapiclient.Hooks{
		Decode: func(site openapiclient.HookSite, raw openapiclient.RawResult) (any, bool, error) {
			coreSite := coreHookSite(args, bindingSpec, site.Target)
			coreRaw := invoke.RawResult{Status: raw.Status, Body: append([]byte(nil), raw.Body...), Meta: toCoreMetadata(raw.Meta)}
			contentType := ""
			for name, values := range raw.Meta {
				if strings.EqualFold(name, "Content-Type") && len(values) > 0 {
					contentType = values[0]
					break
				}
			}
			value, err := args.Hooks.DecodeOutput(coreSite, coreRaw, decodeByContentTypeFor(contentType, bindingSpec))
			return value, true, err
		},
		Classify: func(site openapiclient.HookSite, raw openapiclient.RawResult) (bool, bool, error) {
			coreSite := coreHookSite(args, bindingSpec, site.Target)
			coreRaw := invoke.RawResult{Status: raw.Status, Body: append([]byte(nil), raw.Body...), Meta: toCoreMetadata(raw.Meta)}
			value, err := args.Hooks.Classify(coreSite, coreRaw, BuiltinClassify)
			return value, true, err
		},
	}
}

func coreHookSite(args *invoke.BindingInvocationArgs, bindingSpec, target string) invoke.InvokeSite {
	var site invoke.InvokeSite
	if args.Site != nil {
		site = *args.Site
	} else {
		site.BindingSpec = bindingSpec
		site.Selector = args.Selector
	}
	if site.Target == "" {
		site.Target = target
	}
	return site
}

func toCoreMetadata(metadata openapiclient.Metadata) invoke.Metadata {
	result := make(invoke.Metadata, len(metadata))
	for name, values := range metadata {
		result[name] = append([]string(nil), values...)
	}
	return result
}

// InvokeBinding adapts the SDK binding invocation to the artifact runtime.
func (e *Invoker) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	return e.Runtime.invokeBinding(ctx, args)
}

// invokeBinding invokes an HTTP request based on an OpenAPI binding. The
// Invocation handle is returned synchronously; creation is inert and the
// HTTP work is scheduled on its own goroutine. Input messages flow through
// the handle's Write channel. All pre-dispatch failures (bad selector, missing
// server URL, unresolvable operation, missing context) terminate the handle
// BEFORE any network side effect.
func (e *Runtime) invokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	inv := invoke.NewInvocationImpl[any, any](ctx)
	go func() {
		if err := e.run(ctx, args, inv); err != nil {
			inv.FireError(invoke.AsInvocationError(err))
		}
	}()
	return inv
}

func (e *Runtime) run(ctx context.Context, args *invoke.BindingInvocationArgs, inv *invoke.InvocationImpl[any, any]) error {
	return e.runNative(ctx, args, inv)
}

// PrepareBinding adapts the SDK binding preflight to the artifact runtime.
func (e *Invoker) PrepareBinding(ctx context.Context, args *invoke.BindingInvocationArgs) (*invoke.ContextRequiredDetails, error) {
	return e.Runtime.prepareBinding(ctx, args)
}

// prepareBinding is the side-effect-free preflight (the prepareBinding
// operation of the openbindings.binding-invoker interface): it derives the
// operation's auth requirements from the document's securitySchemes and
// reports the context the invocation would require, or nil when it can
// proceed.
//
// It uses the source content or a previously cached document; it never
// fetches. When the document would have to be fetched to learn its security
// schemes, it reports no requirement and lets the invocation raise the
// challenge instead.
func (e *Runtime) prepareBinding(ctx context.Context, args *invoke.BindingInvocationArgs) (*invoke.ContextRequiredDetails, error) {
	return e.prepareNativeBinding(ctx, args)
}

// Synthesizer handles interface synthesis from OpenAPI documents.
type Synthesizer struct {
	client *http.Client
}

type openAPISourceExcludedError struct {
	message string
	rule    string
}

func (e *openAPISourceExcludedError) Error() string { return e.message }

var (
	_ synthesize.InterfaceSynthesizer = (*Synthesizer)(nil)
	_ synthesize.CoverageSynthesizer  = (*Synthesizer)(nil)
	_ synthesize.SourceInspector      = (*Synthesizer)(nil)
)

// NewSynthesizer creates a new OpenAPI interface synthesizer.
func NewSynthesizer() *Synthesizer {
	return NewSynthesizerWithClient(http.DefaultClient)
}

// NewSynthesizerWithClient creates an OpenAPI interface synthesizer whose
// artifact retrievals, including external references, use client. The client
// is an implementation seam only: resolver configuration is not represented
// in the synthesized OBI.
func NewSynthesizerWithClient(client *http.Client) *Synthesizer {
	if client == nil {
		client = http.DefaultClient
	}
	return &Synthesizer{client: client}
}

func (c *Synthesizer) resolverClient() *http.Client {
	if c != nil && c.client != nil {
		return c.client
	}
	return http.DefaultClient
}

// BindingSpecs returns the binding-spec identifiers this synthesizer supports.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return openAPIBindingSpecInfos()
}

func (c *Synthesizer) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(bindingSpecs, openAPIBindingSpecInfos())
}

// SynthesizeInterface converts an OpenAPI document to an OpenBindings interface.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	if swagger20SynthesisInput(in) {
		iface, _, _, err := c.synthesizeSwagger20(ctx, in, false)
		return iface, err
	}
	observed, err := c.synthesizeProviderProjection(ctx, in, false)
	if err != nil {
		return nil, err
	}
	return observed.iface, nil
}

// SynthesizeInterfaceWithCoverage converts an OpenAPI document and durably
// accounts for every path operation, request-media alternative, callback, and
// webhook observed by the same load.
//
// This surface is per-operation tolerant: an operation whose candidate
// operation boundary cannot be represented is omitted from the OBI and
// accounted for as an excluded target in coverage — a sound partial OBI with
// every omission evidenced, never a whole-document refusal
// (interface-synthesizer contract; core §10 posture). Strict synthesis
// (SynthesizeInterface) is unchanged.
func (c *Synthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	if swagger20SynthesisInput(in) {
		iface, _, entries, err := c.synthesizeSwagger20(ctx, in, true)
		if err != nil {
			return nil, err
		}
		return synthesize.NewSynthesisResult(iface, entries, true)
	}
	observed, err := c.synthesizeProviderProjection(ctx, in, true)
	if err != nil {
		var excluded *openAPISourceExcludedError
		if errors.As(err, &excluded) {
			iface, ifaceErr := excludedOpenAPISourceInterface(in)
			if ifaceErr != nil {
				return nil, ifaceErr
			}
			return synthesize.NewSynthesisResult(iface, []synthesize.SynthesisCoverageEntry{{
				SourceIndex: 0,
				SourceRef:   "#",
				Scope:       synthesize.SynthesisCoverageSource,
				Status:      synthesize.SynthesisExcluded,
				ReasonCode:  "openapi.source_excluded",
				Rule:        excluded.rule,
				Message:     excluded.message,
			}}, true)
		}
		return nil, err
	}
	return synthesize.NewSynthesisResult(observed.iface, observed.coverage, true)
}

func swagger20SynthesisInput(in *synthesize.SynthesizeInput) bool {
	return in != nil && len(in.Sources) == 1 && in.Sources[0].BindingSpec == BindingSpecOpenAPI20
}

func excludedOpenAPISourceInterface(in *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	if in == nil || len(in.Sources) != 1 {
		return nil, fmt.Errorf("excluded OpenAPI source result requires exactly one source")
	}
	skeleton, err := synthesize.SynthesisSkeleton(in)
	if err != nil {
		return nil, err
	}
	src := in.Sources[0]
	location := src.Location
	if src.OutputLocation != "" {
		location = src.OutputLocation
	}
	entry := openbindings.Source{
		BindingSpec: src.BindingSpec,
		Location:    location,
		Content:     append(json.RawMessage(nil), src.Content...),
		Description: src.Description,
	}
	skeleton.Sources = map[string]openbindings.Source{DefaultSourceName: entry}
	skeleton.Bindings = map[string]openbindings.BindingEntry{}
	skeleton.Dependencies = map[string]openbindings.DependencyEntry{}
	if err := synthesize.FinalizeSynthesis(&skeleton, in, DefaultSourceName, src.BindingSpec); err != nil {
		return nil, err
	}
	return &skeleton, nil
}

func readAuthoringArtifact(ctx context.Context, client *http.Client, location string) ([]byte, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "file":
		return os.ReadFile(u.Path)
	case "http", "https":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(resp.Body)
	default:
		return nil, fmt.Errorf("location scheme %q cannot be embedded", u.Scheme)
	}
}

// BuiltinHooks exposes the openapi builtins to the consultation seam's
// cross-format dispatch (invoke.BuiltinDecode/BuiltinClassify): the
// decoder follows the delivery unit's declared Content-Type header (read
// from raw.Meta — wire framing, never payload sniffing); the classifier
// is the 2xx convention floor.
func (e *Runtime) BuiltinHooks() (invoke.OutputDecoder, invoke.ResultClassifier) {
	decode := func(site invoke.InvokeSite, raw invoke.RawResult) (any, error) {
		ct := ""
		if vs := raw.Meta["Content-Type"]; len(vs) > 0 {
			ct = vs[0]
		}
		return decodeByContentTypeFor(ct, site.BindingSpec)(site, raw)
	}
	return decode, BuiltinClassify
}
