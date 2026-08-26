// Package openapi implements the OpenAPI binding format for OpenBindings.
//
// The package handles:
//   - Converting OpenAPI 3.x documents to OpenBindings interfaces
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

// Registered OpenAPI binding-specification identifiers. The 2.0 and 3.2
// families are named here so dispatch can refuse them honestly; this package
// implements only the 3.0 and 3.1 engines in the current release.
const (
	BindingSpecOpenAPI20 = "openbindings.openapi-2.0@1"
	BindingSpecOpenAPI30 = "openbindings.openapi-3.0@1"
	BindingSpecOpenAPI31 = "openbindings.openapi-3.1@1"
	BindingSpecOpenAPI32 = "openbindings.openapi-3.2@1"

	defaultBindingSpec = BindingSpecOpenAPI31
	// ErrCodeUnsupportedBindingSpec reports a registered family whose engine
	// is absent, or a binding invocation that names no exact family token.
	ErrCodeUnsupportedBindingSpec = "ERR_UNSUPPORTED_BINDING_SPEC"
)

type openAPIBindingSpecRegistration struct {
	editions    map[string]bool
	implemented bool
}

var openAPIBindingSpecRegistry = map[string]openAPIBindingSpecRegistration{
	BindingSpecOpenAPI20: {implemented: false},
	BindingSpecOpenAPI30: {
		implemented: true,
		editions: map[string]bool{
			"3.0.0": true, "3.0.1": true, "3.0.2": true,
			"3.0.3": true, "3.0.4": true,
		},
	},
	BindingSpecOpenAPI31: {
		implemented: true,
		editions:    map[string]bool{"3.1.0": true, "3.1.1": true, "3.1.2": true},
	},
	BindingSpecOpenAPI32: {implemented: false},
}

// Numbered "revision" labels retained in some internal helper/test filenames
// are development-history markers, not published binding-spec revisions.

