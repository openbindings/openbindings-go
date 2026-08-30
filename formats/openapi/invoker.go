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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/getkin/kin-openapi/openapi3"

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
	client           *http.Client
	engine           *openapiclient.Engine
	securityHandlers map[string]SecurityHandler
	parameterConvert ParameterConversion
	requestCodings   map[string]ContentEncoder
	responseCodings  map[string]ContentDecoder
}

// ContentEncoder and ContentDecoder are deterministic, whole-representation
// HTTP content-coding capabilities. The binding applies request encoders in
// field order and response decoders in reverse field order.
type ContentEncoder = openapiclient.ContentEncoder
type ContentDecoder = openapiclient.ContentDecoder

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
	ParameterConversion    ParameterConversion
	RequestContentCodings  map[string]ContentEncoder
	ResponseContentCodings map[string]ContentDecoder
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
		client:           client,
		engine:           openapiclient.NewEngine(client),
		securityHandlers: cloneSecurityHandlers(options.SecurityHandlers),
		parameterConvert: options.ParameterConversion,
		requestCodings:   options.RequestContentCodings,
		responseCodings:  options.ResponseContentCodings,
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

func enginePrepareOptions(args *invoke.BindingInvocationArgs, client *http.Client, securityHandlers map[string]SecurityHandler, parameterConverter ParameterConversion, requestCodings map[string]ContentEncoder, responseCodings map[string]ContentDecoder) (openapiclient.PrepareOptions, error) {
	if args == nil {
		return openapiclient.PrepareOptions{}, &invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError}
	}
	bindingSpec := args.Source.BindingSpec
	profile, ok := engineProfile(bindingSpec)
	if !ok {
		return openapiclient.PrepareOptions{}, unsupportedBindingSpecError(bindingSpec)
	}
	var content []byte
	var err error
	if args.Source.Content != nil {
		content, err = openbindings.ContentToBytes(args.Source.Content)
		if err != nil {
			return openapiclient.PrepareOptions{}, &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
		}
	}
	return openapiclient.PrepareOptions{
		Source: openapiclient.Source{Location: args.Source.Location, Content: content},
		Ref:    args.Selector, Profile: profile, Context: args.Context, HTTPClient: client,
		Hooks: bridgeHooks(args, bindingSpec), MaxDeliveryUnitBytes: args.MaxDeliveryUnitBytes,
		SecurityHandlers: securityHandlers, ParameterConverter: parameterConverter,
		RequestContentCodings: requestCodings, ResponseContentCodings: responseCodings,
		BufferEventStreams: bindingSpec != BindingSpecOpenAPI32, OmitAcceptHeader: true,
	}, nil
}

func engineProfile(bindingSpec string) (openapiclient.Profile, bool) {
	if isRequestImplementedOpenAPIBindingSpec(bindingSpec) {
		// OpenAPI 3.2 uses its native artifact lane for request and response
		// governance.
		return openapiclient.FullProfile(), true
	}
	return openapiclient.Profile{}, false
}

type runtimeOperationModel struct {
	artifact          *openapiclient.Artifact
	target            *openapiclient.OperationTarget
	document          *openapi3.T
	pathItem          *openapi3.PathItem
	operation         *openapi3.Operation
	parameters        openapi3.Parameters
	plans             []*bodyPlan
	routes            abstractInputRoutes
	preStartInputGate bool
}

