package openbindings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ---------------------------------------------------------------------------
// Context store
// ---------------------------------------------------------------------------

// ContextStore is an optional SDK storage seam for binding invocation context.
// Keys are caller-chosen strings; StoreContextResolver uses its own normalized
// target convention, while other consumers may choose a different policy.
// Values are opaque context records — credentials, headers, cookies,
// environment, metadata — using well-known field names for cross-invoker
// interoperability.
//
// The well-known credential fields, by the requirement family they satisfy:
//
//	auth.bearer          →  "bearerToken"
//	auth.apiKey          →  "apiKey"
//	auth.apiKey (named)  →  "apiKeys"[name] (falls back to "apiKey")
//	auth.basic           →  "basic" (a {"username","password"} object)
//	auth.oauth2          →  "accessToken" (plus "refreshToken", "clientSecret")
//
// so satisfying a bearer challenge for an origin is one call:
//
//	store.Set(ctx, openbindings.NormalizeContextKey(target),
//	    map[string]any{"bearerToken": token})
//
// StoreContextResolver inspects standard requirement fields only to satisfy
// and scope a challenge. Other consumers may treat values as wholly opaque.
// Delete removes an entry; Set is for a non-nil object. The published
// openbindings.document-store interface is the language-neutral wire analogue
// when this optional seam crosses a process boundary.
type ContextStore interface {
	Get(ctx context.Context, key string) (map[string]any, error)
	Set(ctx context.Context, key string, value map[string]any) error
	Delete(ctx context.Context, key string) error
}

// ---------------------------------------------------------------------------
// Platform callbacks
// ---------------------------------------------------------------------------

// BrowserRedirectResult holds the result of a browser redirect callback.
type BrowserRedirectResult struct {
	CallbackURL string
	RedirectURI string // the redirect_uri the platform used in the authorization request
}

// PromptOptions configures a Prompt callback invocation.
type PromptOptions struct {
	Label  string
	Secret bool
}

// FileSelectOptions configures a FileSelect callback invocation.
type FileSelectOptions struct {
	Label      string
	Extensions []string
}

// PlatformCallbacks are functions an application wires into its own
// ContextResolver so interactive context resolution (prompts, browser
// redirects, file pickers) can run without the resolver knowing what
// platform it's on. Each field is nil when the capability is unavailable.
// Bindings never see these: a binding depends on context data, never on the
// mechanism that produced it.
type PlatformCallbacks struct {
	BrowserRedirect func(ctx context.Context, url string) (*BrowserRedirectResult, error)
	Prompt          func(ctx context.Context, message string, opts *PromptOptions) (string, error)
	Confirmation    func(ctx context.Context, message string) (bool, error)
	FileSelect      func(ctx context.Context, message string, opts *FileSelectOptions) (string, error)
}

// ---------------------------------------------------------------------------
// Invocation argument types
// ---------------------------------------------------------------------------

// InvocationSource identifies the binding source for invocation. Content is
// raw JSON with presence semantics, exactly as on Source: nil = absent, a
// `null` literal = present null; the format interprets a present value per
// its own pins.
type InvocationSource struct {
	BindingSpec string          `json:"bindingSpec"`
	Location    string          `json:"location,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"`
}

