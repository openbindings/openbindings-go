package openbindings

import (
	"context"
	"encoding/json"
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

// PlatformCallbacks are functions injected into invokers so they can
// interact with the runtime environment without knowing what platform
// they're running on. Each field is nil when the capability is unavailable.
type PlatformCallbacks struct {
	BrowserRedirect func(ctx context.Context, url string) (*BrowserRedirectResult, error)
	Prompt          func(ctx context.Context, message string, opts *PromptOptions) (string, error)
	Confirmation    func(ctx context.Context, message string) (bool, error)
	FileSelect      func(ctx context.Context, message string, opts *FileSelectOptions) (string, error)
}

// ---------------------------------------------------------------------------
// Invocation types
// ---------------------------------------------------------------------------

// BindingInvocationSource identifies the binding source for invocation.
type BindingInvocationSource struct {
	Format   string `json:"format"`
	Location string `json:"location,omitempty"`
	Content  any    `json:"content,omitempty"`
}

// BindingInvocationInput is the input for invoking a binding against a
// format-specific source. The OperationInvoker populates Context from the
// ContextStore when available; Store and Callbacks let the invoker
// persist updated context and invoke platform interactions during invocation.
// Security is populated by the OperationInvoker from the OBI's security
// section when the binding has a security reference. InputSchema is populated
// from the operation's input schema when available, enabling format-specific
// invokers to read schema metadata (e.g., const values) that inform how the
// binding is invoked.
type BindingInvocationInput struct {
	Source      BindingInvocationSource `json:"source"`
	Ref         string                  `json:"ref"`
	Input       any                     `json:"input,omitempty"`
	InputSchema JSONSchema              `json:"-"`
	Context     map[string]any          `json:"context,omitempty"`
	Store       ContextStore            `json:"-"`
	Callbacks   *PlatformCallbacks      `json:"-"`
	Security    []SecurityMethod        `json:"-"`
	// Interface is the containing OBI. Most invokers do not need this;
	// it is used by invokers that invoke sub-operations (e.g., operation graphs).
	Interface *Interface `json:"-"`
}

// OperationInvocationInput is the input for invoking an OBI operation.
// The OperationInvoker resolves the binding internally.
type OperationInvocationInput struct {
	Interface *Interface     `json:"interface"`
	Operation string         `json:"operation"`
	Input     any            `json:"input,omitempty"`
	Context   map[string]any `json:"context,omitempty"`
	// When set, bypass the binding selector and use this binding key directly.
	BindingKey string `json:"bindingKey,omitempty"`
}

// SecurityMethod describes an authentication method available for a binding.
// Per spec §6.6, only Type (required) and Description (optional) are spec-defined;
// all other fields are open-ended and scheme-specific. Use Extra to read/write
// scheme-specific fields (e.g., "authorizeUrl", "tokenUrl" for oauth2).
type SecurityMethod struct {
	Type        string         `json:"type"`
	Description string         `json:"description,omitempty"`
	Extra       map[string]any `json:"-"`
}

func (m SecurityMethod) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 2+len(m.Extra))
	for k, v := range m.Extra {
		out[k] = v
	}
	out["type"] = m.Type
	if m.Description != "" {
		out["description"] = m.Description
	}
	return json.Marshal(out)
}

func (m *SecurityMethod) UnmarshalJSON(b []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if t, ok := raw["type"].(string); ok {
		m.Type = t
	}
	if d, ok := raw["description"].(string); ok {
		m.Description = d
	}
	delete(raw, "type")
	delete(raw, "description")
	if len(raw) > 0 {
		m.Extra = raw
	}
	return nil
}

// ExtraString returns a string field from Extra, or "" if absent/wrong type.
func (m SecurityMethod) ExtraString(key string) string {
	if m.Extra == nil {
		return ""
	}
	s, _ := m.Extra[key].(string)
	return s
}

// ExtraStringSlice returns a string slice field from Extra, or nil if absent.
// Handles both []string (in-memory construction) and []any (after JSON round-trip).
func (m SecurityMethod) ExtraStringSlice(key string) []string {
	if m.Extra == nil {
		return nil
	}
	switch v := m.Extra[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, elem := range v {
			if s, ok := elem.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// InvocationOutput is a single output produced by an operation invocation.
// Unary invocations produce one InvocationOutput; streaming invocations
// produce many over time. Each one may carry:
//   - Output only — success.
//   - Error only — failure prior to producing output (transport error, input
//     validation, transform failure, schema compilation failure).
//   - Output AND Error — OBI-T-08 output validation failed against the
//     declared output schema. The data is still surfaced so callers may
//     inspect or render it, while the error reports the schema mismatch.
type InvocationOutput struct {
	Output     any              `json:"output,omitempty"`
	Error      *InvocationError `json:"error,omitempty"`
	Status     int              `json:"status,omitempty"`     // HTTP status or exit code
	DurationMs int64            `json:"durationMs,omitempty"` // invocation duration
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

// SingleEventChannel wraps a single InvocationOutput as a closed, buffered
// channel of InvocationOutput. Convenience for invokers that return a single
// unary result.
func SingleEventChannel(output *InvocationOutput) <-chan InvocationOutput {
	ch := make(chan InvocationOutput, 1)
	if output != nil {
		ch <- *output
	}
	close(ch)
	return ch
}

// InvocationError represents a structured invocation error.
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
