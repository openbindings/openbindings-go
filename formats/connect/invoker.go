// Package connect implements the openbindings.connect@1 binding
// specification for OpenBindings: services speaking the Connect protocol
// with the JSON codec.
//
// The package handles:
//   - Discovering services from embedded .proto source text, an embedded
//     FileDescriptorSet in canonical JSON, or a .proto file on disk
//   - Converting protobuf service descriptors to OpenBindings interfaces
//   - Invoking unary and server-streaming methods (client-streaming and
//     bidirectional methods are refused pre-dispatch as this
//     implementation's declared limitation, per CONN-P-04)
//
// The family has two modes, discriminated structurally by content
// presence (CONN-P-01): schema mode (full fidelity, the shared
// openbindings.grpc@1 schema layer) and descriptorless mode (unary-only BY
// DEFINITION, verbatim JSON values). Three protocol variants are excluded
// from revision 1 (§2): the binary proto codec, gRPC-Web, and the
// protocol's GET dispatch lane — every dispatch under this identifier is a
// POST. See https://connectrpc.com for the incorporated protocol.
package connect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"

	"google.golang.org/protobuf/reflect/protoreflect"

	openbindings "github.com/openbindings/openbindings-go"
)

const BindingSpec = "openbindings.connect@1"
const DefaultSourceName = "connectServer"

// maxRedirects bounds the redirect chain a single request may follow.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context). Like the response
// size cap, this is reference-tool implementation policy, outside
// openbindings.connect@1 (§2: resource policy is consumer and
// implementation policy).
const maxRedirects = 10

// Invoker handles binding invocation for Connect sources.
type Invoker struct {
	client     *http.Client
	fullDuplex bool
}

// NewInvoker creates a new Connect binding invoker with a default HTTP
// client. Use NewInvokerWithClient to inject a custom client (e.g., for
// tests, or to add a transport layer for tracing or auth).
func NewInvoker() *Invoker {
	return NewInvokerWithClient(nil)
}

// NewInvokerWithClient creates an Invoker that uses the supplied
// *http.Client for all outbound requests. A nil client falls back to the
// default. The caller is responsible for configuring redirect policy,
// transport, and any other client-level behavior — consumer-supplied
// channel security (a custom CA, a mutual-TLS identity) is consumer
// configuration per §4, and this is where it plugs in. No overall request
// timeout should be set on the client because the caller controls
// cancellation via context.
func NewInvokerWithClient(client *http.Client) *Invoker {
	if client == nil {
		client = &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		}
	}
	return &Invoker{client: client, fullDuplex: true}
}

// WithFullDuplexTransport declares whether this invoker's selected HTTP
// transport can concurrently stream a request and response. It is useful for
// runtimes restricted to HTTP/1.1; such runtimes refuse client-streaming and
// bidirectional methods before dispatch rather than changing their shape.
func (e *Invoker) WithFullDuplexTransport(enabled bool) *Invoker {
	e.fullDuplex = enabled
	return e
}

// BindingSpecs returns the binding specifications supported by the
// Connect invoker.
func (e *Invoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "Connect (Buf) via HTTP"}}
}

var _ invoke.BindingInvoker = (*Invoker)(nil)

// InvokeBinding invokes a Connect binding and returns the invocation
// handle synchronously; the HTTP work runs on its own goroutine. The
// single request message flows through the handle's Write channel (unary
// and server-streaming methods both take exactly one input).
//
// In schema mode (embedded content, CONN-P-01) the interaction shape is
// the method's declared kind: unary methods yield one output;
// server-streaming methods yield one output per received frame and close
// on the END_STREAM envelope. Client-streaming and bidirectional methods
// are refused pre-dispatch as this implementation's declared limitation
// (CONN-P-04).
//
// In descriptorless mode (no content) the mode is unary-only BY
// DEFINITION (§8): one verbatim JSON input value, one verbatim JSON output
// value, unary framing. A method that is in fact streaming is not
// detected — the server's rejection of the framing is a failure outcome,
// the mode's declared limit, not a guess about the method.
//
// All pre-dispatch failures (bad selector, missing or non-conformant base URL,
// schema load failures, schema-range and kind-coverage refusals, input
// validation) terminate the handle before any network side effect.
func (e *Invoker) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	inv := invoke.NewInvocationImpl[any, any](ctx)
	go e.run(ctx, args, inv)
	return inv
}

