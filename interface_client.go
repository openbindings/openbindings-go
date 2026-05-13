package openbindings

import (
	"context"
	"encoding/json"
	"fmt"
)

// InterfaceClient dispatches operations against an OBI through an invoker.
//
// Construction is synchronous and side-effect free. The caller is
// responsible for acquiring the OBI (e.g. via FetchInterface(ctx, url),
// loading from disk, or constructing in memory) and validating it
// against any required contract (e.g. via CheckInterfaceCompatibility)
// before passing it here.
//
// InterfaceClient is safe for concurrent use.
type InterfaceClient struct {
	iface       *Interface
	invoker     *OperationInvoker
	defaultOpts *InvocationOptions
}

// InterfaceClientOption configures an InterfaceClient.
type InterfaceClientOption func(*InterfaceClient)

// WithContextStore configures a ContextStore for this client. The store is
// injected into a dedicated invoker copy so the original shared invoker
// is never mutated.
func WithContextStore(s ContextStore) InterfaceClientOption {
	return func(ic *InterfaceClient) {
		ic.invoker = ic.invoker.WithRuntime(s, nil)
	}
}

// WithPlatformCallbacks configures PlatformCallbacks for this client.
// Callbacks are injected into a dedicated invoker copy so the original
// shared invoker is never mutated.
func WithPlatformCallbacks(cb *PlatformCallbacks) InterfaceClientOption {
	return func(ic *InterfaceClient) {
		ic.invoker = ic.invoker.WithRuntime(nil, cb)
	}
}

// WithDefaultOptions sets client-level default InvocationOptions that are
// merged into every invocation. Per-call options (via InvokeWithOptions)
// override these defaults.
func WithDefaultOptions(opts *InvocationOptions) InterfaceClientOption {
	return func(ic *InterfaceClient) { ic.defaultOpts = opts }
}

// NewInterfaceClient creates a new InterfaceClient bound to the given
// OBI and invoker. The OBI must not be nil.
func NewInterfaceClient(iface *Interface, invoker *OperationInvoker, opts ...InterfaceClientOption) *InterfaceClient {
	if iface == nil {
		panic("openbindings: NewInterfaceClient: iface is nil — fetch or load an OBI first")
	}
	ic := &InterfaceClient{
		iface:   iface,
		invoker: invoker,
	}
	for _, o := range opts {
		o(ic)
	}
	return ic
}

// Interface returns the OBI this client was constructed with.
func (c *InterfaceClient) Interface() *Interface { return c.iface }

// InterfaceJSON returns the OBI as pretty-printed JSON.
func (c *InterfaceClient) InterfaceJSON() string {
	return marshalIndentString(c.iface)
}

// Invoke invokes an operation, returning a stream of events. A unary
// operation produces exactly one event. Client-level default options
// are applied automatically.
func (c *InterfaceClient) Invoke(ctx context.Context, op string, input any) (<-chan InvocationOutput, error) {
	return c.InvokeWithOptions(ctx, op, input, nil)
}

// InvokeWithOptions invokes an operation with per-call execution options,
// returning a stream of events. Per-call options are merged on top of
// client-level defaults (per-call wins).
func (c *InterfaceClient) InvokeWithOptions(ctx context.Context, op string, input any, opts *InvocationOptions) (<-chan InvocationOutput, error) {
	merged := mergeInvocationOptions(c.defaultOpts, opts)
	return c.invoker.Invoke(ctx, &OperationInvocationInput{
		Interface: c.iface,
		Operation: op,
		Input:     input,
		Options:   merged,
	})
}

// mergeInvocationOptions merges per-call options on top of defaults.
// Per-call values override defaults. Returns nil when both are nil.
func mergeInvocationOptions(defaults, perCall *InvocationOptions) *InvocationOptions {
	if defaults == nil {
		return perCall
	}
	if perCall == nil {
		return defaults
	}
	return &InvocationOptions{
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

func marshalIndentString(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("/* marshal error: %v */", err)
	}
	return string(b)
}