// BindingInvocationArgs are the arguments for invoking a resolved binding
// against a format-specific source. Input messages are NOT part of the args:
// they flow through the returned Invocation handle's Write channel.
//
// Runtime prerequisites (credentials, configuration) travel in Context as
// opaque well-known fields; a binding that needs context it wasn't given
// terminates with CONTEXT_REQUIRED before any side effect, and resolution
// happens above the binding (see OperationInvoker.ContextResolver).
type BindingInvocationArgs struct {
	Source InvocationSource `json:"source"`
	// Ref is the format-specific pointer into the source artifact. Empty
	// when the format doesn't use refs.
	Ref string `json:"ref"`
	// Binding is the selected binding entry. Populated by the operation
	// invoker; optional for direct calls.
	Binding *BindingEntry  `json:"-"`
	Context map[string]any `json:"context,omitempty"`
	// Interface is the containing OBI. Most invokers do not need this;
	// it is used by invokers that invoke sub-operations (e.g., operation graphs).
	Interface *Interface `json:"-"`
	// InputSchema is the operation's input schema, populated by the
	// operation invoker. Enables format-specific invokers to read schema
	// metadata (e.g., const values).
	InputSchema JSONSchema `json:"-"`
	// Hooks is the consumer-hook consultation seam, populated by the
	// operation invoker from its per-Invoke snapshot (per-invocation over
	// invoker-level; nil = builtins only). Process-local — never wire.
	// OperationInvoker.InvokeBinding fills it from invoker-level fields
	// when nil (the in-process binding-layer path).
	Hooks *InvokeHooks `json:"-"`
	// Site identifies this consultation site (canonical operation key,
	// binding, format, ref), constructed and builtin-stamped by the
	// operation invoker; the FORMAT completes Target on its copy before
	// consulting hooks. Process-local — never wire.
	Site *InvokeSite `json:"-"`
	// MaxDeliveryUnitBytes bounds ONE DELIVERY UNIT: the bytes materialized
	// to produce one emitted output value (a unary response body, one SSE
	// event, one streaming envelope, one WebSocket message, one captured
	// stdout). Zero or negative selects DefaultMaxDeliveryUnitBytes; there
	// is no unlimited sentinel — an effectively-unlimited bound is set
	// explicitly huge. Consumer policy, process-local — never wire; the
	// operation invoker stamps its own MaxDeliveryUnitBytes here when the
	// caller left it unset, and direct binding-layer callers set it per
	// invocation. Formats resolve it via DeliveryUnitLimit, never directly.
	MaxDeliveryUnitBytes int64 `json:"-"`
}

// DefaultMaxDeliveryUnitBytes is the delivery-unit bound applied when
// neither the operation invoker nor the binding-layer caller sets
// BindingInvocationArgs.MaxDeliveryUnitBytes: 10 MiB, the SDK's historical
// per-lane cap (cross-SDK parity value).
const DefaultMaxDeliveryUnitBytes = 10 << 20 // 10 MiB

// DeliveryUnitLimit resolves the effective delivery-unit bound for these
// args: MaxDeliveryUnitBytes when positive, DefaultMaxDeliveryUnitBytes
// otherwise. This is the single semantics point for the default — format
// invokers call it at every delivery-unit read site and never re-derive
// the fallback.
func (a *BindingInvocationArgs) DeliveryUnitLimit() int64 {
	if a != nil && a.MaxDeliveryUnitBytes > 0 {
		return a.MaxDeliveryUnitBytes
	}
	return DefaultMaxDeliveryUnitBytes
}

// SynthesizeSource describes a binding source for interface synthesis.
// Content carries raw JSON with the same presence semantics as Source.
type SynthesizeSource struct {
	BindingSpec    string          `json:"bindingSpec"`
	Name           string          `json:"name,omitempty"`
	Location       string          `json:"location,omitempty"`
	Content        json.RawMessage `json:"content,omitempty"`
	OutputLocation string          `json:"outputLocation,omitempty"`
	Embed          bool            `json:"embed,omitempty"`
	Description    string          `json:"description,omitempty"`
}

// SynthesizeInput is the input for synthesizing an OpenBindings interface from format-specific sources.
type SynthesizeInput struct {
	OpenBindingsVersion string             `json:"openbindingsVersion,omitempty"`
	Sources             []SynthesizeSource `json:"sources,omitempty"`
	Name                string             `json:"name,omitempty"`
	Version             string             `json:"version,omitempty"`
	Description         string             `json:"description,omitempty"`

	// OnWarning, when set, is invoked for a non-fatal, usable but lossy schema
	// projection. It must never stand in for omitting a callable target or for
	// returning an operation that is statically guaranteed to refuse; those
	// conditions fail synthesis. nil is acceptable because warning-free use of
	// the returned Interface remains sound.
	OnWarning func(SynthesizerWarning) `json:"-"`
}

// SynthesisSkeleton returns the deterministic source-less result required by
// the interface-synthesizer contract. Family synthesizers use the same helper
// as the combined dispatcher so a source-less call has no family-dependent
// behavior.
func SynthesisSkeleton(in *SynthesizeInput) (Interface, error) {
	version := MaxTestedVersion
	var name, contractVersion, description string
	if in != nil {
		if in.OpenBindingsVersion != "" {
			version = in.OpenBindingsVersion
		}
		name = in.Name
		contractVersion = in.Version
		description = in.Description
	}
	iface := Interface{
		OpenBindings: version,
		Name:         name,
		Version:      contractVersion,
		Description:  description,
		Operations:   map[string]Operation{},
	}
	if err := iface.Validate(); err != nil {
		return Interface{}, fmt.Errorf("source-less synthesis target is invalid: %w", err)
	}
	return iface, nil
}