func (e *Invoker) run(ctx context.Context, args *invoke.BindingInvocationArgs, inv *invoke.InvocationImpl[any, any]) {
	// Bound all I/O to the invocation's lifetime: caller Cancel(), an
	// abandoned output stream, or upstream ctx cancellation tears down any
	// in-flight HTTP request.
	bctx, stop := invoke.DoneContext(ctx, inv.Done())
	defer stop()

	// ----- Pre-side-effect failures: fire before any input is consumed and
	// before any network I/O, so a no-input-consumed retry is safe. -----

	svcName, methodName, err := parseSelector(args.Selector)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeInvalidSelector})
		return
	}

	// Target configuration point (§9.1): this family's ONE named
	// configuration point, consulted per-invocation configuration →
	// consumer-level configuration (both tiers merged into
	// context.configuration by the operation invoker) → the default, the
	// source's location. A configured target replaces the location
	// entirely, in the same §4 form. Nothing else is configurable.
	cfg := invoke.ContextConfiguration(args.Context)
	target := strings.TrimSpace(args.Source.Location)
	if raw, ok := cfg["target"]; ok && raw != nil {
		s, isStr := raw.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeSourceConfigError,
			})
			return
		}
		target = strings.TrimSpace(s)
	}
	if target == "" {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeSourceConfigError,
		})
		return
	}
	if err := validateBaseURL(target); err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceConfigError})
		return
	}

	// Mode discrimination (CONN-P-01): schema mode with content,
	// descriptorless mode without. In schema mode the embedded content is
	// the artifact the processor interprets (§6, content primacy):
	// resolution against the schema precedes dispatch, byte-exact
	// (CONN-D-03), and a selector matching no method makes the binding
	// unresolvable — offline-checkable, before any network I/O. In
	// descriptorless mode there is nothing to resolve against: the selector
	// segments ride verbatim into the request URL, and an unknown method
	// surfaces as the server's own error — a failure outcome, a stated
	// limit of the mode (§7).
	var mi *methodInfo
	if args.Source.Content != nil {
		disc, parseErr := discoverFromContent(bctx, args.Source.Content)
		if parseErr != nil {
			inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeSourceLoadFailed})
			return
		}
		m, ie := resolveMethod(disc, svcName, methodName)
		if ie != nil {
			inv.FireError(ie)
			return
		}
		if ie := preflightMethod(m, e.fullDuplex); ie != nil {
			inv.FireError(ie)
			return
		}
		mi = &methodInfo{method: m}
	}

	// Credentials ride ordinary HTTP header fields (§9.6, CONN-P-07). An
	// inexpressible credential (an apiKey with no consumer-named header) is
	// surfaced here — pre-dispatch AND before any input is consumed, so a
	// no-input-consumed retry stays safe — never silently skipped.
	headers, hdrErr := buildHTTPHeaders(args.Context)
	if hdrErr != nil {
		if _, ok := hdrErr.(*unplacedCredentialError); ok {
			inv.FireError(invoke.NewContextRequiredError(&invoke.ContextRequiredDetails{
				Target: target,
				Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{{
					Type: "auth.apiKey", Description: "supply the credential through an explicitly named Connect metadata field",
				}}}},
			}))
			return
		}
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeSourceConfigError,
		})
		return
	}

	if mi != nil && mi.method.IsStreamingClient() {
		e.runClientStreaming(bctx, inv, connectURL(target, svcName, methodName), headers, mi, args.DeliveryUnitLimit())
		return
	}

	// ----- Input flows through the handle, not the args. One request
	// message for unary and server-streaming alike. -----
	input, gotInput, ok := readRequestValue(bctx, inv, args, mi)
	if !ok {
		return
	}

	var body []byte
	var ierr *invoke.InvocationError
	if mi != nil {
		body, ierr = buildSchemaModeBody(mi, input)
	} else {
		if !gotInput {
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeValidationFailed,
			})
			return
		}
		body, ierr = buildDescriptorlessBody(input, gotInput)
	}
	if ierr != nil {
		inv.FireError(ierr)
		return
	}

	reqURL := connectURL(target, svcName, methodName)

	if mi != nil && mi.method.IsStreamingServer() {
		e.runStreaming(bctx, inv, reqURL, body, headers, mi, args.DeliveryUnitLimit())
		return
	}
	e.runUnary(bctx, inv, reqURL, body, headers, mi, args.DeliveryUnitLimit())
}

// readRequestValue obtains the single request value from the handle.
//
// No-input operations (Binding set, InputSchema nil — the operation layer
// driving an operation that declares no input) and schema-mode methods
// whose request message has no fields close input on entry and dispatch
// without waiting for a write: a caller of a no-input operation never
// writes nor closes, so reading would park forever. Otherwise the first
// written value is the request; a close-without-write is the ABSENT input
// value — schema mode marshals it as the empty request message (§9.2 via
// GRPC-P-03), descriptorless mode sends {} (§9.3). ok=false means the
// invocation is already terminal.
func readRequestValue(ctx context.Context, inv invoke.BindingHandle[any, any], args *invoke.BindingInvocationArgs, mi *methodInfo) (input any, gotInput bool, ok bool) {
	noInput := args.Binding != nil && args.InputSchema == nil
	if noInput || (mi != nil && mi.method.Input().Fields().Len() == 0) {
		_ = inv.CloseInput()
		return nil, false, true
	}
	v, err := inv.ReadInput(ctx)
	if err == io.EOF {
		return nil, false, true
	}
	if err != nil {
		// Terminal (cancelled) or ctx error; FireError is an idempotent
		// no-op when the invocation already terminated.
		inv.FireError(invoke.AsInvocationError(err))
		return nil, false, false
	}
	_ = inv.CloseInput() // one request message: close after the first read
	return v, true, true
}