func hasRoutedInputs(bindingSpec string) bool {
	return isImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasMediaFidelity(bindingSpec string) bool {
	return isImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasResponseFidelity(bindingSpec string) bool {
	return isImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasDynamicObjectCarriage(bindingSpec string) bool {
	return isImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasWholeJSONCarriage(bindingSpec string) bool {
	return isImplementedOpenAPIBindingSpec(bindingSpec)
}

func hasSchemaOmittedOAS30ByteCarriage(bindingSpec string) bool {
	return isImplementedOpenAPIBindingSpec(bindingSpec)
}

func isImplementedOpenAPIBindingSpec(bindingSpec string) bool {
	registration, ok := openAPIBindingSpecRegistry[bindingSpec]
	return ok && registration.implemented
}

func openAPIRule(bindingSpec, rule string) string {
	switch bindingSpec {
	case BindingSpecOpenAPI30:
		return "OAPI30-" + rule
	case BindingSpecOpenAPI31:
		return "OAPI31-" + rule
	default:
		return "OAPI-" + rule
	}
}

func unsupportedBindingSpecError(bindingSpec string) *invoke.InvocationError {
	return invoke.NewInvocationErrorWithData(ErrCodeUnsupportedBindingSpec, map[string]any{
		"bindingSpec": bindingSpec,
	})
}

// DefaultSourceName is the default source key used when registering an OpenAPI source in an OBI.
const DefaultSourceName = "openapi"

// newDefaultHTTPClient constructs an HTTP client with the invoker's default
// redirect policy and no overall timeout (the caller controls cancellation
// via context). Each Invoker gets its own client so multiple Invokers can
// be configured independently and tests can substitute clients without
// reaching into package-level globals.
func newDefaultHTTPClient() *http.Client {
	return &http.Client{
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
}

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
}

// RuntimeSource identifies an OpenAPI artifact without requiring an OBI.
type RuntimeSource struct {
	// BindingSpec selects the exact OpenBindings OpenAPI binding candidate.
	// Empty selects the OpenAPI 3.1 family token.
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
		{BindingSpec: BindingSpecOpenAPI30, Description: "OpenAPI 3.0 HTTP APIs"},
		{BindingSpec: BindingSpecOpenAPI31, Description: "OpenAPI 3.1 HTTP APIs"},
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
	bindingSpec := args.Source.BindingSpec
	if bindingSpec == "" {
		bindingSpec = defaultBindingSpec
	}
	return &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{
			BindingSpec: bindingSpec,
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

func enginePrepareOptions(args *invoke.BindingInvocationArgs, client *http.Client, securityHandlers map[string]SecurityHandler) (openapiclient.PrepareOptions, error) {
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
		SecurityHandlers: securityHandlers,
	}, nil
}

func engineProfile(bindingSpec string) (openapiclient.Profile, bool) {
	if isImplementedOpenAPIBindingSpec(bindingSpec) {
		return openapiclient.FullProfile(), true
	}
	return openapiclient.Profile{}, false
}

type runtimeOperationModel struct {
	document   *openapi3.T
	operation  *openapi3.Operation
	parameters openapi3.Parameters
	plans      []*bodyPlan
	routes     abstractInputRoutes
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
	document, _, entryBytes, err := loadDocumentForSynthesis(ctx, client, args.Source.Location, args.Source.Content)
	floor := computeAcceptanceFloorFromBytes(entryBytes)
	if floor != nil && floor.Refusal != "" {
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
	path, method, err := parseSelector(args.Selector)
	if err != nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeInvalidSelector}
	}
	if document.Paths == nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeSelectorNotFound}
	}
	pathItem := document.Paths.Find(path)
	if pathItem == nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeSelectorNotFound}
	}
	operation := pathItem.GetOperation(strings.ToUpper(method))
	if operation == nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeSelectorNotFound}
	}
	selector := buildJSONPointerSelector(path, method)
	verdict := floor.opVerdict(selector)
	if verdict != nil && verdict.Disposition == "invalid" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}

	parameters := effectiveParameters(pathItem, operation)
	if duplicate := duplicateEffectiveParameterIdentity(parameters); duplicate != "" {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	if err := checkPathTemplateAddressability(path, parameters); err != nil {
		return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
	}
	var plans []*bodyPlan
	if operation.RequestBody != nil && operation.RequestBody.Value != nil {
		plans, err = planRequestBodiesFor(document, operation, bindingSpec)
		if err != nil {
			return nil, invoke.NewInvocationError(invoke.ErrCodeSourceConfigError)
		}
		plans = filterLadderInvalidAlternatives(plans, verdict, selector)
	}

	// The two sibling specifications intentionally differ here. The 3.0
	// family ignores declared requestBody on methods without defined body
	// semantics; both families refuse caller body on TRACE. Removing the typed
	// declaration before handing the operation to the standalone engine keeps
	// ignored declarations from becoming accidental required-input gates.
	if requestBodyIgnoredForBindingSpec(bindingSpec, method) {
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

	routes := planAbstractInputRoutes(parameters, plans)
	return &runtimeOperationModel{
		document: document, operation: operation, parameters: parameters, plans: plans, routes: routes,
	}, nil
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
			if values := raw.Meta["Content-Type"]; len(values) > 0 {
				contentType = values[0]
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
	options, err := enginePrepareOptions(args, e.client, e.securityHandlers)
	if err != nil {
		return bridgeExecutionError(err)
	}
	bindingSpec := args.Source.BindingSpec
	if bindingSpec == "" {
		bindingSpec = defaultBindingSpec
	}
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
	options.Source = openapiclient.Source{Location: args.Source.Location, Document: model.document}
	prepared, err := e.engine.Prepare(ctx, options)
	if err != nil {
		return bridgeExecutionError(err)
	}
	bridgeCtx, stop := invoke.DoneContext(ctx, inv.Done())
	defer stop()

	execution, err := prepared.Start(bridgeCtx)
	if err != nil {
		return bridgeExecutionError(err)
	}
	if execution.InputRequested() {
		value, readErr := inv.ReadInput(bridgeCtx)
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
			if len(model.plans) > 1 {
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
	options, err := enginePrepareOptions(args, e.client, e.securityHandlers)
	if err != nil {
		var invocationErr *invoke.InvocationError
		if errors.As(err, &invocationErr) && invocationErr.Code == ErrCodeUnsupportedBindingSpec {
			return nil, invocationErr
		}
		return nil, nil
	}
	bindingSpec := args.Source.BindingSpec
	if bindingSpec == "" {
		bindingSpec = defaultBindingSpec
	}
	var prepared *openapiclient.PreparedOperation
	if args.Source.Content != nil {
		if document := e.prepareDoc(args.Source.Location, args.Source.Content); document != nil {
			if err := checkAcceptedOpenAPIVersionForBindingSpec(document, bindingSpec); err != nil {
				return nil, &invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed}
			}
			if path, method, selectorErr := parseSelector(args.Selector); selectorErr == nil && document.Paths != nil {
				if pathItem := document.Paths.Find(path); pathItem != nil {
					if operation := pathItem.GetOperation(strings.ToUpper(method)); operation != nil {
						if duplicate := duplicateEffectiveParameterIdentity(effectiveParameters(pathItem, operation)); duplicate != "" {
							return nil, invoke.NewInvocationError(openapiclient.CodeRefused)
						}
						if requestBodyIgnoredForBindingSpec(bindingSpec, method) {
							operation.RequestBody = nil
						}
					}
				}
			}
			options.Source = openapiclient.Source{Location: args.Source.Location, Document: document}
		}
		allowExternal := false
		options.AllowExternalRefs = &allowExternal
		prepared, err = e.engine.Prepare(ctx, options)
	} else {
		prepared, err = e.engine.PrepareCached(ctx, options)
	}
	if err != nil {
		var execution *openapiclient.ExecutionError
		if errors.As(err, &execution) && execution.Code == openapiclient.CodeSourceConfigError {
			return nil, bridgeExecutionError(err)
		}
		return nil, nil
	}
	if prepared == nil {
		return nil, nil
	}
	return toCorePrerequisites(prepared.Prerequisites()), nil
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
	iface, _, _, err := c.synthesizeObserved(ctx, in, nil)
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
	unrealizable := map[string]unrealizableTarget{}
	iface, doc, floor, err := c.synthesizeObserved(ctx, in, func(target unrealizableTarget) {
		unrealizable[target.selector] = target
	})
	if err != nil {
		return nil, err
	}
	entries := openAPISynthesisCoverage(doc, iface, unrealizable, floor)
	return synthesize.NewSynthesisResult(iface, entries, true)
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *synthesize.SynthesizeInput, onUnrealizable func(unrealizableTarget)) (*openbindings.Interface, *openapi3.T, *acceptanceFloor, error) {
	if len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		return &skeleton, nil, nil, err
	}
	if len(in.Sources) > 1 {
		return nil, nil, nil, synthesize.ErrMultipleSources
	}
	src := in.Sources[0]
	if !isImplementedOpenAPIBindingSpec(src.BindingSpec) {
		return nil, nil, nil, fmt.Errorf("%s: binding specification %q is not implemented", ErrCodeUnsupportedBindingSpec, src.BindingSpec)
	}
	if src.OutputLocation != "" {
		if err := validateDocumentAddress(src.OutputLocation); err != nil {
			return nil, nil, nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	// Authoring convenience: a bare filesystem path loads and is emitted as
	// its absolute file:// spelling. Emitting the original relative spelling
	// would create a source this binding implementation is guaranteed to
	// refuse under OAPI-D-02.
	loadLocation, err := absolutizeArtifactLocation(src.Location)
	if err != nil {
		return nil, nil, nil, err
	}
	artifactContent := src.Content
	if src.Embed && artifactContent == nil {
		data, embedErr := readAuthoringArtifact(ctx, c.resolverClient(), loadLocation)
		if embedErr != nil {
			return nil, nil, nil, fmt.Errorf("embed OpenAPI source: %w", embedErr)
		}
		artifactContent = openbindings.TextContent(string(data))
	}
	doc, schemaOverlays, entryBytes, err := loadDocumentForSynthesis(ctx, c.resolverClient(), loadLocation, artifactContent)
	// The invalid-artifact acceptance floor (the registered OpenAPI binding family §3),
	// computed over the entry document's raw image. Part 2's single derived
	// whole-source refusal fires here, on every synthesis surface -- and it
	// fires WHETHER OR NOT the load succeeded, because it is decided over the
	// artifact's raw image and not over anything the loader produced.
	floor := computeAcceptanceFloorFromBytes(entryBytes)
	if floor != nil && floor.Refusal != "" {
		return nil, nil, nil, errors.New(floor.Refusal)
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	if err := checkAcceptedOpenAPIVersionForBindingSpec(doc, src.BindingSpec); err != nil {
		return nil, nil, nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	// kin-openapi resolves external SchemaRefs into Value but intentionally
	// retains their artifact-relative Ref spelling. That spelling would dangle
	// after a schema is projected into an operation-local OBI contract.
	// Internalize the already-resolved closure before projection so the
	// existing self-contained-schema pass can materialize every reachable
	// schema without exposing artifact reference semantics in the OBI.
	schemaOverlays.setExternalComponents(internalizeExternalRefs(ctx, doc))
	warn := func(w synthesize.SynthesizerWarning) {
		if in.OnWarning != nil {
			in.OnWarning(w)
		}
	}
	iface, err := convertDocToInterfaceWithOverlay(doc, loadLocation, src.BindingSpec, warn, onUnrealizable, schemaOverlays, floor)
	if err != nil {
		return nil, nil, nil, err
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
		return nil, nil, nil, err
	}
	return &iface, doc, floor, nil
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