func loadRuntimeOperationModel(ctx context.Context, args *invoke.BindingInvocationArgs, client *http.Client, bindingSpec string) (*runtimeOperationModel, error) {
	if args == nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError}
	}
	if args.Source.Content != nil {
		rawBytes, _ := openbindings.ContentToBytes(args.Source.Content)
		if rawSelectorHasUnaddressablePathVariable(rawBytes, args.Selector) {
			return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
		}
	}
	var artifact *openapiclient.Artifact
	var entryBytes []byte
	var document *openapi3.T
	var err error
	if bindingSpec == BindingSpecOpenAPI32 {
		var content []byte
		if args.Source.Content != nil {
			content, err = openbindings.ContentToBytes(args.Source.Content)
			if err != nil {
				return nil, &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
			}
		}
		artifact, err = openapiclient.LoadArtifact(ctx, openapiclient.Source{
			Location: args.Source.Location,
			Content:  content,
		}, openapiclient.ArtifactLoadOptions{HTTPClient: client, AllowExternalRefs: true})
		if artifact != nil {
			entryBytes = artifact.EntryBytes()
			document = artifact.Document
		}
	} else {
		// Keep the ratified 3.0/3.1 engines on their established loader. Their
		// private schema-presence and referring-security sidecars are adapter
		// concerns and changing that path here would be a legacy behavioral
		// delta, not part of the 3.2 edition fork.
		document, _, entryBytes, err = loadDocumentForSynthesis(ctx, client, args.Source.Location, args.Source.Content)
	}
	floor := computeAcceptanceFloorFromBytes(entryBytes)
	if floor != nil && floor.Refusal != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if floor != nil && floor.SourceExclusion != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if err != nil {
		rawBytes := entryBytes
		if len(rawBytes) == 0 && args.Source.Content != nil {
			rawBytes, _ = openbindings.ContentToBytes(args.Source.Content)
		}
		if rawSelectorHasUnaddressablePathVariable(rawBytes, args.Selector) {
			return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
		}
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
	}
	if err := checkAcceptedOpenAPIVersionForBindingSpec(document, bindingSpec); err != nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
	}
	var path, method, selector string
	var operationTarget *openapiclient.OperationTarget
	var pathItem *openapi3.PathItem
	var operation *openapi3.Operation
	if artifact != nil {
		if refusal := artifact.Refusal(); refusal != nil {
			return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
		}
		if exclusion := artifact.SourceExclusion(); exclusion != nil {
			return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
		}
		reference, parseErr := openapiclient.ParseOperationReference(args.Selector, artifact.Edition)
		if parseErr != nil {
			var resolution *openapiclient.OperationResolutionError
			if errors.As(parseErr, &resolution) && resolution.Kind == openapiclient.OperationTargetExcluded {
				return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
			}
			return nil, &invoke.InvocationError{Code: invoke.ErrCodeInvalidSelector}
		}
		target, resolutionErr := artifact.ResolveOperation(args.Selector)
		if resolutionErr != nil {
			var resolution *openapiclient.OperationResolutionError
			if errors.As(resolutionErr, &resolution) && resolution.Kind == openapiclient.OperationTargetExcluded {
				return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
			}
			return nil, &invoke.InvocationError{Code: invoke.ErrCodeSelectorNotFound}
		}
		target = adapterOpenAPI32SecurityTarget(target)
		operationTarget = target
		path, method, selector = reference.Path, reference.Method, reference.Ref
		document = target.Document
		pathItem, operation = target.PathItem, target.Operation
	} else {
		path, method, err = parseSelector(args.Selector)
		if err != nil {
			return nil, &invoke.InvocationError{Code: invoke.ErrCodeInvalidSelector}
		}
		selector = buildJSONPointerSelector(path, method)
		// The ladder's verdict is asked BEFORE the typed document is consulted,
		// and that order is load-bearing rather than cosmetic. An excluded
		// target is excluded by a property of the artifact's RAW image, which
		// the floor decided without the typed loader's help; whether the typed
		// loader could represent it is a different question with a different
		// answer. Round R made the difference observable: the per-target
		// restriction leaves an operation kin-openapi cannot decode out of the
		// document it hands back, so asking the document first would report a
		// target the artifact plainly declares as SELECTOR_NOT_FOUND instead of
		// the exclusion it is.
		if verdict := floor.opVerdict(selector); verdict != nil && verdict.Disposition == "invalid" {
			return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
		}
		if document.Paths == nil {
			return nil, &invoke.InvocationError{Code: invoke.ErrCodeSelectorNotFound}
		}
		pathItem = document.Paths.Find(path)
		if pathItem == nil {
			return nil, &invoke.InvocationError{Code: invoke.ErrCodeSelectorNotFound}
		}
		operation = pathItem.GetOperation(strings.ToUpper(method))
		if operation == nil {
			return nil, &invoke.InvocationError{Code: invoke.ErrCodeSelectorNotFound}
		}
	}
	verdict := floor.opVerdict(selector)
	if verdict != nil && verdict.Disposition == "invalid" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}

	parameters := effectiveParameters(pathItem, operation)
	if duplicate := duplicateEffectiveParameterIdentity(parameters); duplicate != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if malformedEffectiveParameterFor(parameters, bindingSpec) != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if err := checkPathTemplateDeclaration(path, parameters, bindingSpec); err != nil {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if bindingSpec == BindingSpecOpenAPI31 && equivalentPathTemplateCollision(document.Paths, path) != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if formStyleCookieMultiValueParamFor(parameters, bindingSpec == BindingSpecOpenAPI30) != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if styleLaneUndefinedExpansionParamFor(parameters, bindingSpec, bindingSpec == BindingSpecOpenAPI30) != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if unsupportedParameterContentFor(parameters, bindingSpec) != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	var plans []*bodyPlan
	if operation.RequestBody != nil && operation.RequestBody.Value != nil {
		plans, err = planRequestBodiesFor(document, operation, bindingSpec)
		if err != nil {
			if bindingSpec == BindingSpecOpenAPI32 && !operation.RequestBody.Value.Required {
				// A body-free 3.2 invocation bypasses request-media selection.
				// Keeping no body route preserves that usable operation while a
				// later invocation that tries to reach the unavailable optional
				// body still refuses before dispatch.
				plans = nil
			} else {
				return nil, invoke.NewInvocationError(invoke.ErrCodeSourceConfigError)
			}
		}
		plans = filterLadderInvalidAlternatives(plans, verdict, selector)
	}

	// The two sibling specifications intentionally differ here. The 3.0
	// family ignores declared requestBody on methods without defined body
	// semantics; both families refuse caller body on TRACE. Removing the typed
	// declaration before handing the operation to the standalone engine keeps
	// ignored declarations from becoming accidental required-input gates.
	bodyForbidden := requestBodyIgnoredForBindingSpec(bindingSpec, method)
	preStartInputGate := bodyForbidden || bindingSpec == BindingSpecOpenAPI32
	if bodyForbidden {
		operation.RequestBody = nil
		plans = nil
	}
	if len(plans) > 0 && forceJSONBodyEnvelopeCarriage(plans) {
		plans, err = planRequestBodiesFor(document, operation, bindingSpec)
		if err != nil {
			return nil, invoke.NewInvocationError(invoke.ErrCodeSourceConfigError)
		}
		plans = filterLadderInvalidAlternatives(plans, verdict, selector)
	}

	callerParameters := cloneEffectiveParameters(parameters)
	routes := planAbstractInputRoutes(callerParameters, plans)
	return &runtimeOperationModel{
		artifact: artifact, target: operationTarget, document: document, pathItem: pathItem, operation: operation,
		parameters: callerParameters, plans: plans, routes: routes,
		preStartInputGate: preStartInputGate,
	}, nil
}