// FinalizeSynthesis applies the format-neutral authoring directives shared by
// all single-source synthesizers and validates the emitted OBI. Artifact
// acquisition and the embed directive remain family work because only the
// governing binding specification knows what bytes constitute the source.
func FinalizeSynthesis(iface *Interface, in *SynthesizeInput, defaultSourceName, bindingSpec string) error {
	if iface == nil || in == nil || len(in.Sources) != 1 {
		return fmt.Errorf("finalize synthesis requires one source and one interface")
	}
	src := in.Sources[0]
	if src.BindingSpec != bindingSpec {
		return fmt.Errorf("synthesizer supports exact binding specification %q, got %q", bindingSpec, src.BindingSpec)
	}
	if in.OpenBindingsVersion != "" {
		iface.OpenBindings = in.OpenBindingsVersion
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

	entry, ok := iface.Sources[defaultSourceName]
	if !ok {
		return fmt.Errorf("synthesizer emitted no source %q", defaultSourceName)
	}
	entry.BindingSpec = bindingSpec
	if src.OutputLocation != "" {
		entry.Location = src.OutputLocation
	}
	if src.Description != "" {
		entry.Description = src.Description
	}
	outputName := defaultSourceName
	if src.Name != "" {
		outputName = src.Name
	}
	delete(iface.Sources, defaultSourceName)
	iface.Sources[outputName] = entry
	if outputName != defaultSourceName {
		for key, binding := range iface.Bindings {
			if binding.Source == defaultSourceName {
				binding.Source = outputName
				iface.Bindings[key] = binding
			}
		}
	}
	if err := iface.Validate(); err != nil {
		return fmt.Errorf("synthesized interface is invalid: %w", err)
	}
	return nil
}

// SynthesizerWarning describes a non-fatal, usable but lossy projection made
// while building an Interface. Warnings do not block synthesis; every returned
// operation remains bindable. Consumers may surface warnings in tooling output
// (CLI, registry publish checks, CI) to inform users about lossy conversions.
type SynthesizerWarning struct {
	// Code is a stable machine-readable identifier for programmatic handling.
	// Format-specific codes should be namespaced with the format token as a
	// prefix (e.g., "grpc.multi_group_oneof").
	Code string `json:"code"`
	// Message is a human-readable description of the limitation.
	Message string `json:"message"`
	// Path identifies the location within the Interface that the warning
	// refers to, using dotted notation (e.g., "operations.GetItem.input").
	// Empty when the warning applies to the whole interface.
	Path string `json:"path,omitempty"`
	// Details carries format-specific context. May be nil.
	Details map[string]any `json:"details,omitempty"`
}

// SynthesisCoverageScope identifies the granularity of a synthesis coverage
// entry.
type SynthesisCoverageScope string

const (
	// SynthesisCoverageTarget is an addressable source interaction.
	SynthesisCoverageTarget SynthesisCoverageScope = "target"
	// SynthesisCoverageAlternative is an independently selectable alternative
	// whose omission would remove a source-permitted invocation path.
	SynthesisCoverageAlternative SynthesisCoverageScope = "alternative"
	// SynthesisCoverageProjection is a source contract projection into an OBI
	// schema or operation framing.
	SynthesisCoverageProjection SynthesisCoverageScope = "projection"
)

// SynthesisCoverageStatus is the durable disposition of one observed source
// interaction unit.
type SynthesisCoverageStatus string

const (
	// SynthesisRepresented means the emitted OBI carries an operation and
	// binding path for the unit.
	SynthesisRepresented SynthesisCoverageStatus = "represented"
	// SynthesisExcluded means the governing binding-specification revision
	// explicitly excludes the upstream-valid unit.
	SynthesisExcluded SynthesisCoverageStatus = "excluded"
	// SynthesisInvalid means the source unit is malformed or internally
	// contradictory under its upstream authority.
	SynthesisInvalid SynthesisCoverageStatus = "invalid"
	// SynthesisLossy means invocation remains usable but the emitted OBI
	// framing cannot express part of the source contract exactly.
	SynthesisLossy SynthesisCoverageStatus = "lossy"
	// SynthesisImplementationUnsupported means the binding revision admits the
	// unit but this synthesizer cannot represent it. Reference SDK releases
	// must carry no such entries.
	SynthesisImplementationUnsupported SynthesisCoverageStatus = "implementation-unsupported"
)

// SynthesisCoverageEntry records the disposition of one source interaction
// unit observed during the same call that produced the synthesized Interface.
type SynthesisCoverageEntry struct {
	// SourceIndex is the zero-based index of the input source containing the
	// interaction unit.
	SourceIndex int `json:"sourceIndex"`
	// SourceKey is the corresponding source key in the emitted OBI. It is
	// required for represented and lossy entries.
	SourceKey string `json:"sourceKey,omitempty"`
	// SourceRef is a stable source-local identifier. It need not be a conformant
	// binding ref for an excluded or invalid unit.
	SourceRef string `json:"sourceRef"`
	// Scope distinguishes addressable targets from independently selectable
	// alternatives of a target.
	Scope SynthesisCoverageScope `json:"scope"`
	// Status is the unit's durable disposition.
	Status SynthesisCoverageStatus `json:"status"`
	// OperationKey, BindingKey, and BindingRef are required for represented
	// and lossy entries.
	// BindingRef is serialized even when empty because some binding
	// specifications identify a valid target by an omitted ref.
	OperationKey string `json:"operationKey,omitempty"`
	BindingKey   string `json:"bindingKey,omitempty"`
	BindingRef   string `json:"bindingRef"`
	// ReasonCode and Message are required for every non-represented entry.
	// ReasonCode is stable and family-namespaced; Message is diagnostic prose.
	ReasonCode string `json:"reasonCode,omitempty"`
	Rule       string `json:"rule,omitempty"`
	Message    string `json:"message,omitempty"`
	// Requirements names runtime prerequisites without treating them as
	// synthesis omissions.
	Requirements []string `json:"requirements,omitempty"`
	// Details carries family-specific structured evidence.
	Details map[string]any `json:"details,omitempty"`
}

// SynthesisCoverage is durable coverage evidence for one synthesis call.
type SynthesisCoverage struct {
	Entries []SynthesisCoverageEntry `json:"entries"`
	// Exhaustive is true when every interaction unit in every accepted source,
	// under the governing binding revision's inventory, has one disposition.
	Exhaustive bool `json:"exhaustive"`
	// FullyRepresented is true when Exhaustive is true and every upstream-valid
	// unit is represented. NewSynthesisResult derives it from Entries.
	FullyRepresented bool `json:"fullyRepresented"`
	// Limitation explains what may be missing when Exhaustive is false.
	Limitation *SynthesisCoverageLimitation `json:"limitation,omitempty"`
}

// SynthesisCoverageLimitation explains why a synthesis inventory is not
// exhaustive.
type SynthesisCoverageLimitation struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// SynthesizeResult carries a creation-time-sound Interface and the coverage
// evidence derived from the same synthesis observation.
type SynthesizeResult struct {
	Interface *Interface        `json:"interface"`
	Coverage  SynthesisCoverage `json:"coverage"`
}

// BindingSpecInfo describes a binding specification supported by an
// invoker, by exact identifier.
type BindingSpecInfo struct {
	BindingSpec string `json:"bindingSpec"`
	Description string `json:"description,omitempty"`
}

// SourceInspection is the result of inspecting a source for bindable targets.
type SourceInspection struct {
	// Targets is the list of bindable targets discovered in the source.
	Targets []BindableTarget `json:"targets"`
	// Exhaustive is true when this is the complete list of targets for the
	// source. When false, additional targets may exist that were not enumerated.
	Exhaustive bool `json:"exhaustive"`
	// Limitation explains a non-exhaustive result. It is required when
	// Exhaustive is false.
	Limitation *InspectionLimitation `json:"limitation,omitempty"`
}

// InspectionLimitation explains why a source inspection is not exhaustive.
type InspectionLimitation struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// BindableTarget describes a target within a source that can be framed as an
// OpenBindings operation.
type BindableTarget struct {
	// Ref is the reference string to use in a binding entry.
	Ref string `json:"ref"`
	// OperationKey is an optional suggested operation key for this target.
	OperationKey string `json:"operationKey,omitempty"`
	// Operation is an optional OpenBindings operation framing for this target.
	Operation *Operation `json:"operation,omitempty"`
}

// InvocationError is the minimal unsuccessful-completion record used by the
// invocation interfaces. Code and Message describe the terminal condition.
// Details carries portable data defined by an interface-owned code (currently
// CONTEXT_REQUIRED and validation failures), or an opaque JSON failure value
// identified as application-authored by the governing binding rules.
// Diagnostics is an optional expert escape hatch for selected-binding or
// implementation evidence; ordinary operation behavior must not depend on it.
type InvocationError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Details     any    `json:"details,omitempty"`
	Diagnostics any    `json:"diagnostics,omitempty"`
}

