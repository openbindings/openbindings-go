package openbindings

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// InterfaceClientState represents the current state of an InterfaceClient.
type InterfaceClientState string

const (
	StateIdle         InterfaceClientState = "idle"
	StateResolving    InterfaceClientState = "resolving"
	StateBound        InterfaceClientState = "bound"
	StateIncompatible InterfaceClientState = "incompatible"
	StateError        InterfaceClientState = "error"
)

// InterfaceClient is a stateful object that resolves an OBI from a target
// service and optionally validates it against a required interface. Once
// resolved, operations can be executed through it.
//
// Demand mode — pass a required *Interface to enforce compatibility:
// "I need these capabilities — find me something compatible and let me use it."
//
// Discovery mode — pass nil to accept any service unconditionally:
// "Connect to this service and show me what it offers." Use Conforms()
// after resolution to check capabilities ad-hoc.
//
// InterfaceClient is safe for concurrent use.
type InterfaceClient struct {
	iface       *Interface // nil in discovery mode
	interfaceID string
	executor    *OperationExecutor
	client      *http.Client
	defaultOpts *ExecutionOptions

	contextStore      ContextStore
	platformCallbacks *PlatformCallbacks

	synthesizer InterfaceCreator // combined creator for synthesis, nil if none

	mu          sync.RWMutex
	state       InterfaceClientState
	resolved    *Interface
	resolvedURL string
	issues      []CompatibilityIssue
	synthesized bool
	lastTarget  string
	errMsg      string
	cancelPrev  context.CancelFunc
}

// InterfaceClientOption configures an InterfaceClient.
type InterfaceClientOption func(*InterfaceClient)

// WithHTTPClient sets a custom HTTP client for network requests.
// Useful for configuring timeouts, proxies, or injecting a test transport.
func WithHTTPClient(c *http.Client) InterfaceClientOption {
	return func(ic *InterfaceClient) { ic.client = c }
}

// WithContextStore configures a ContextStore for this client. The store is
// injected into a dedicated executor copy so the original shared executor
// is never mutated.
func WithContextStore(s ContextStore) InterfaceClientOption {
	return func(ic *InterfaceClient) { ic.contextStore = s }
}

// WithPlatformCallbacks configures PlatformCallbacks for this client.
// Callbacks are injected into a dedicated executor copy so the original
// shared executor is never mutated.
func WithPlatformCallbacks(cb *PlatformCallbacks) InterfaceClientOption {
	return func(ic *InterfaceClient) { ic.platformCallbacks = cb }
}

// WithDefaultOptions sets client-level default ExecutionOptions that are
// merged into every execution. Per-call options (via ExecuteWithOptions)
// override these defaults.
func WithDefaultOptions(opts *ExecutionOptions) InterfaceClientOption {
	return func(ic *InterfaceClient) { ic.defaultOpts = opts }
}

// WithInterfaceID sets the role key identifying the required interface
// (e.g., "openbindings.workspace-manager"). When set, enables satisfies-based
// capability matching during resolution and in Conforms().
func WithInterfaceID(id string) InterfaceClientOption {
	return func(ic *InterfaceClient) { ic.interfaceID = id }
}

// NewInterfaceClient creates a new InterfaceClient with the given required
// interface and executor. Pass nil as iface for discovery mode (accepts any
// service unconditionally). The executor must be configured with the
// executors the client should attempt synthesis with.
func NewInterfaceClient(iface *Interface, exec *OperationExecutor, opts ...InterfaceClientOption) *InterfaceClient {
	ic := &InterfaceClient{
		iface:    iface,
		executor: exec,
		client:   &http.Client{},
		state:    StateIdle,
	}
	for _, o := range opts {
		o(ic)
	}
	if ic.contextStore != nil || ic.platformCallbacks != nil {
		ic.executor = ic.executor.WithRuntime(ic.contextStore, ic.platformCallbacks)
		ic.contextStore = nil
		ic.platformCallbacks = nil
	}
	return ic
}

// NewUnboundClient creates an InterfaceClient in discovery mode (nil required
// interface), accepting any resolved interface. Equivalent to
// NewInterfaceClient(nil, exec, opts...).
func NewUnboundClient(exec *OperationExecutor, opts ...InterfaceClientOption) *InterfaceClient {
	return NewInterfaceClient(nil, exec, opts...)
}

