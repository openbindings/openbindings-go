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

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/getkin/kin-openapi/openapi3"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
)

// BindingSpec identifies the unreleased first OpenAPI binding candidate.
const BindingSpec = "openbindings.openapi@1"

// Numbered "revision" labels retained in some internal helper/test filenames
// are development-history markers, not published binding-spec revisions.

func hasRoutedInputs(bindingSpec string) bool {
	return bindingSpec == BindingSpec
}

func hasMediaFidelity(bindingSpec string) bool {
	return bindingSpec == BindingSpec
}

func hasResponseFidelity(bindingSpec string) bool {
	return bindingSpec == BindingSpec
}

func hasDynamicObjectCarriage(bindingSpec string) bool {
	return bindingSpec == BindingSpec
}

func hasWholeJSONCarriage(bindingSpec string) bool {
	return bindingSpec == BindingSpec
}

func hasSchemaOmittedOAS30ByteCarriage(bindingSpec string) bool {
	return bindingSpec == BindingSpec
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
	// Empty selects the current unreleased candidate.
	BindingSpec string
	Location    string
	Content     json.RawMessage
}

// RuntimeInvocationArgs invoke a directly selected OpenAPI operation. Input
// values flow through the returned cardinality-agnostic Invocation handle.
type RuntimeInvocationArgs struct {
	Source               RuntimeSource
	Ref                  string
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
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpec, Description: "OpenAPI 3.x HTTP APIs"},
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
		bindingSpec = BindingSpec
	}
	return &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{
			BindingSpec: bindingSpec,
			Location:    args.Source.Location,
			Content:     args.Source.Content,
		},
		Ref:                  args.Ref,
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
	if bindingSpec == "" {
		bindingSpec = BindingSpec
	}
	profile, ok := engineProfile(bindingSpec)
	if !ok {
		return openapiclient.PrepareOptions{}, &invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError}
	}
	profile = openapiclient.WithInputRouteEnvelope(profile, "$openbindings", bindingSpec)
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
		Ref:    args.Ref, Profile: profile, Context: args.Context, HTTPClient: client,
		Hooks: bridgeHooks(args, bindingSpec), MaxDeliveryUnitBytes: args.MaxDeliveryUnitBytes,
		SecurityHandlers: securityHandlers,
	}, nil
}

func engineProfile(bindingSpec string) (openapiclient.Profile, bool) {
	if bindingSpec == BindingSpec {
		return openapiclient.FullProfile(), true
	}
	return openapiclient.Profile{}, false
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
		site.Ref = args.Ref
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
	case "INVALID_OPERATION_REF":
		return invoke.ErrCodeInvalidRef
	case "OPERATION_NOT_FOUND":
		return invoke.ErrCodeRefNotFound
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
// the handle's Write channel. All pre-dispatch failures (bad ref, missing
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
			if err := execution.Send(bridgeCtx, value); err != nil {
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
		return nil, nil
	}
	var prepared *openapiclient.PreparedOperation
	if args.Source.Content != nil {
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
	return []openbindings.BindingSpecInfo{
		{BindingSpec: BindingSpec, Description: "OpenAPI 3.x HTTP APIs"},
	}
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
// flattened boundary cannot be represented is omitted from the OBI and
// accounted for as an excluded target in coverage — a sound partial OBI with
// every omission evidenced, never a whole-document refusal
// (interface-synthesizer contract; core §10 posture). Strict synthesis
// (SynthesizeInterface) is unchanged.
func (c *Synthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	unrealizable := map[string]unrealizableTarget{}
	iface, doc, floor, err := c.synthesizeObserved(ctx, in, func(target unrealizableTarget) {
		unrealizable[target.ref] = target
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
	if src.BindingSpec != BindingSpec {
		return nil, nil, nil, fmt.Errorf("synthesizer supports exact binding specification %q, got %q", BindingSpec, src.BindingSpec)
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
	// The invalid-artifact acceptance floor (openbindings.openapi@1 §3),
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