// ValidationFailure is a single schema-validation failure (OBI-T-16 claim semantics)
// in a stable, validator-agnostic shape. When validation produces multiple
// failures (e.g., several fields violating the schema), each one appears
// as a separate ValidationFailure in InvocationError.Details.Failures.
type ValidationFailure struct {
	// Path is a JSON Pointer into the instance, e.g. "/results/0/name".
	// Empty string means the root.
	Path string `json:"path"`
	// Message is a human-readable diagnostic.
	Message string `json:"message"`
	// SchemaPath is an optional JSON Pointer into the schema.
	SchemaPath string `json:"schemaPath,omitempty"`
}

// ValidationFailureDetails is the typed shape of InvocationError.Details
// for input/output validation failures (OBI-T-16).
type ValidationFailureDetails struct {
	Failures []ValidationFailure `json:"failures"`
}

func (e *InvocationError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Message
	if msg == "" {
		msg = e.Code
	}
	// A CONTEXT_REQUIRED challenge is actionable only through its details;
	// an error string that hides the target and the requirement families
	// strands whoever sees it in a log. Formats write the prose, the
	// challenge writes the facts.
	if d := ContextRequiredFrom(e); d != nil {
		if summary := contextRequirementSummary(d); summary != "" {
			return msg + " (" + summary + ")"
		}
	}
	return msg
}

