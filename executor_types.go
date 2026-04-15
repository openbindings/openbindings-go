package openbindings

import (
	"context"
	"strings"
)

// ---------------------------------------------------------------------------
// Context store
// ---------------------------------------------------------------------------

// ContextStore is a key-value store for binding execution context.
// Keys are executor-determined strings (typically a normalized API origin).
// Values are opaque credential maps using well-known field names for
// cross-executor interoperability.
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

// PlatformCallbacks are functions injected into executors so they can
// interact with the runtime environment without knowing what platform
// they're running on. Each field is nil when the capability is unavailable.
type PlatformCallbacks struct {
	BrowserRedirect func(ctx context.Context, url string) (*BrowserRedirectResult, error)
	Prompt          func(ctx context.Context, message string, opts *PromptOptions) (string, error)
	Confirmation    func(ctx context.Context, message string) (bool, error)
	FileSelect      func(ctx context.Context, message string, opts *FileSelectOptions) (string, error)
}

// ---------------------------------------------------------------------------
// Execution types
// ---------------------------------------------------------------------------

// BindingExecutionSource identifies the binding source for execution.
type BindingExecutionSource struct {
	Format   string `json:"format"`
	Location string `json:"location,omitempty"`
	Content  any    `json:"content,omitempty"`
}

// BindingExecutionInput is the input for executing a binding against a
// format-specific source. The OperationExecutor populates Context from the
// ContextStore when available; Store and Callbacks let the executor
// persist updated context and invoke platform interactions during execution.
// Security is populated by the OperationExecutor from the OBI's security
// section when the binding has a security reference. InputSchema is populated
// from the operation's input schema when available, enabling format-specific
// executors to read schema metadata (e.g., const values) that inform how the
// binding is executed.
type BindingExecutionInput struct {
	Source      BindingExecutionSource      `json:"source"`
	Ref         string             `json:"ref"`
	Input       any                `json:"input,omitempty"`
	InputSchema JSONSchema         `json:"-"`
	Context     map[string]any     `json:"-"`
	Options     *ExecutionOptions  `json:"options,omitempty"`
	Store       ContextStore       `json:"-"`
	Callbacks   *PlatformCallbacks `json:"-"`
	Security    []SecurityMethod   `json:"-"`
	// Interface is the containing OBI. Most executors do not need this;
	// it is used by executors that invoke sub-operations (e.g., operation graphs).
	Interface   *Interface         `json:"-"`
}

// OperationExecutionInput is the input for executing an OBI operation.
// The OperationExecutor resolves the binding internally.
type OperationExecutionInput struct {
	Interface  *Interface        `json:"interface"`
	Operation  string            `json:"operation"`
	Input      any               `json:"input,omitempty"`
	Context    map[string]any    `json:"context,omitempty"`
	Options    *ExecutionOptions `json:"options,omitempty"`
	// When set, bypass the binding selector and use this binding key directly.
	BindingKey string            `json:"bindingKey,omitempty"`
}

// ExecutionOptions holds developer-supplied per-request settings passed through
// to the executor. Unlike context, options are not stored or resolved.
type ExecutionOptions struct {
	Headers     map[string]string `json:"headers,omitempty"`
	Cookies     map[string]string `json:"cookies,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Metadata    map[string]any    `json:"metadata,omitempty"`
}

// SecurityMethod describes an authentication method available for a binding.
// Methods are walked in preference order; clients pick the first they support.
// The Type field is community-driven and extensible (like format tokens).
// Well-known types: "bearer", "oauth2", "basic", "apiKey".
type SecurityMethod struct {
	Type         string   `json:"type"`
	Description  string   `json:"description,omitempty"`
	AuthorizeURL string   `json:"authorizeUrl,omitempty"` // oauth2
	TokenURL     string   `json:"tokenUrl,omitempty"`     // oauth2
	Scopes       []string `json:"scopes,omitempty"`       // oauth2
	ClientID     string   `json:"clientId,omitempty"`     // oauth2: optional client identifier
	Name         string   `json:"name,omitempty"`         // apiKey: header/query/cookie name
	In           string   `json:"in,omitempty"`           // apiKey: "header", "query", or "cookie"
}

// ExecuteOutput is the result of an operation execution.
type ExecuteOutput struct {
	Output     any           `json:"output,omitempty"`
	Status     int           `json:"status"`
	DurationMs int64         `json:"durationMs,omitempty"`
	Error      *ExecuteError `json:"error,omitempty"`
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
}

// FormatInfo describes a binding format supported by an executor.
type FormatInfo struct {
	Token       string `json:"token"`
	Description string `json:"description,omitempty"`
}

// ListRefsResult is the result of listing bindable refs from a source.
type ListRefsResult struct {
	// Refs is the list of available bindable references.
	Refs []BindableRef `json:"refs"`
	// Exhaustive is true when this is the complete list of refs for the source.
	// When false, additional refs may exist that were not enumerated (e.g., the
	// source was partially parsed, or the format supports dynamic refs).
	Exhaustive bool `json:"exhaustive"`
}

// BindableRef describes a single bindable reference within a source.
type BindableRef struct {
	// Ref is the reference string to use in a binding entry.
	Ref string `json:"ref"`
	// Description is an optional human-readable description.
	Description string `json:"description,omitempty"`
}

// StreamEvent represents a single event received from a streaming subscription.
type StreamEvent struct {
	Data       any           `json:"data,omitempty"`
	Error      *ExecuteError `json:"error,omitempty"`
	Status     int           `json:"status,omitempty"`     // HTTP status or exit code, carried for unary compat
	DurationMs int64         `json:"durationMs,omitempty"` // execution duration, carried for unary compat
}

// SingleEventChannel wraps a single ExecuteOutput as a closed, buffered
// channel of StreamEvent. Convenience for executors that return unary results.
func SingleEventChannel(output *ExecuteOutput) <-chan StreamEvent {
	ch := make(chan StreamEvent, 1)
	ev := StreamEvent{Status: output.Status, DurationMs: output.DurationMs}
	if output.Error != nil {
		ev.Error = output.Error
	} else {
		ev.Data = output.Output
	}
	ch <- ev
	close(ch)
	return ch
}

// ExecuteError represents a structured execution error.
type ExecuteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *ExecuteError) Error() string {
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
// cross-executor interoperability. Executors SHOULD store credentials
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

// NormalizeContextKey normalizes a URL to a stable context store key.
// The key is host[:port] (scheme, path, query, and fragment are stripped)
// to enable cross-executor credential sharing for the same API origin.
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