func adapterOpenAPI32SecurityTarget(target *openapiclient.OperationTarget) *openapiclient.OperationTarget {
	if target == nil || target.Operation == nil || len(target.ReferringSecuritySchemes) == 0 {
		return target
	}
	copyTarget := *target
	copyOperation := *target.Operation
	copyOperation.Extensions = make(map[string]any, len(target.Operation.Extensions)+1)
	for name, value := range target.Operation.Extensions {
		copyOperation.Extensions[name] = value
	}
	copyOperation.Extensions[referringSecuritySchemesMarker] = target.ReferringSecuritySchemes
	copyTarget.Operation = &copyOperation
	return &copyTarget
}

func cloneOpenAPIOperation(operation *openapi3.Operation) *openapi3.Operation {
	if operation == nil {
		return nil
	}
	clone := *operation
	if operation.Responses == nil {
		return &clone
	}

	responses := openapi3.NewResponsesWithCapacity(operation.Responses.Len())
	responses.Extensions = operation.Responses.Extensions
	responses.Origin = operation.Responses.Origin
	for key, ref := range operation.Responses.Map() {
		if ref == nil {
			responses.Set(key, nil)
			continue
		}
		refClone := *ref
		if ref.Value != nil {
			responseClone := *ref.Value
			if ref.Value.Content != nil {
				responseClone.Content = make(openapi3.Content, len(ref.Value.Content))
				for mediaType, media := range ref.Value.Content {
					if media == nil {
						responseClone.Content[mediaType] = nil
						continue
					}
					mediaClone := *media
					responseClone.Content[mediaType] = &mediaClone
				}
			}
			refClone.Value = &responseClone
		}
		responses.Set(key, &refClone)
	}
	clone.Responses = responses
	return &clone
}

// forceJSONBodyEnvelopeCarriage makes the standalone wire engine consume the
// public envelope's body as one JSON value. This is an adapter-local document
// view: it never changes synthesis or validates the application value. The
// engine otherwise reconstructs object-declared JSON bodies from legacy flat
// fields, which would turn a supplied scalar or null into a refusal or `{}`.
func forceJSONBodyEnvelopeCarriage(plans []*bodyPlan) bool {
	changed := false
	for _, plan := range plans {
		if plan == nil || plan.family != familyJSON || plan.synthetic || plan.wholeObject || plan.media == nil {
			continue
		}
		schema := mediaSchema(plan.media)
		if schema == nil {
			continue
		}
		admitDynamic := true
		schema.AdditionalProperties = openapi3.AdditionalProperties{Has: &admitDynamic}
		changed = true
	}
	return changed
}