// contextRequirementSummary renders "target: <t>; satisfied by: auth.bearer
// (context field \"bearerToken\"), ..." for the challenge's alternatives.
func contextRequirementSummary(d *ContextRequiredDetails) string {
	var alts []string
	for _, alt := range d.Alternatives {
		var reqs []string
		for _, req := range alt.Requirements {
			if field, ok := requirementFields[req.Type]; ok {
				reqs = append(reqs, fmt.Sprintf("%s (context field %q)", req.Type, field))
			} else {
				reqs = append(reqs, req.Type)
			}
		}
		if len(reqs) > 0 {
			alts = append(alts, strings.Join(reqs, " + "))
		}
	}
	if d.Target == "" && len(alts) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if d.Target != "" {
		parts = append(parts, "target: "+d.Target)
	}
	if len(alts) > 0 {
		parts = append(parts, "satisfied by: "+strings.Join(alts, ", or "))
	}
	return strings.Join(parts, "; ")
}

// RedactContext returns a shallow copy of ctx with well-known credential
// fields replaced by "[REDACTED]". Returns nil for nil input. Other fields
// may also contain secrets according to their binding specification or
// application meaning, so the result is not automatically safe to log without
// an application-specific second pass.
//
// The context-confidentiality invariant: no context value the standard
// credential taxonomy classifies as secret survives, in cleartext, to any
// diagnostic surface. credentialFieldNames is the closed redaction registry;
// ScopeContext separately admits only the family selected by a challenge.
// Tests guard both registries as the standard taxonomy evolves. The taxonomy
// itself comes from the binding-invoker interface's context table and
// confidentiality clause. Flat credential fields redact to "[REDACTED]";
// nested credential fields retain their identifiers (basic keeps its username,
// apiKeys keeps its scheme names) and redact the known secret values. (Store
// KEYS are the one surface this cannot reach — see NormalizeContextKey, which
// strips userinfo so no secret rides into a key.)
func RedactContext(ctx map[string]any) map[string]any {
	if ctx == nil {
		return nil
	}
	redacted := make(map[string]any, len(ctx))
	for k, v := range ctx {
		if !isCredentialField(k) {
			// Unknown fields pass through because this generic helper cannot
			// infer their structure. Callers must additionally redact fields
			// their binding or application classifies as sensitive.
			redacted[k] = v
			continue
		}
		switch k {
		case "basic":
			if m, ok := v.(map[string]any); ok {
				rc := make(map[string]any, len(m))
				for bk, bv := range m {
					if bk == "password" {
						rc[bk] = "[REDACTED]"
					} else {
						rc[bk] = bv
					}
				}
				redacted[k] = rc
			} else {
				redacted[k] = v
			}
		case "apiKeys":
			// Scheme-scoped API keys (R2.d ruling): every named entry is
			// credential material, same as the single 'apiKey' field. Scheme
			// names stay; values are redacted.
			if m, ok := v.(map[string]any); ok {
				rc := make(map[string]any, len(m))
				for name := range m {
					rc[name] = "[REDACTED]"
				}
				redacted[k] = rc
			} else {
				redacted[k] = v
			}
		default:
			// Flat credential fields: bearerToken, apiKey, accessToken,
			// refreshToken, clientSecret.
			redacted[k] = "[REDACTED]"
		}
	}
	return redacted
}

