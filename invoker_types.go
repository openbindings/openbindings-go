package openbindings

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ---------------------------------------------------------------------------
// Context store
// ---------------------------------------------------------------------------

// ContextStore is a key-value store for binding invocation context.
// Keys are invoker-determined strings (typically a normalized API origin).
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
// The SDK stores and retrieves context but never inspects its contents.
// Setting a nil value removes the entry (the published contract pins
// set-null ≡ delete, so Get's nil uniformly means "no entry").
// The published openbindings.key-value-store interface standardizes this
// same get/set/delete capability where a store sits across a wire.
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

// InvocationSource identifies the binding source for invocation.
type InvocationSource struct {
	Format   string `json:"format"`
	Location string `json:"location,omitempty"`
	Content  any    `json:"content,omitempty"`
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
}

// SynthesizeSource describes a binding source for interface synthesis.
type SynthesizeSource struct {
	Format         string `json:"format"`
	Name           string `json:"name,omitempty"`
	Location       string `json:"location,omitempty"`
	Content        any    `json:"content,omitempty"`
	OutputLocation string `json:"outputLocation,omitempty"`
	Embed          bool   `json:"embed,omitempty"`
	Description    string `json:"description,omitempty"`
}

// SynthesizeInput is the input for synthesizing an OpenBindings interface from format-specific sources.
type SynthesizeInput struct {
	OpenBindingsVersion string             `json:"openbindingsVersion,omitempty"`
	Sources             []SynthesizeSource `json:"sources,omitempty"`
	Name                string             `json:"name,omitempty"`
	Version             string             `json:"version,omitempty"`
	Description         string             `json:"description,omitempty"`

	// OnWarning, when set, is invoked by synthesizers that encounter non-fatal
	// limitations during interface construction (e.g., a source-side feature
	// the schema profile cannot fully express). The synthesizer still produces
	// a valid Interface; the warning surfaces what was lost or approximated.
	// nil is acceptable and means warnings are dropped silently.
	OnWarning func(SynthesizerWarning) `json:"-"`
}

// SynthesizerWarning describes a non-fatal limitation encountered while building
// an Interface. Warnings do not block synthesis; the returned Interface is
// still valid and usable. Consumers may surface warnings in tooling output
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

// FormatInfo describes a binding format supported by an invoker.
type FormatInfo struct {
	Token       string `json:"token"`
	Description string `json:"description,omitempty"`
}

// SourceInspection is the result of inspecting a source for bindable targets.
type SourceInspection struct {
	// Targets is the list of bindable targets discovered in the source.
	Targets []BindableTarget `json:"targets"`
	// Exhaustive is true when this is the complete list of targets for the
	// source. When false, additional targets may exist that were not enumerated.
	Exhaustive bool `json:"exhaustive"`
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

// InvocationError is the structured error type for all terminal invocation
// failures.
type InvocationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
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
// fields replaced by "[REDACTED]". Safe for logging and error messages.
// Returns nil for nil input.
func RedactContext(ctx map[string]any) map[string]any {
	if ctx == nil {
		return nil
	}
	redacted := make(map[string]any, len(ctx))
	for k, v := range ctx {
		switch k {
		case "bearerToken", "apiKey", "refreshToken", "accessToken", "clientSecret":
			redacted[k] = "[REDACTED]"
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
			// credential material, same as the single 'apiKey' field.
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
			redacted[k] = v
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
// to enable cross-invoker credential sharing for the same API origin. When
// the input carries a scheme, an explicit port matching that scheme's
// default (443 for https/wss, 80 for http/ws) is elided, so a key written
// with the default port and one written without it collide; any other
// explicit port is kept as-is. Strings without a scheme (e.g. a gRPC
// "host:port" format-defined address) are returned as-is: with no scheme
// there is no known default, and eliding a port there would corrupt a
// format-defined address.
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