func rawSelectorHasUnaddressablePathVariable(data []byte, selector string) bool {
	path, method, err := parseSelector(selector)
	if err != nil || len(pathTemplateVariables(path)) == 0 {
		return false
	}
	root, ok := parseRawResource(data)
	if !ok {
		return false
	}
	document, ok := root.(map[string]any)
	if !ok {
		return false
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		return false
	}
	pathItem, ok := paths[path].(map[string]any)
	if !ok {
		return false
	}
	operation, ok := pathItem[strings.ToLower(method)].(map[string]any)
	if !ok {
		return false
	}
	declared := map[string]bool{}
	for _, owner := range []map[string]any{pathItem, operation} {
		parameters, _ := owner["parameters"].([]any)
		for _, rawParameter := range parameters {
			parameter, ok := rawParameter.(map[string]any)
			if !ok || parameter["$ref"] != nil {
				return false
			}
			name, _ := parameter["name"].(string)
			location, _ := parameter["in"].(string)
			if location == openapi3.ParameterInPath {
				declared[name] = true
			}
		}
	}
	for _, name := range pathTemplateVariables(path) {
		if !declared[name] {
			return true
		}
	}
	return false
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

func bridgeExecutionError(err error) error {
	var invocation *invoke.InvocationError
	if errors.As(err, &invocation) && invocation != nil {
		return invocation
	}
	var execution *openapiclient.ExecutionError
	if !errors.As(err, &execution) || execution == nil {
		if err == nil {
			return nil
		}
		return &invoke.InvocationError{
			Code: invoke.ErrCodeRuntime,
		}
	}
	details := execution.Details
	if prerequisites, ok := details.(*openapiclient.Prerequisites); ok {
		return invoke.NewInvocationErrorWithData(
			normalizedAdapterErrorCode(execution.Code),
			toCorePrerequisites(prerequisites),
		)
	}
	code := normalizedAdapterErrorCode(execution.Code)
	if execution.DetailsPresent {
		return invoke.NewInvocationErrorWithData(code, details)
	}
	return invoke.NewInvocationError(code)
}

func normalizedAdapterErrorCode(code string) string {
	switch code {
	case "SOURCE_LOAD_FAILED":
		return invoke.ErrCodeSourceLoadFailed
	// The adapter's vocabulary is the openapi-client's own: it still says
	// "ref" where the OpenBindings surface says "selector", so its ref-flavored
	// codes normalize to the selector-flavored SDK codes here.
	case "INVALID_OPERATION_REF", "ERR_INVALID_REF":
		return invoke.ErrCodeInvalidSelector
	case "OPERATION_NOT_FOUND", "ERR_REF_NOT_FOUND":
		return invoke.ErrCodeSelectorNotFound
	case "INVALID_DOCUMENT":
		return invoke.ErrCodeSourceConfigError
	case "RUNTIME_ERROR", "EXECUTION_COMPLETED_BEFORE_READY":
		return invoke.ErrCodeRuntime
	default:
		return code
	}
}

func toCorePrerequisites(value *openapiclient.Prerequisites) *invoke.ContextRequiredDetails {
	if value == nil {
		return nil
	}
	result := &invoke.ContextRequiredDetails{Target: value.Target, Alternatives: make([]invoke.ContextAlternative, len(value.Alternatives))}
	for alternativeIndex, alternative := range value.Alternatives {
		result.Alternatives[alternativeIndex].Requirements = make([]invoke.ContextRequirement, len(alternative.Requirements))
		for requirementIndex, requirement := range alternative.Requirements {
			result.Alternatives[alternativeIndex].Requirements[requirementIndex] = invoke.ContextRequirement{
				Type: requirement.Type, Name: requirement.Name, Durable: requirement.Durable,
				Description: requirement.Description, Extra: requirement.Extra,
			}
		}
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
	if args != nil && args.Source.BindingSpec == BindingSpecOpenAPI20 {
		return e.runSwagger20(ctx, args, inv)
	}
	options, err := enginePrepareOptions(args, e.client, e.securityHandlers, e.parameterConvert, e.requestCodings, e.responseCodings)
	if err != nil {
		return bridgeExecutionError(err)
	}
	bindingSpec := args.Source.BindingSpec
	model, err := loadRuntimeOperationModel(ctx, args, e.client, bindingSpec)
	if err != nil {
		var localInvocationError *invoke.InvocationError
		if errors.As(err, &localInvocationError) && localInvocationError.Code == openapiclient.CodeRefused {
			return localInvocationError
		}
		// Preserve the standalone engine's established refusal taxonomy for
		// malformed artifacts. The local model adds the edition/envelope gate;
		// it must not relabel a source the engine already rejects more
		// specifically.
		if _, prepareErr := e.engine.Prepare(ctx, options); prepareErr != nil {
			return bridgeExecutionError(prepareErr)
		}
		return bridgeExecutionError(err)
	}
	serverBase, serverErr := resolveServer(model.document, model.pathItem, model.operation, args.Context, args.Source.Location)
	if serverErr != nil {
		var required *configRequired
		if errors.As(serverErr, &required) {
			return invoke.NewContextRequiredError(configRequiredDetails(required, args.Source.Location))
		}
		return invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if scopeDetails, _, scopeErr := requiredImplicitConnectionScopeContext(model.document, model.operation, args.Context, serverBase, model.parameters, serverBase); scopeErr != nil {
		return invoke.NewInvocationError(openapiclient.CodeRefused)
	} else if scopeDetails != nil {
		return invoke.NewContextRequiredError(scopeDetails)
	}
	selectedSecurity, securityErr := electSecurityAlternative(model.document, model.operation, args.Context, serverBase, model.parameters)
	if securityErr != nil {
		return invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	securityDetails := requiredSelectedSecurityContext(selectedSecurity, args.Context, serverBase, e.securityHandlers)
	selectedServer := openapi3.Servers{&openapi3.Server{URL: serverBase}}
	model.operation.Servers = &selectedServer
	options.Context = contextWithoutConfigurationPoints(args.Context, "server", "security", "implicitConnectionScope")
	mediaDetails, mediaErr := requiredRequestMediaContext(model.document, model.operation, bindingSpec, args.Context)
	if mediaErr != nil {
		return invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if mediaDetails != nil {
		return invoke.NewContextRequiredError(mediaDetails)
	}
	propertyDetails, propertyErr := requiredPropertyMediaContext(model.document, model.operation, bindingSpec, args.Context)
	if propertyErr != nil {
		return invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if propertyDetails != nil {
		return invoke.NewContextRequiredError(propertyDetails)
	}
	if model.artifact != nil {
		preparedTarget := *model.target
		preparedTarget.Document = model.document
		preparedTarget.PathItem = model.pathItem
		preparedTarget.Operation = model.operation
		preparedArtifact, preparedErr := model.artifact.WithOperationTarget(&preparedTarget)
		if preparedErr != nil {
			return invoke.NewInvocationError(openapiclient.CodeRefused)
		}
		options.Source = openapiclient.Source{Location: args.Source.Location, Artifact: preparedArtifact}
	} else {
		options.Source = openapiclient.Source{Location: args.Source.Location, Document: model.document}
	}
	prepared, err := e.engine.Prepare(ctx, options)
	if err != nil {
		return bridgeExecutionError(err)
	}
	if securityDetails != nil {
		return invoke.NewContextRequiredError(securityDetails)
	}
	bridgeCtx, stop := invoke.DoneContext(ctx, inv.Done())
	defer stop()
	// Prove absence before starting the carrier: these methods can never route
	// a body, so dispatch must wait for either one body-free input or EOF.
	var preReadValue any
	var preReadErr error
	preRead := false
	if model.preStartInputGate {
		preReadValue, preReadErr = inv.ReadInput(bridgeCtx)
		preRead = true
		if preReadErr != nil && !errors.Is(preReadErr, io.EOF) {
			return preReadErr
		}
		if preReadErr == nil {
			envelope, envelopeErr := parseCallerEnvelope(preReadValue)
			selectedPlans := model.plans
			if envelopeErr == nil && envelope.bodyPresent {
				selectedPlans, envelopeErr = configuredRequestPlansFor(model.document, model.operation, model.plans, args.Context, bindingSpec)
			}
			if envelopeErr == nil {
				_, envelopeErr = engineInputForCallerEnvelope(preReadValue, model.parameters, selectedPlans, model.routes, options.Profile)
			}
			if envelopeErr != nil {
				return invoke.NewInvocationError(openapiclient.CodeRefused)
			}
		}
	}
	execution, err := prepared.Start(bridgeCtx)
	if err != nil {
		return bridgeExecutionError(err)
	}
	if execution.InputRequested() {
		value, readErr := preReadValue, preReadErr
		if !preRead {
			value, readErr = inv.ReadInput(bridgeCtx)
		}
		switch {
		case errors.Is(readErr, io.EOF):
			if err := execution.FinishInput(); err != nil {
				return bridgeExecutionError(err)
			}
		case readErr != nil:
			execution.Cancel()
			return readErr
		default:
			selectedPlans := model.plans
			envelope, envelopeErr := parseCallerEnvelope(value)
			if envelopeErr != nil {
				execution.Cancel()
				return invoke.NewInvocationError(openapiclient.CodeRefused)
			}
			if envelope.bodyPresent {
				selected, selectionErr := configuredRequestPlansFor(model.document, model.operation, model.plans, args.Context, bindingSpec)
				if selectionErr != nil {
					execution.Cancel()
					return invoke.NewInvocationError(openapiclient.CodeRefused)
				}
				selectedPlans = selected
			}
			engineInput, err := engineInputForCallerEnvelope(value, model.parameters, selectedPlans, model.routes, options.Profile)
			if err != nil {
				execution.Cancel()
				return invoke.NewInvocationError(openapiclient.CodeRefused)
			}
			if err := execution.Send(bridgeCtx, engineInput); err != nil {
				return bridgeExecutionError(err)
			}
			if err := execution.FinishInput(); err != nil {
				return bridgeExecutionError(err)
			}
		}
		_ = inv.CloseInput()
	} else {
		_ = inv.CloseInput()
	}

	_, _ = execution.Response(bridgeCtx)
	for event := range execution.Events() {
		if err := inv.EmitOutput(event.Value); err != nil {
			execution.Cancel()
			return nil
		}
	}
	if err := execution.Wait(); err != nil {
		if bridgeCtx.Err() != nil {
			return nil
		}
		return bridgeExecutionError(err)
	}
	inv.CloseOutput()
	return nil
}

func configRequiredDetails(required *configRequired, target string) *invoke.ContextRequiredDetails {
	requirement := invoke.NewConfigValueRequirement(
		required.point, required.path, required.description, required.schema, required.durable,
	)
	return &invoke.ContextRequiredDetails{
		Target:       target,
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{requirement}}},
	}
}

func contextWithoutConfigurationPoints(bindCtx map[string]any, points ...string) map[string]any {
	if bindCtx == nil {
		return nil
	}
	copyContext := make(map[string]any, len(bindCtx))
	for name, value := range bindCtx {
		copyContext[name] = value
	}
	configuration, _ := bindCtx["configuration"].(map[string]any)
	if configuration == nil {
		return copyContext
	}
	copyConfiguration := make(map[string]any, len(configuration))
	for name, value := range configuration {
		copyConfiguration[name] = value
	}
	for _, point := range points {
		delete(copyConfiguration, point)
	}
	copyContext["configuration"] = copyConfiguration
	return copyContext
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
	if args != nil && args.Source.BindingSpec == BindingSpecOpenAPI20 {
		return e.prepareSwagger20Binding(ctx, args)
	}
	options, err := enginePrepareOptions(args, e.client, e.securityHandlers, e.parameterConvert, e.requestCodings, e.responseCodings)
	if err != nil {
		var invocationErr *invoke.InvocationError
		if errors.As(err, &invocationErr) && invocationErr.Code == ErrCodeUnsupportedBindingSpec {
			return nil, invocationErr
		}
		return nil, nil
	}
	bindingSpec := args.Source.BindingSpec
	var prepared *openapiclient.PreparedOperation
	var localDetails *invoke.ContextRequiredDetails
	configurationPending := false
	if args.Source.Content != nil {
		var artifact *openapiclient.Artifact
		var document *openapi3.T
		if bindingSpec == BindingSpecOpenAPI32 {
			artifact = e.prepareArtifact(args.Source.Location, args.Source.Content)
			if artifact != nil {
				document = artifact.Document
			}
		} else {
			document = e.prepareDoc(args.Source.Location, args.Source.Content)
		}
		if document != nil {
			if err := checkAcceptedOpenAPIVersionForBindingSpec(document, bindingSpec); err != nil {
				return nil, &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
			}
			if artifact != nil {
				if artifact.Refusal() != nil || artifact.SourceExclusion() != nil {
					return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
				}
			}
			var method string
			var pathItem *openapi3.PathItem
			var operation *openapi3.Operation
			if artifact != nil {
				if reference, selectorErr := openapiclient.ParseOperationReference(args.Selector, artifact.Edition); selectorErr == nil {
					if target, resolutionErr := artifact.ResolveOperation(args.Selector); resolutionErr == nil {
						method = reference.Method
						document = target.Document
						pathItem, operation = target.PathItem, target.Operation
					}
				}
			} else if path, legacyMethod, selectorErr := parseSelector(args.Selector); selectorErr == nil && document.Paths != nil {
				if target := document.Paths.Find(path); target != nil {
					if targetOperation := target.GetOperation(strings.ToUpper(legacyMethod)); targetOperation != nil {
						method = legacyMethod
						pathItem, operation = target, targetOperation
					}
				}
			}
			if operation != nil {
				parameters := effectiveParameters(pathItem, operation)
				if duplicate := duplicateEffectiveParameterIdentity(parameters); duplicate != "" {
					return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
				}
				serverBase, serverErr := resolveServer(document, pathItem, operation, args.Context, args.Source.Location)
				if serverErr != nil {
					var required *configRequired
					if !errors.As(serverErr, &required) {
						return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
					}
					localDetails = mergeContextRequirements(localDetails, configRequiredDetails(required, args.Source.Location))
					configurationPending = true
				}
				selectionTarget := serverBase
				if selectionTarget == "" {
					selectionTarget = args.Source.Location
				}
				selectionDetails, selectionPending, selectionErr := requiredSecuritySelectionContext(document, operation, args.Context, selectionTarget)
				if selectionErr != nil {
					return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
				}
				localDetails = mergeContextRequirements(localDetails, selectionDetails)
				configurationPending = configurationPending || selectionPending
				if !selectionPending {
					scopeDetails, scopePending, scopeErr := requiredImplicitConnectionScopeContext(document, operation, args.Context, serverBase, parameters, selectionTarget)
					if scopeErr != nil {
						return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
					}
					localDetails = mergeContextRequirements(localDetails, scopeDetails)
					configurationPending = configurationPending || scopePending
					if !scopePending {
						selected, securityErr := electSecurityAlternative(document, operation, args.Context, serverBase, parameters)
						if securityErr != nil {
							return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
						}
						localDetails = mergeContextRequirements(localDetails, requiredSelectedSecurityContext(selected, args.Context, selectionTarget, e.securityHandlers))
					}
				}
				if requestBodyIgnoredForBindingSpec(bindingSpec, method) {
					operation.RequestBody = nil
				}
				mediaDetails, mediaErr := requiredRequestMediaContext(document, operation, bindingSpec, args.Context)
				if mediaErr != nil {
					return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
				}
				localDetails = mergeContextRequirements(localDetails, mediaDetails)
				propertyDetails, propertyErr := requiredPropertyMediaContext(document, operation, bindingSpec, args.Context)
				if propertyErr != nil {
					return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
				}
				localDetails = mergeContextRequirements(localDetails, propertyDetails)
				if !configurationPending {
					if _, configured := invoke.ContextConfiguration(args.Context)["server"]; configured {
						selectedServer := openapi3.Servers{&openapi3.Server{URL: serverBase}}
						operation.Servers = &selectedServer
					}
					options.Context = contextWithoutConfigurationPoints(args.Context, "server", "security", "implicitConnectionScope")
				}
			}
			if artifact != nil {
				options.Source = openapiclient.Source{Location: args.Source.Location, Artifact: artifact}
			} else {
				options.Source = openapiclient.Source{Location: args.Source.Location, Document: document}
			}
		}
		if configurationPending {
			return localDetails, nil
		}
		allowExternal := false
		options.AllowExternalRefs = &allowExternal
		prepared, err = e.engine.Prepare(ctx, options)
	} else {
		prepared, err = e.engine.PrepareCached(ctx, options)
	}
	if err != nil {
		var execution *openapiclient.ExecutionError
		if errors.As(err, &execution) {
			switch execution.Code {
			case openapiclient.CodeSourceConfigError, openapiclient.CodeRefused,
				openapiclient.CodeInvalidRef, openapiclient.CodeRefNotFound:
				return nil, bridgeExecutionError(err)
			}
		}
		return nil, nil
	}
	if prepared == nil {
		return nil, nil
	}
	return mergeContextRequirements(toCorePrerequisites(prepared.Prerequisites()), localDetails), nil
}

// prepareDoc returns the OpenAPI document without performing network I/O:
// inline source content is parsed locally (external refs disabled so the
// parse cannot fetch), and a location-only source is served from the warm
// document cache. Returns nil when the document is not knowable without I/O.
func (e *Runtime) prepareDoc(location string, content json.RawMessage) *openapi3.T {
	if content != nil {
		data, err := openbindings.ContentToBytes(content)
		if err != nil {
			return nil
		}
		var resource *url.URL
		if location != "" {
			resource, _ = url.Parse(location)
		}
		normalizer := newRawRefSiblingNormalizer(nil)
		data, err = normalizer.normalizeResource(data, resource)
		if err != nil {
			return nil
		}
		loader := openapi3.NewLoader() // external refs NOT allowed: no I/O
		var doc *openapi3.T
		if resource != nil {
			doc, err = loader.LoadFromDataWithPath(data, resource)
		} else {
			doc, err = loader.LoadFromData(data)
		}
		if err != nil {
			return nil
		}
		localizeReferenceMetadata(doc)
		return doc
	}
	if location == "" {
		return nil
	}
	return nil
}

func (e *Runtime) prepareArtifact(location string, content json.RawMessage) *openapiclient.Artifact {
	if content != nil {
		data, err := openbindings.ContentToBytes(content)
		if err != nil {
			return nil
		}
		artifact, err := openapiclient.LoadArtifact(context.Background(), openapiclient.Source{
			Location: location,
			Content:  data,
		}, openapiclient.ArtifactLoadOptions{HTTPClient: e.client, AllowExternalRefs: false})
		if err != nil {
			return nil
		}
		return artifact
	}
	if location == "" {
		return nil
	}
	return nil
}

// Synthesizer handles interface synthesis from OpenAPI documents.
type Synthesizer struct {
	client *http.Client
}

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
	iface, _, _, _, err := c.synthesizeObserved(ctx, in, nil)
	return iface, err
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
	unrealizable := map[string]unrealizableTarget{}
	iface, doc, artifact, floor, err := c.synthesizeObserved(ctx, in, func(target unrealizableTarget) {
		unrealizable[target.selector] = target
	})
	if err != nil {
		return nil, err
	}
	entries := openAPISynthesisCoverage(doc, artifact, iface, unrealizable, floor)
	return synthesize.NewSynthesisResult(iface, entries, true)
}

func swagger20SynthesisInput(in *synthesize.SynthesizeInput) bool {
	return in != nil && len(in.Sources) == 1 && in.Sources[0].BindingSpec == BindingSpecOpenAPI20
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *synthesize.SynthesizeInput, onUnrealizable func(unrealizableTarget)) (*openbindings.Interface, *openapi3.T, *openapiclient.Artifact, *acceptanceFloor, error) {
	if len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		return &skeleton, nil, nil, nil, err
	}
	if len(in.Sources) > 1 {
		return nil, nil, nil, nil, synthesize.ErrMultipleSources
	}
	src := in.Sources[0]
	if !isRequestImplementedOpenAPIBindingSpec(src.BindingSpec) {
		return nil, nil, nil, nil, fmt.Errorf("%s: binding specification %q is not implemented", ErrCodeUnsupportedBindingSpec, src.BindingSpec)
	}
	if src.OutputLocation != "" {
		if err := validateDocumentAddress(src.OutputLocation); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	// Authoring convenience: a bare filesystem path loads and is emitted as
	// its absolute file:// spelling. Emitting the original relative spelling
	// would create a source this binding implementation is guaranteed to
	// refuse under OAPI-D-02.
	loadLocation, err := absolutizeArtifactLocation(src.Location)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	artifactContent := src.Content
	if src.Embed && artifactContent == nil {
		data, embedErr := readAuthoringArtifact(ctx, c.resolverClient(), loadLocation)
		if embedErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("embed OpenAPI source: %w", embedErr)
		}
		artifactContent = openbindings.TextContent(string(data))
	}
	var artifact *openapiclient.Artifact
	var doc *openapi3.T
	var schemaOverlays *rawSchemaOverlayCollector
	var entryBytes []byte
	if src.BindingSpec == BindingSpecOpenAPI32 {
		var content []byte
		if artifactContent != nil {
			content, err = openbindings.ContentToBytes(artifactContent)
		}
		if err == nil {
			artifact, err = openapiclient.LoadArtifact(ctx, openapiclient.Source{Location: loadLocation, Content: content}, openapiclient.ArtifactLoadOptions{HTTPClient: c.resolverClient(), AllowExternalRefs: true})
		}
		if artifact != nil {
			doc = artifact.Document
			entryBytes = artifact.EntryBytes()
		}
	} else {
		doc, schemaOverlays, entryBytes, err = loadDocumentForSynthesis(ctx, c.resolverClient(), loadLocation, artifactContent)
	}
	// The invalid-artifact acceptance floor (the registered OpenAPI binding family §3),
	// computed over the entry document's raw image. Part 2's single derived
	// whole-source refusal fires here, on every synthesis surface -- and it
	// fires WHETHER OR NOT the load succeeded, because it is decided over the
	// artifact's raw image and not over anything the loader produced.
	floor := computeAcceptanceFloorFromBytes(entryBytes)
	if floor != nil && floor.Refusal != "" {
		return nil, nil, nil, nil, errors.New(floor.Refusal)
	}
	if floor != nil && floor.SourceExclusion != "" {
		return nil, nil, nil, nil, errors.New(floor.SourceExclusion)
	}
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	if err := checkAcceptedOpenAPIVersionForBindingSpec(doc, src.BindingSpec); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	// kin-openapi resolves external SchemaRefs into Value but intentionally
	// retains their artifact-relative Ref spelling. That spelling would dangle
	// after a schema is projected into an operation-local OBI contract.
	// Internalize the already-resolved closure before projection so the
	// existing self-contained-schema pass can materialize every reachable
	// schema without exposing artifact reference semantics in the OBI.
	if schemaOverlays != nil {
		schemaOverlays.setExternalComponents(internalizeExternalRefs(ctx, doc))
	} else {
		internalizeExternalRefs(ctx, doc)
	}
	warn := func(w synthesize.SynthesizerWarning) {
		if in.OnWarning != nil {
			in.OnWarning(w)
		}
	}
	iface, err := convertArtifactToInterfaceWithOverlay(doc, artifact, loadLocation, src.BindingSpec, warn, onUnrealizable, schemaOverlays, floor)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// Content is authoritative and remains byte-for-byte in the synthesized
	// source. A co-present location is its base/provenance, not permission to
	// replace the embedded artifact with a later fetch.
	if artifactContent != nil {
		if entry, ok := iface.Sources[DefaultSourceName]; ok {
			entry.Content = append(json.RawMessage(nil), artifactContent...)
			iface.Sources[DefaultSourceName] = entry
		}
	}
	if err := synthesize.FinalizeSynthesis(&iface, in, DefaultSourceName, src.BindingSpec); err != nil {
		return nil, nil, nil, nil, err
	}
	return &iface, doc, artifact, floor, nil
}

// internalizeExternalRefs internalizes the already-resolved external closure
// and returns what each generated component key was internalized FROM. These
// keys never escape the binding processor: a cut point that would otherwise be
// named by one is named from the recorded identity instead (cutpoint_names.go).
func internalizeExternalRefs(ctx context.Context, doc *openapi3.T) map[string]refIdentity {
	externals := map[string]refIdentity{}
	doc.InternalizeRefs(ctx, func(_ *openapi3.T, ref openapi3.ComponentRef) string {
		identity := referenceIdentity(ref)
		name := internalizedRefName(ref.CollectionName(), identity)
		externals[name] = identity
		return name
	})
	return externals
}

// referenceIdentity reads a resolved reference's declaring document and
// pointer. The fragment is normalized because kin-openapi records it in two
// spellings (refIdentity's own documentation): without normalization one
// component acquires two identities and internalizes twice, publishing the same
// artifact component as two cut points.
func referenceIdentity(ref openapi3.ComponentRef) refIdentity {
	path := ref.RefPath()
	if path == nil {
		return refIdentity{pointer: normalizeReferenceFragment(ref.RefString())}
	}
	document := *path
	document.Fragment = ""
	return refIdentity{
		document: document.String(),
		pointer:  normalizeReferenceFragment(path.Fragment),
	}
}

// internalizedRefName is stable and collision-resistant across the complete
// artifact closure. The upstream default is human-readable but explicitly not
// injective for every URI; fidelity matters more than generated component
// aesthetics because these names never escape the binding processor.
func internalizedRefName(collection string, identity refIdentity) string {
	sum := sha256.Sum256([]byte(collection + "\x00" + identity.canonical()))
	return fmt.Sprintf("ob_%x", sum)
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