// Well-known context field helpers.
// These extract conventional credential fields from opaque context for
// cross-invoker interoperability. Invokers SHOULD store credentials
// using these field names.

// ContextBearerToken returns the well-known bearerToken field from context.
func ContextBearerToken(ctx map[string]any) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx["bearerToken"].(string)
	return v
}

// ContextAPIKey returns the well-known apiKey field from context.
func ContextAPIKey(ctx map[string]any) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx["apiKey"].(string)
	return v
}

// ContextAPIKeyFor returns the API key for a NAMED scheme: the scheme-scoped
// context.apiKeys[name] entry first (the form BindingContext defines for an
// alternative that ANDs several API keys at once — two ANDed apiKey schemes
// are otherwise indistinguishable), falling back to the single well-known
// apiKey convenience field. name == "" behaves exactly like ContextAPIKey
// (an unnamed requirement has no named entry to prefer). Every format
// invoker's credential application (openapi, asyncapi) and the core
// satisfaction/scoping logic (requirementSatisfied, admitRequirement) share
// this one lookup rather than duplicating the priority per call site.
func ContextAPIKeyFor(ctx map[string]any, name string) string {
	if ctx == nil {
		return ""
	}
	if name != "" {
		if keys, ok := ctx["apiKeys"].(map[string]any); ok {
			if v, ok := keys[name].(string); ok && v != "" {
				return v
			}
		}
	}
	return ContextAPIKey(ctx)
}

// ContextBasicAuth returns the well-known basic auth fields from context.
func ContextBasicAuth(ctx map[string]any) (username, password string, ok bool) {
	if ctx == nil {
		return "", "", false
	}
	basic, _ := ctx["basic"].(map[string]any)
	if basic == nil {
		return "", "", false
	}
	u, _ := basic["username"].(string)
	p, _ := basic["password"].(string)
	if u == "" && p == "" {
		return "", "", false
	}
	return u, p, true
}

// ContextString returns a string value from context by key.
func ContextString(ctx map[string]any, key string) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx[key].(string)
	return v
}

// ContextHeaders extracts the well-known 'headers' field from context as a
// typed map[string]string. Returns nil if absent or not a string map.
func ContextHeaders(ctx map[string]any) map[string]string {
	return extractStringMap(ctx, "headers")
}

// ContextCookies extracts the well-known 'cookies' field from context.
func ContextCookies(ctx map[string]any) map[string]string {
	return extractStringMap(ctx, "cookies")
}

// ContextEnvironment extracts the well-known 'environment' field from context.
func ContextEnvironment(ctx map[string]any) map[string]string {
	return extractStringMap(ctx, "environment")
}

// ContextMetadata extracts the well-known 'metadata' field from context.
// Returns nil if absent or not an object.
func ContextMetadata(ctx map[string]any) map[string]any {
	if ctx == nil {
		return nil
	}
	v, _ := ctx["metadata"].(map[string]any)
	return v
}

// ContextConfiguration extracts the well-known 'configuration' field from
// context: per-invocation configuration-point values, keyed by point name —
// the operation-invoker contract's 'selection' point, and the named points
// each binding specification defines for its family (decode, classify,
// route, solicit, server, address, target, transport). The values' meanings
// belong to whichever specification defines the point; this helper only
// provides the carriage. Returns nil if absent or not an object.
func ContextConfiguration(ctx map[string]any) map[string]any {
	if ctx == nil {
		return nil
	}
	v, _ := ctx["configuration"].(map[string]any)
	return v
}

