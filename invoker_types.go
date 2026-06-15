package openbindings

import (
	"context"
	"net/url"
	"strings"
)

// ---------------------------------------------------------------------------
// Context store
// ---------------------------------------------------------------------------

// ContextStore is a key-value store for binding invocation context.
// Keys are invoker-determined strings (typically a normalized API origin).
// Values are opaque credential maps using well-known field names for
// cross-invoker interoperability.
//
// The SDK stores and retrieves context but never inspects its contents.
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
}

// OperationInvocationArgs are the arguments for invoking an OBI operation.
// The invoker resolves the operation name (OBI-T-12), selects a binding
// (OBI-T-09), and returns an Invocation handle; input messages flow through
// the handle.
type OperationInvocationArgs struct {
	Interface *Interface     `json:"interface"`
	Operation string         `json:"operation"`
	Context   map[string]any `json:"context,omitempty"`
	// BindingKey, when set, bypasses the binding selector and uses this
	// binding key directly.
	BindingKey string `json:"bindingKey,omitempty"`
}

// CreateSource describes a binding source for interface creation.
type CreateSource struct {
	Format         string `json:"format"`
	Name           string `json:"name,omitempty"`
	Location       string `json:"location,omitempty"`
	Content        any    `json:"content,omitempty"`
	OutputLocation string `json:"outputLocation,omitempty"`
	Embed          bool   `json:"embed,omitempty"`
	Description    string `json:"description,omitempty"`
}

// CreateInput is the input for creating an OpenBindings interface from format-specific sources.
type CreateInput struct {
	OpenBindingsVersion string         `json:"openbindingsVersion,omitempty"`
	Sources             []CreateSource `json:"sources,omitempty"`
	Name                string         `json:"name,omitempty"`
	Version             string         `json:"version,omitempty"`
	Description         string         `json:"description,omitempty"`

	// OnWarning, when set, is invoked by creators that encounter non-fatal
	// limitations during interface construction (e.g., a source-side feature
	// the schema profile cannot fully express). The creator still produces
	// a valid Interface; the warning surfaces what was lost or approximated.
	// nil is acceptable and means warnings are dropped silently.
	OnWarning func(CreatorWarning) `json:"-"`
}

// CreatorWarning describes a non-fatal limitation encountered while building
// an Interface. Warnings do not block creation; the returned Interface is
// still valid and usable. Consumers may surface warnings in tooling output
// (CLI, registry publish checks, CI) to inform users about lossy conversions.
type CreatorWarning struct {
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

// ValidationFailure is a single OBI-T-07/T-08 schema-validation failure
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
// for OBI-T-07 / OBI-T-08 validation failures.
type ValidationFailureDetails struct {
	Failures []ValidationFailure `json:"failures"`
}

func (e *InvocationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
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
// it URL-parses the input (lowercasing the host, stripping userinfo) and
// returns NormalizeContextKey over the host[:port], falling back to
// NormalizeContextKey(raw) for non-URL strings. The store-backed resolver uses
// this to derive a storage key from a challenge's target — it matches the
// TypeScript SDK's normalizeEndpoint so keys are identical across languages.
func NormalizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return NormalizeContextKey(u.Host)
	}
	return NormalizeContextKey(raw)
}

// NormalizeContextKey normalizes a URL to a stable context store key.
// The key is host[:port] (scheme, path, query, and fragment are stripped)
// to enable cross-invoker credential sharing for the same API origin.
// Non-URL strings are returned as-is.
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
	return host
}