// preflightMethod applies the pre-dispatch gates that follow schema-mode
// method resolution: the accepted schema range over the bound method's
// transitive closure (openbindings.grpc@1 §3, via CONN-D-01), then
// interaction-kind coverage (CONN-P-04). Client-streaming and
// bidirectional methods are refused before dispatch as this
// implementation's declared limitation — a coverage declaration, never a
// reinterpretation of the shape.
func preflightMethod(methodDesc protoreflect.MethodDescriptor, fullDuplex bool) *invoke.InvocationError {
	if err := validateBoundClosure(methodDesc); err != nil {
		return &invoke.InvocationError{
			Code: invoke.ErrCodeSourceLoadFailed,
		}
	}
	if methodDesc.IsStreamingClient() && !fullDuplex {
		return &invoke.InvocationError{
			Code: invoke.ErrCodeExecutionFailed,
		}
	}
	return nil
}

// Synthesizer handles interface synthesis from protobuf definitions for the Connect format.
type Synthesizer struct{}

var _ synthesize.InterfaceSynthesizer = (*Synthesizer)(nil)
var _ synthesize.CoverageSynthesizer = (*Synthesizer)(nil)
var _ synthesize.SourceInspector = (*Synthesizer)(nil)

// NewSynthesizer creates a new Connect interface synthesizer.
func NewSynthesizer() *Synthesizer { return &Synthesizer{} }

// BindingSpecs returns the binding specifications supported by the
// Connect synthesizer.
func (c *Synthesizer) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: BindingSpec, Description: "Connect (Buf) via HTTP"}}
}

// SynthesizeInterface parses a .proto file or inline content and converts to an
// OpenBindings interface with Connect bindings.
func (c *Synthesizer) SynthesizeInterface(ctx context.Context, in *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return observation.iface, nil
}

// SynthesizeInterfaceWithCoverage returns the schema-mode OBI and a durable
// disposition for every protobuf method and lossy projection observed by the
// same schema load.
func (c *Synthesizer) SynthesizeInterfaceWithCoverage(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	observation, err := c.synthesizeObserved(ctx, in)
	if err != nil {
		return nil, err
	}
	return synthesize.NewSynthesisResult(
		observation.iface,
		synthesisCoverage(observation.disc, observation.iface, observation.warnings),
		true,
	)
}

type synthesisObservation struct {
	iface    *openbindings.Interface
	disc     *discovery
	warnings []synthesize.SynthesizerWarning
}

func (c *Synthesizer) synthesizeObserved(ctx context.Context, in *synthesize.SynthesizeInput) (*synthesisObservation, error) {
	if len(in.Sources) == 0 {
		skeleton, err := synthesize.SynthesisSkeleton(in)
		if err != nil {
			return nil, err
		}
		return &synthesisObservation{iface: &skeleton}, nil
	}
	if len(in.Sources) > 1 {
		return nil, synthesize.ErrMultipleSources
	}
	src := in.Sources[0]
	if src.BindingSpec != BindingSpec {
		return nil, fmt.Errorf("synthesizer supports exact binding specification %q, got %q", BindingSpec, src.BindingSpec)
	}
	if src.OutputLocation != "" {
		if err := validateBaseURL(src.OutputLocation); err != nil {
			return nil, fmt.Errorf("outputLocation: %w", err)
		}
	}
	if err := validateBaseURL(src.Location); err != nil {
		return nil, fmt.Errorf("location: %w", err)
	}
	if src.Content == nil {
		return nil, fmt.Errorf("descriptorless Connect sources expose no discoverable operation set; synthesis requires embedded protobuf content")
	}

	disc, err := discoverFromProto(ctx, src.Location, src.Content)
	if err != nil {
		return nil, fmt.Errorf("Connect proto parse: %w", err)
	}

	var warnings []synthesize.SynthesizerWarning
	observeWarning := func(warning synthesize.SynthesizerWarning) {
		warnings = append(warnings, warning)
		if in.OnWarning != nil {
			in.OnWarning(warning)
		}
	}
	iface, err := convertToInterface(disc, src.Location, observeWarning)
	if err != nil {
		return nil, fmt.Errorf("Connect convert: %w", err)
	}
	entry := iface.Sources[DefaultSourceName]
	entry.Content = src.Content
	iface.Sources[DefaultSourceName] = entry
	if err := synthesize.FinalizeSynthesis(&iface, in, DefaultSourceName, BindingSpec); err != nil {
		return nil, err
	}
	return &synthesisObservation{iface: &iface, disc: disc, warnings: warnings}, nil
}