// contextSelectionOverride extracts the operation-invoker contract's
// 'selection' configuration point from context: an ordered list of binding
// keys, the first invocable entry winning. Accepts []string or a JSON-parsed
// []any of strings; anything else is no override.
func contextSelectionOverride(ctx map[string]any) []string {
	cfg := ContextConfiguration(ctx)
	if cfg == nil {
		return nil
	}
	switch v := cfg["selection"].(type) {
	case []string:
		return v
	case []any:
		keys := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			keys = append(keys, s)
		}
		return keys
	}
	return nil
}

func extractStringMap(ctx map[string]any, key string) map[string]string {
	if ctx == nil {
		return nil
	}
	raw, ok := ctx[key].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// NormalizeEndpoint normalizes a remote endpoint URL to a stable context key:
// it URL-parses the input and returns NormalizeContextKey over
// "scheme://host[:port]" (so a port matching the scheme's default is elided,
// same as NormalizeContextKey), falling back to NormalizeContextKey(raw) for
// non-URL strings. The store-backed resolver uses this to derive a storage
// key from a challenge's target — it matches the TypeScript SDK's
// normalizeEndpoint so keys are identical across languages.
func NormalizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return NormalizeContextKey(u.Scheme + "://" + u.Host)
	}
	return NormalizeContextKey(raw)
}

// NormalizeContextKey normalizes a URL to a stable context store key.
// The key is host[:port] (scheme, path, query, and fragment are stripped)
// to enable cross-invoker credential sharing for the same API origin. The
// host is lowercased and any userinfo (user[:password]@) is stripped: DNS
// hosts are case-insensitive and userinfo is not part of a host (RFC 3986
// §3.2.2/§6.2.2.1), and a secret in a store key is the one confidentiality
// leak RedactContext cannot reach. When the input carries a scheme, an
// explicit port matching that scheme's default (443 for https/wss, 80 for
// http/ws) is elided, so a key written with the default port and one written
// without it collide; any other explicit port is kept as-is. Strings without
// a scheme (e.g. a gRPC "host:port" format-defined address) are returned
// as-is: with no scheme there is no known default, and eliding a port there
// would corrupt a format-defined address.
//
// The keying rule (normalize to the host — lowercased, userinfo excluded) is
// owned by the binding-invoker interface's context table; this is its
// implementation, shared with NormalizeEndpoint (the read path) so write and
// read derive identical keys, and pinned byte-for-byte to the TypeScript
// SDK's normalizeContextKey.
func NormalizeContextKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	// Strip scheme — the context key is just host[:port].
	// Protocol is irrelevant to origin identity.
	idx := strings.Index(raw, "://")
	if idx < 0 {
		return raw
	}
	scheme := raw[:idx]
	host := raw[idx+3:]
	if slashIdx := strings.Index(host, "/"); slashIdx >= 0 {
		host = host[:slashIdx]
	}
	if qIdx := strings.Index(host, "?"); qIdx >= 0 {
		host = host[:qIdx]
	}
	if hIdx := strings.Index(host, "#"); hIdx >= 0 {
		host = host[:hIdx]
	}
	// Strip userinfo (user[:password]@): it is not part of a host, and a
	// password must never ride into a store key — the one surface redaction
	// cannot scrub. There is at most one '@' in a conformant authority.
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	// Case-fold the host: DNS hostnames are case-insensitive, so
	// API.example.com and api.example.com are one origin and derive one key.
	// The port is numeric, so lowercasing the whole authority leaves it
	// unchanged; an IPv6 literal folds to its canonical lowercase form.
	host = strings.ToLower(host)
	return elideDefaultPort(scheme, host)
}

// elideDefaultPort strips an explicit port from host when it equals the
// given scheme's default port (443 for https/wss, 80 for http/ws), so a
// context key written with the default port and one written without it
// resolve to the same key. Any other explicit port, and any scheme with no
// known default, is returned unchanged. The scheme is lowercased for the
// comparison only; it is never part of the returned key.
func elideDefaultPort(scheme, host string) string {
	switch strings.ToLower(scheme) {
	case "https", "wss":
		return strings.TrimSuffix(host, ":443")
	case "http", "ws":
		return strings.TrimSuffix(host, ":80")
	default:
		return host
	}
}