// Close cancels any in-flight resolution and releases resources.
// The client transitions to StateIdle and cannot be used for execution
// until Resolve is called again. Close is safe to call multiple times.
func (c *InterfaceClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelPrev != nil {
		c.cancelPrev()
		c.cancelPrev = nil
	}
	c.state = StateIdle
	c.resolved = nil
	c.resolvedURL = ""
	c.issues = nil
	c.synthesized = false
	c.lastTarget = ""
	c.errMsg = ""
}

// Interface returns the required (unbound) interface.
func (c *InterfaceClient) Interface() *Interface { return c.iface }

// State returns the current state.
func (c *InterfaceClient) State() InterfaceClientState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Resolved returns the resolved interface, or nil if not bound.
func (c *InterfaceClient) Resolved() *Interface {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.resolved
}

// ResolvedURL returns the URL the resolved interface was fetched from,
// which may differ from the target if redirects occurred or well-known
// discovery was used. Empty when the interface was synthesized or resolved
// from memory.
func (c *InterfaceClient) ResolvedURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.resolvedURL
}

// Issues returns compatibility issues from the last resolution, or nil.
func (c *InterfaceClient) Issues() []CompatibilityIssue {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.issues
}

// Synthesized returns true if the resolved interface was synthesized from
// a raw spec rather than fetched as a native OBI.
func (c *InterfaceClient) Synthesized() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.synthesized
}

// ErrorMessage returns the error message if state is StateError.
func (c *InterfaceClient) ErrorMessage() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.errMsg
}

// Conforms checks whether the resolved service structurally conforms to the
// given required interface. Returns an empty slice when fully compatible.
//
// This is useful in discovery mode (NewInterfaceClient(nil, ...)) where no
// upfront requirements are enforced — resolve first, then probe for specific
// capabilities ad-hoc.
//
// The optional interfaceID enables satisfies-based matching for this check.
// Returns an error if called before the client has resolved a service.
func (c *InterfaceClient) Conforms(required *Interface, interfaceID ...string) ([]CompatibilityIssue, error) {
	c.mu.RLock()
	resolved := c.resolved
	c.mu.RUnlock()

	if resolved == nil {
		return nil, fmt.Errorf("openbindings: cannot check conformance before resolution")
	}

	var opts []CheckCompatibilityOptions
	if len(interfaceID) > 0 && interfaceID[0] != "" {
		opts = append(opts, CheckCompatibilityOptions{RequiredInterfaceID: interfaceID[0]})
	}
	return CheckInterfaceCompatibility(required, resolved, opts...), nil
}

// Resolve resolves the required interface against a target. The target can be:
//   - A URL to an OBI document (fetches and checks compatibility)
//   - A base URL (discovers via /.well-known/openbindings)
//   - A raw spec URL (synthesizes an OBI via registered creators)
//
// When creators are provided, synthesis is attempted using the combined
// creator when the target is not a native OBI. When no creators are
// provided, synthesis is skipped entirely.
//
// The context controls cancellation and timeouts.
func (c *InterfaceClient) Resolve(ctx context.Context, target string, creators ...InterfaceCreator) error {
	if len(creators) > 0 {
		c.synthesizer = CombineCreators(creators...)
	} else {
		c.synthesizer = nil
	}
	target = strings.TrimSpace(target)
	if target == "" {
		c.mu.Lock()
		if c.cancelPrev != nil {
			c.cancelPrev()
		}
		c.state = StateIdle
		c.resolved = nil
		c.resolvedURL = ""
		c.issues = nil
		c.synthesized = false
		c.lastTarget = ""
		c.errMsg = ""
		c.cancelPrev = nil
		c.mu.Unlock()
		return nil
	}

	c.mu.Lock()
	if c.cancelPrev != nil {
		c.cancelPrev()
	}
	resolveCtx, cancel := context.WithCancel(ctx)
	c.cancelPrev = cancel
	c.state = StateResolving
	c.lastTarget = target
	c.mu.Unlock()

	iface, fetchedURL, native, err := c.resolveFromURL(resolveCtx, target)
	if err != nil {
		c.mu.Lock()
		c.state = StateError
		c.errMsg = err.Error()
		c.resolved = nil
		c.resolvedURL = ""
		c.issues = nil
		c.synthesized = false
		c.mu.Unlock()
		return err
	}

	c.applyResolved(iface, fetchedURL, !native)
	return nil
}

