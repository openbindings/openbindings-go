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

// InterfaceClient is a stateful object that holds a required interface (unbound
// OBI) and resolves it against a provider. Once resolved, operations can be
// executed through it.
//
// This is the SDK-level primitive for "I need these capabilities — find me
// something compatible and let me use it."
//
// InterfaceClient is safe for concurrent use.
type InterfaceClient struct {
	iface    *Interface
	executor *OperationExecutor
	client   *http.Client

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

// NewInterfaceClient creates a new InterfaceClient with the given required
// interface and executor. The executor must be configured with the providers
// the client should attempt synthesis with.
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
	return ic
}

// NewUnboundClient creates an InterfaceClient with no required operations,
// accepting any resolved interface. Use this when you want to discover or
// synthesize an interface without checking compatibility.
func NewUnboundClient(exec *OperationExecutor, opts ...InterfaceClientOption) *InterfaceClient {
	return NewInterfaceClient(
		&Interface{OpenBindings: "0.1.0", Operations: map[string]Operation{}},
		exec,
		opts...,
	)
}

// Interface returns the required (unbound) interface.
func (c *InterfaceClient) Interface() *Interface { return c.iface }

// State returns the current state.
func (c *InterfaceClient) State() InterfaceClientState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Resolved returns the resolved (provider) interface, or nil if not bound.
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

// Resolve resolves the required interface against a target. The target can be:
//   - A URL to an OBI document (fetches and checks compatibility)
//   - A base URL (discovers via /.well-known/openbindings)
//   - A raw spec URL (synthesizes an OBI via registered providers)
//
// The context controls cancellation and timeouts.
func (c *InterfaceClient) Resolve(ctx context.Context, target string) error {
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
// network resolution. Useful when the provider interface is already available.
func (c *InterfaceClient) ResolveInterface(provided *Interface) {
	c.mu.Lock()
	c.lastTarget = ""
	c.mu.Unlock()
	c.applyResolved(provided, "", false)
}

// Refresh re-resolves against the same target. Useful for detecting when a
// service's interface has changed.
func (c *InterfaceClient) Refresh(ctx context.Context) error {
	c.mu.RLock()
	target := c.lastTarget
	c.mu.RUnlock()

	if target == "" {
		return nil
	}
	return c.Resolve(ctx, target)
}

// Execute executes an operation against the bound service.
func (c *InterfaceClient) Execute(ctx context.Context, op string, input any) (*ExecuteOutput, error) {
	c.mu.RLock()
	state := c.state
	resolved := c.resolved
	c.mu.RUnlock()

	if state != StateBound || resolved == nil {
		return nil, fmt.Errorf("openbindings: client is not bound to a service (state: %s)", state)
	}

	return c.executor.ExecuteOperation(ctx, &OperationExecutionInput{
		Interface: resolved,
		Operation: op,
		Input:     input,
	})
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

	iface, err := c.synthesize(ctx, target)
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

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
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
		return nil, finalURL, nil
	}

	var iface Interface
	if err := json.Unmarshal(body, &iface); err != nil {
		return nil, finalURL, err
	}
	return &iface, finalURL, nil
}

// synthesize attempts to create an interface from the target using all
// registered providers.
func (c *InterfaceClient) synthesize(ctx context.Context, location string) (*Interface, error) {
	formats := c.executor.Formats()
	var lastErr error

	for _, format := range formats {
		iface, err := c.executor.CreateInterface(ctx, &CreateInput{
			Sources: []CreateSource{{Format: format, Location: location}},
		})
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
	return nil, fmt.Errorf("no provider could synthesize an interface from %s", location)
}

func (c *InterfaceClient) applyResolved(iface *Interface, fetchedURL string, synthesized bool) {
	issues := CheckInterfaceCompatibility(c.iface, iface)

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(issues) > 0 {
		c.state = StateIncompatible
		c.resolved = nil
		c.resolvedURL = ""
		c.issues = issues
		c.synthesized = false
		c.errMsg = ""
	} else {
		c.state = StateBound
		c.resolved = iface
		c.resolvedURL = fetchedURL
		c.issues = nil
		c.synthesized = synthesized
		c.errMsg = ""
	}
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