// ResolveInterface resolves directly against an in-memory interface, skipping
// network resolution. Useful when the interface is already available.
func (c *InterfaceClient) ResolveInterface(provided *Interface) {
	c.mu.Lock()
	c.lastTarget = ""
	c.mu.Unlock()
	c.applyResolved(provided, "", false)
}

// Refresh re-resolves against the same target, bypassing any caches.
// For HTTP targets this re-fetches the content and passes it to executors so
// cached parsed documents are replaced with fresh versions.
func (c *InterfaceClient) Refresh(ctx context.Context) error {
	c.mu.RLock()
	target := c.lastTarget
	c.mu.RUnlock()

	if target == "" {
		return nil
	}

	if !IsHTTPURL(target) {
		if c.synthesizer != nil {
			return c.Resolve(ctx, target, c.synthesizer)
		}
		return c.Resolve(ctx, target)
	}

	c.mu.Lock()
	if c.cancelPrev != nil {
		c.cancelPrev()
	}
	resolveCtx, cancel := context.WithCancel(ctx)
	c.cancelPrev = cancel
	c.state = StateResolving
	c.mu.Unlock()

	direct, finalURL, err := c.tryFetchOBI(resolveCtx, target)
	if err == nil && direct != nil {
		c.applyResolved(direct, finalURL, false)
		return nil
	}

	if !shouldSkipWellKnownDiscovery(target) {
		wellKnownURL := strings.TrimRight(target, "/") + WellKnownPath
		wk, wkFinalURL, err := c.tryFetchOBI(resolveCtx, wellKnownURL)
		if err == nil && wk != nil {
			c.applyResolved(wk, wkFinalURL, false)
			return nil
		}
	}

	content, err := c.fetchRawContent(resolveCtx, target)
	if err != nil {
		c.mu.Lock()
		c.state = StateError
		c.errMsg = err.Error()
		c.mu.Unlock()
		return err
	}

	iface, err := c.trySynthesize(resolveCtx, target, content, c.synthesizer)
	if err != nil {
		c.mu.Lock()
		c.state = StateError
		c.errMsg = err.Error()
		c.mu.Unlock()
		return err
	}
	c.applyResolved(iface, "", true)
	return nil
}

// Execute executes an operation against the bound service, returning a stream
// of events. A unary operation produces exactly one event. Client-level default
// options are applied automatically.
func (c *InterfaceClient) Execute(ctx context.Context, op string, input any) (<-chan StreamEvent, error) {
	return c.ExecuteWithOptions(ctx, op, input, nil)
}

// ExecuteWithOptions executes an operation with per-call execution options,
// returning a stream of events. Per-call options are merged on top of
// client-level defaults (per-call wins).
func (c *InterfaceClient) ExecuteWithOptions(ctx context.Context, op string, input any, opts *ExecutionOptions) (<-chan StreamEvent, error) {
	c.mu.RLock()
	state := c.state
	resolved := c.resolved
	c.mu.RUnlock()

	if state != StateBound || resolved == nil {
		return nil, fmt.Errorf("openbindings: client is not bound to a service (state: %s)", state)
	}

	merged := mergeExecutionOptions(c.defaultOpts, opts)
	return c.executor.ExecuteOperation(ctx, &OperationExecutionInput{
		Interface: resolved,
		Operation: op,
		Input:     input,
		Options:   merged,
	})
}

// mergeExecutionOptions merges per-call options on top of defaults.
// Per-call values override defaults. Returns nil when both are nil.
func mergeExecutionOptions(defaults, perCall *ExecutionOptions) *ExecutionOptions {
	if defaults == nil {
		return perCall
	}
	if perCall == nil {
		return defaults
	}
	return &ExecutionOptions{
		Headers:     mergeMaps(defaults.Headers, perCall.Headers),
		Cookies:     mergeMaps(defaults.Cookies, perCall.Cookies),
		Environment: mergeMaps(defaults.Environment, perCall.Environment),
		Metadata:    mergeMaps(defaults.Metadata, perCall.Metadata),
	}
}

func mergeMaps[V any](base, overlay map[string]V) map[string]V {
	if len(overlay) == 0 {
		return base
	}
	if len(base) == 0 {
		return overlay
	}
	result := make(map[string]V, len(base)+len(overlay))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		result[k] = v
	}
	return result
}

// resolveFromURL attempts to resolve an interface from the given URL.
// Returns the interface, the URL it was fetched from, and whether it was
// a native OBI (true) or synthesized (false).
func (c *InterfaceClient) resolveFromURL(ctx context.Context, target string) (*Interface, string, bool, error) {
	if IsHTTPURL(target) {
		direct, finalURL, err := c.tryFetchOBI(ctx, target)
		if err == nil && direct != nil {
			return direct, finalURL, true, nil
		}

		if !shouldSkipWellKnownDiscovery(target) {
			wellKnownURL := strings.TrimRight(target, "/") + WellKnownPath
			wk, wkFinalURL, err := c.tryFetchOBI(ctx, wellKnownURL)
			if err == nil && wk != nil {
				return wk, wkFinalURL, true, nil
			}
		}
	}

	iface, err := c.trySynthesize(ctx, target, nil, c.synthesizer)
	if err != nil {
		return nil, "", false, err
	}
	return iface, "", false, nil
}

// tryFetchOBI attempts to fetch and parse a URL as an OBI document.
// Returns the parsed interface and the final URL after any redirects.
func (c *InterfaceClient) tryFetchOBI(ctx context.Context, target string) (*Interface, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	finalURL := resp.Request.URL.String()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, finalURL, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, finalURL, err
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, finalURL, err
	}

	if !IsOBInterface(raw) {
		// Valid JSON but not an OBI — return nil so the caller falls through
		// to well-known discovery and then synthesis.
		return nil, finalURL, nil
	}

	var iface Interface
	if err := json.Unmarshal(body, &iface); err != nil {
		return nil, finalURL, err
	}
	return &iface, finalURL, nil
}

// fetchRawContent downloads raw bytes from a URL for cache-busting synthesis.
func (c *InterfaceClient) fetchRawContent(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, sanitizeURL(target))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// sanitizeURL strips query parameters to avoid leaking potential
// embedded credentials in error messages.
func sanitizeURL(u string) string {
	if idx := strings.Index(u, "?"); idx >= 0 {
		return u[:idx]
	}
	return u
}

// trySynthesize attempts to create an interface from the given location using
// the provided creator. When content is non-nil, it is passed to the creator
// so cached documents are replaced with the fresh payload.
func (c *InterfaceClient) trySynthesize(ctx context.Context, location string, content []byte, creator InterfaceCreator) (*Interface, error) {
	if creator == nil {
		return nil, fmt.Errorf("no creators available for synthesis")
	}
	var lastErr error
	for _, fi := range creator.Formats() {
		src := CreateSource{Format: fi.Token, Location: location}
		if content != nil {
			src.Content = string(content)
		}
		iface, err := creator.CreateInterface(ctx, &CreateInput{Sources: []CreateSource{src}})
		if err != nil {
			lastErr = err
			continue
		}
		if iface != nil && len(iface.Operations) > 0 {
			return iface, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no creator could synthesize an interface from %s", location)
}

func (c *InterfaceClient) applyResolved(iface *Interface, fetchedURL string, synthesized bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.iface != nil {
		var opts []CheckCompatibilityOptions
		if c.interfaceID != "" {
			opts = append(opts, CheckCompatibilityOptions{RequiredInterfaceID: c.interfaceID})
		}
		issues := CheckInterfaceCompatibility(c.iface, iface, opts...)
		if len(issues) > 0 {
			c.state = StateIncompatible
			c.resolved = nil
			c.resolvedURL = ""
			c.issues = issues
			c.synthesized = false
			c.errMsg = ""
			return
		}
	}

	c.state = StateBound
	c.resolved = iface
	c.resolvedURL = fetchedURL
	c.issues = nil
	c.synthesized = synthesized
	c.errMsg = ""
}

// shouldSkipWellKnownDiscovery returns true if the URL points at a specific
// resource rather than a bare base URL.
func shouldSkipWellKnownDiscovery(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	return strings.HasSuffix(path, ".json") ||
		strings.HasSuffix(path, ".yaml") ||
		strings.HasSuffix(path, ".yml") ||
		strings.Contains(path, "/openapi") ||
		strings.Contains(path, "/swagger") ||
		strings.Contains(path, "/asyncapi") ||
		strings.HasSuffix(path, WellKnownPath)
}
