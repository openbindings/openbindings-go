package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

// This file is the cardinality-agnostic operation invocation handle: one call
// shape for unary, server-streaming, client-streaming, and bidirectional
// bindings. The caller writes messages until done; the invocation yields
// messages until done. The OpenBindings spec assigns cardinality to the
// binding, not the operation, so the call signature never declares it.
//
// Two views of one session:
//   - Invocation[I, O]    — the caller-facing handle
//   - BindingHandle[I, O] — the binding-facing push surface
//
// The same concrete InvocationImpl implements both; the views never overlap
// structurally, so callers can't reach binding-only methods and bindings
// can't reach caller-facing ones.

// Metadata is multi-valued binding-native evidence used inside artifact
// interpretation hooks. It is not exposed by the abstract invocation handle.
type Metadata map[string][]string

// ---------------------------------------------------------------------------
// Context negotiation shapes (the openbindings.binding-invoker interface)
// ---------------------------------------------------------------------------

// ContextRequirement is one runtime prerequisite. Type names a requirement
// family (e.g. "auth.bearer", "auth.apiKey", "auth.basic", "auth.oauth2");
// additional fields are family-specific and round-trip through Extra (the
// binding-invoker contract declares requirement objects open).
type ContextRequirement struct {
	Type string `json:"type"`
	// Name is the scheme name as the source artifact declares it (e.g. an
	// OpenAPI securitySchemes key, or the AsyncAPI components.securitySchemes
	// key a $ref resolves through). Distinguishes two requirements of the
	// same type within one alternative — two ANDed API keys are otherwise
	// indistinguishable — and keys BindingContext's scheme-scoped credentials
	// lookup. Empty when the artifact scheme has no addressable name (e.g.
	// an inline AsyncAPI scheme object).
	Name string `json:"name,omitempty"`
	// Durable reports whether resolved context MAY be persisted and reused.
	// nil means false; only true permits persistence. The
	// contract prescribes no store or key derivation.
	Durable     *bool          `json:"durable,omitempty"`
	Description string         `json:"description,omitempty"`
	Extra       map[string]any `json:"-"`
}

func (r ContextRequirement) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 4+len(r.Extra))
	for k, v := range r.Extra {
		out[k] = v
	}
	out["type"] = r.Type
	if r.Name != "" {
		out["name"] = r.Name
	}
	if r.Durable != nil {
		out["durable"] = *r.Durable
	}
	if r.Description != "" {
		out["description"] = r.Description
	}
	return json.Marshal(out)
}

func (r *ContextRequirement) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*r = ContextRequirement{}
	typeRaw, present := raw["type"]
	if !present || json.Unmarshal(typeRaw, &r.Type) != nil || r.Type == "" {
		return fmt.Errorf("context requirement type must be a nonempty string")
	}
	if nameRaw, present := raw["name"]; present {
		if json.Unmarshal(nameRaw, &r.Name) != nil || r.Name == "" {
			return fmt.Errorf("context requirement name must be a nonempty string")
		}
	}
	if durableRaw, present := raw["durable"]; present {
		var durable bool
		if json.Unmarshal(durableRaw, &durable) != nil {
			return fmt.Errorf("context requirement durable must be a boolean")
		}
		r.Durable = &durable
	}
	if descriptionRaw, present := raw["description"]; present {
		if json.Unmarshal(descriptionRaw, &r.Description) != nil {
			return fmt.Errorf("context requirement description must be a string")
		}
	}
	delete(raw, "type")
	delete(raw, "name")
	delete(raw, "durable")
	delete(raw, "description")
	if len(raw) > 0 {
		r.Extra = make(map[string]any, len(raw))
		for key, valueRaw := range raw {
			var value any
			if err := jsonvalue.Unmarshal(valueRaw, &value); err != nil {
				return fmt.Errorf("context requirement %s: %w", key, err)
			}
			r.Extra[key] = value
		}
	}
	return nil
}

// NewConfigValueRequirement builds a config.value ContextRequirement — the
// binding-invoker family for a configuration value a binding needs
// but the artifact does not supply (a server variable with no default, a
// channel address a service generates at runtime). point names the
// binding-specification configuration point the value belongs to ("server",
// "address", …); path is a JSON Pointer relative to that point (the empty
// pointer addresses the whole point); schema is the engine-asserted JSON
// Schema for the value at (point, path) — artifact-derived where the
// artifact speaks, engine-known where the binding specification pins a
// shape, nil where neither does (absent = unconstrained). An `enum` member
// is a closed admissible set (satisfaction validates against it);
// `examples` are advisory. durable
// defaults to false; pass a *bool of true only when reuse is permitted. The
// /variables/region addresses configuration[point].variables.region, while
// /value addresses a member literally named "value".
func NewConfigValueRequirement(point, path, description string, schema map[string]any, durable *bool) ContextRequirement {
	extra := map[string]any{"point": point, "path": path}
	if schema != nil {
		extra["schema"] = schema
	}
	return ContextRequirement{
		Type:        "config.value",
		Description: description,
		Durable:     durable,
		Extra:       extra,
	}
}

// ContextAlternative is a conjunctive requirement set: ALL requirements must
// be satisfied.
type ContextAlternative struct {
	Requirements []ContextRequirement `json:"requirements"`
}

func (a *ContextAlternative) UnmarshalJSON(raw []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	for key := range envelope {
		if key != "requirements" {
			return fmt.Errorf("invalid context alternative member %q", key)
		}
	}
	requirements, present := envelope["requirements"]
	if !present || json.Unmarshal(requirements, &a.Requirements) != nil {
		return fmt.Errorf("context alternative requirements must be an array")
	}
	return nil
}

// ContextRequiredDetails is the data payload of a CONTEXT_REQUIRED
// terminal error, per the openbindings.binding-invoker interface.
// Alternatives is disjunctive: satisfying any one alternative suffices.
type ContextRequiredDetails struct {
	// Target is an opaque identifier for the concrete destination or context
	// scope. A runtime may use it when resolving or reusing context; key
	// derivation and persistence are outside the contract.
	Target       string               `json:"target"`
	Alternatives []ContextAlternative `json:"alternatives"`
}

func (d *ContextRequiredDetails) UnmarshalJSON(raw []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	for key := range envelope {
		if key != "target" && key != "alternatives" {
			return fmt.Errorf("invalid context-required member %q", key)
		}
	}
	target, targetPresent := envelope["target"]
	alternatives, alternativesPresent := envelope["alternatives"]
	if !targetPresent || json.Unmarshal(target, &d.Target) != nil {
		return fmt.Errorf("context-required target must be a string")
	}
	if !alternativesPresent || json.Unmarshal(alternatives, &d.Alternatives) != nil {
		return fmt.Errorf("context-required alternatives must be an array")
	}
	return nil
}

// NewContextRequiredError constructs the canonical CONTEXT_REQUIRED terminal error.
func NewContextRequiredError(details *ContextRequiredDetails) *InvocationError {
	if !ValidContextRequiredDetails(details) {
		return NewInvocationError(ErrCodeRuntime)
	}
	return NewInvocationErrorWithData(ErrCodeContextRequired, details)
}

// ValidContextRequiredDetails validates the complete portable challenge
// shape before a resolver or frame consumer acts on it. The target is allowed
// to be empty: an unkeyable target cannot select reusable stored context, but
// an interactive resolver may still satisfy it.
func ValidContextRequiredDetails(details *ContextRequiredDetails) bool {
	if details == nil || len(details.Alternatives) == 0 {
		return false
	}
	for _, alternative := range details.Alternatives {
		if len(alternative.Requirements) == 0 {
			return false
		}
		for _, requirement := range alternative.Requirements {
			if requirement.Type == "" {
				return false
			}
			if requirement.Type == "config.value" {
				point, _ := requirement.Extra["point"].(string)
				path, pathPresent := requirement.Extra["path"].(string)
				if point == "" || !pathPresent || !validConfigurationPointer(path) {
					return false
				}
				// A present schema member must be a JSON object (the
				// engine-asserted JSON Schema for the value at point/path).
				// Its content is not metaschema-validated here: whether the
				// schema itself is well-formed is the emitting engine's
				// responsibility; this gate only refuses a carriage shape no
				// consumer could evaluate.
				if schema, present := requirement.Extra["schema"]; present {
					if _, ok := schema.(map[string]any); !ok {
						return false
					}
				}
			}
		}
	}
	return true
}

func validConfigurationPointer(path string) bool {
	if path == "" {
		return true
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, token := range strings.Split(path[1:], "/") {
		for index := 0; index < len(token); index++ {
			if token[index] != '~' {
				continue
			}
			if index+1 >= len(token) || (token[index+1] != '0' && token[index+1] != '1') {
				return false
			}
			index++
		}
	}
	return true
}

// ContextRequiredFrom narrows a terminal error to a CONTEXT_REQUIRED
// challenge, returning its typed data, or nil when err is not one. Data that
// crossed a JSON boundary (the binding-invoker wire protocol) arrive as a
// generic map and are decoded back into the typed shape.
func ContextRequiredFrom(err *InvocationError) *ContextRequiredDetails {
	if err == nil || err.Code != ErrCodeContextRequired {
		return nil
	}
	switch d := err.Data.(type) {
	case *ContextRequiredDetails:
		if ValidContextRequiredDetails(d) {
			return d
		}
		return nil
	case ContextRequiredDetails:
		if ValidContextRequiredDetails(&d) {
			return &d
		}
		return nil
	case map[string]any:
		b, merr := json.Marshal(d)
		if merr != nil {
			return nil
		}
		var details ContextRequiredDetails
		if json.Unmarshal(b, &details) != nil || !ValidContextRequiredDetails(&details) {
			return nil
		}
		return &details
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Caller-facing handle
// ---------------------------------------------------------------------------

// Invocation is a cardinality-agnostic, bidirectional invocation session.
//
// Lifecycle:
//
//	Write(ctx, v)  write one input message to the binding's channel
//	Outputs()      acquire the output sequence (read it to io.EOF / terminal)
//	Close()        graceful close: signal no more input (idempotent)
//	Cancel()       request termination (idempotent; no-op once terminal)
//
// Creation is inert: constructing the handle performs no I/O. Write does NOT
// dispatch the underlying transport; it enqueues a message on the
// caller→binding channel and the binding decides when to dispatch.
//
// Outputs are outputs; errors are errors: OutputStream.Read returns one or
// the other per call, never both. The method ctx on Write/Read bounds
// that blocking call; the invocation's lifetime is governed by the ctx passed
// to Invoke/InvokeBinding (its cancellation converges with Cancel()).
//
// Producers may submit concurrently. Successful, non-overlapping submissions
// from one producer preserve their order when delivered. Relative order across
// producers or overlapping calls is unspecified.
//
// Bidi contract: under bounded backpressure a single goroutine interleaving
// Write and Read deadlocks. Drive input and output from separate goroutines,
// and `defer cancel()` when abandoning the output stream early (Go has no
// `for await`-break hook; OutputStream.Stop is the explicit form).
type Invocation[I, O any] interface {
	// Write submits one input message. Semantics are enqueue, not delivery:
	// nil means the message was accepted into the input stream; the
	// invocation's outcome still arrives at the output terminal. Every error
	// Write returns is truthful — the caller's ctx error, a flow signal
	// (ErrCodeInputClosed once the input side has closed), or, when a
	// terminal has already fired, the terminal error itself, never a weaker
	// substitute. The output side remains the authoritative verdict: a write
	// racing a clean completion can return ErrCodeInvocationClosed even
	// though the invocation succeeded. Treat Write errors as fast-fail, not
	// as the outcome.
	Write(ctx context.Context, input I) error
	// Close signals that no more input is coming (graceful; idempotent; it
	// never fails). Outputs continue until the binding closes its side.
	Close() error
	// Outputs acquires the output sequence. Single-consumer, acquire-once:
	// a second call PANICS with ErrCodeAlreadyConsumed (a second consumer is
	// a programming bug, like a concurrent map write).
	Outputs() OutputStream[O]
	// InputClosed returns a channel that is closed once the invocation's
	// input side has closed — by the caller's Close, by the binding from
	// below (a unary binding after its first read), or by a terminal
	// transition. Consumers that pipe a stream into the invocation (e.g.
	// operation-graph conduits) watch it to learn acceptance has ended
	// without having to probe with a failing Write.
	InputClosed() <-chan struct{}
	Cancel()
}

// OutputStream is the output sequence of an invocation: a single-consumer,
// single-pass, fallible iterator in the sql.Rows / bufio.Scanner shape.
type OutputStream[O any] interface {
	// Read returns the next output, io.EOF on clean close, or the terminal
	// *InvocationError. Queued outputs always drain before a terminal error
	// surfaces (read to terminal to observe the guarantee).
	Read(ctx context.Context) (O, error)
	// Stop abandons consumption early and cancels the invocation — the Go
	// analog of breaking out of a TS `for await`.
	Stop()
}

// ---------------------------------------------------------------------------
// Binding-facing handle
// ---------------------------------------------------------------------------

// BindingHandle is the binding-facing view of an in-flight invocation.
// Bindings drive the invocation by reading inputs, emitting outputs, and
// signalling lifecycle transitions. Callers never see this interface.
//
// Binding-author contract (the type system cannot enforce these):
//  1. Raise CONTEXT_REQUIRED or ERR_REFUSED only before output or observable
//     effects of the requested operation. Setup I/O may already have occurred;
//     other errors carry no safe-to-redo guarantee.
//  2. Observe the EmitOutput result: it returns non-nil when the invocation
//     terminated while the emit was parked; stop emitting on error.
//  3. EmitOutput parking supplies backpressure at this handoff. Avoid a redundant
//     producer queue; any additional buffering or flow control required by the
//     protocol belongs to the binding or protocol implementation.
//  4. Terminate exactly once: CloseOutput() on normal completion or
//     FireError() on terminal failure; never emit after either.
//  5. Close input early when you can (no-input: on entry; unary: after the
//     first read), so the caller never has to Close().
//  6. Bidi: read inputs and emit outputs on separate goroutines; a single
//     interleaved loop deadlocks under bounded backpressure.
type BindingHandle[I, O any] interface {
	// ReadInput returns the next input the caller has written, io.EOF when
	// the input side has closed, or the terminal *InvocationError if the
	// invocation errored. Single-consumer: a second concurrent reader
	// panics (loud, structural — a buggy bidi binding with two read loops
	// fails in tests instead of racing the buffer).
	ReadInput(ctx context.Context) (I, error)

	// CloseInput closes the input side from the binding's perspective.
	// Idempotent. Subsequent caller Write calls reject with
	// ErrCodeInputClosed (non-terminal); outputs still flow.
	CloseInput() error

	// EmitOutput sends one output. Blocks while the bounded output buffer is
	// full (backpressure). Returns the terminal *InvocationError if the
	// invocation terminated while blocked, so a binding ranging over its
	// source stops emitting instead of stranding on a buffer no one drains.
	EmitOutput(output O) error

	// CloseOutput closes the output side normally. Idempotent.
	CloseOutput()

	// FireError closes the invocation with a terminal error. Idempotent.
	FireError(err *InvocationError)

	// Done is closed when the invocation terminates (caller Cancel, upstream
	// ctx cancellation, CloseOutput, or FireError). It is the one teardown
	// channel bindings observe: on Done, abandon underlying work.
	Done() <-chan struct{}
}

// ---------------------------------------------------------------------------
// Reference implementation
// ---------------------------------------------------------------------------

// Private queue capacities provide limited scheduling slack between producers
// and consumers, with blocking backpressure in both directions. They bound
// queued values, not total retention: pending captures, blocked producers,
// pipeline values, construction scratch and terminal data also consume memory.
// No transport buffering or source flow-control behavior is assumed here.
const (
	outputBufferCapacity = 4
	inputBufferCapacity  = 1
)

type invocationState int32

const (
	stateOpen invocationState = iota
	stateClosed
	stateErrored
)

// InvocationImpl is the shared invocation session: it implements the
// caller-facing Invocation[I, O] and the binding-facing BindingHandle[I, O]
// over one pair of bounded channels.
//
// Per the design's terminal model, the data channels are NEVER closed:
// terminal state is signalled by closing `done`, every blocking operation
// selects on `done`, and readers drain buffered outputs before surfacing the
// terminal error. (Closing a data channel on terminal is a real
// send-on-closed-channel panic, not a theoretical one.)
type InvocationImpl[I, O any] struct {
	invocationValueAccess
	limits               value.Limits
	inputDeliveryReaders atomic.Int32
	mu                   sync.Mutex
	state                invocationState
	terminalErr          *InvocationError
	terminalData         *value.Snapshot
	errorCapture         sync.Mutex

	inputCh       chan *valueio.Packet
	inputClosedCh chan struct{}
	inputClosed   bool

	outputCh chan *valueio.Packet

	done chan struct{}

	outputsClaimed bool
	inputReaders   atomic.Int32
	outputReaders  atomic.Int32
	// pendingEmits counts EmitOutput calls between their open-state check and
	// their select resolution. The reader's terminal path waits for in-flight
	// emits to settle before surfacing the terminal, closing the
	// state-machine-vs-channel-select seam where an emit could win the send
	// AFTER a racing terminal and strand an "accepted" value.
	pendingEmits atomic.Int32

	// validateInput is the input-validation hook installed by the operation layer (OBI-T-16 claim semantics):
	// a returned error is terminal AND rejects the offending Write with the
	// same *InvocationError (the binding never sees the rejected input value).
	validateInput func(I) *InvocationError

	stopWatch func() bool // stops the upstream-ctx watcher; nil when none
}

var (
	_ Invocation[any, any]    = (*InvocationImpl[any, any])(nil)
	_ BindingHandle[any, any] = (*InvocationImpl[any, any])(nil)
)

// NewInvocationImpl constructs an inert invocation session. ctx is the
// invocation's lifetime: its cancellation converges with Cancel() on one
// ErrCodeCancelled terminal. No goroutine is spawned and no I/O performed.
func NewInvocationImpl[I, O any](ctx context.Context, opts ...InvocationOption) *InvocationImpl[I, O] {
	var cfg invocationOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	limits, configErr := cfg.limits.resolve()
	if configErr != nil {
		limits = defaultValueLimits()
	}

	i := &InvocationImpl[I, O]{
		limits:        limits,
		inputCh:       make(chan *valueio.Packet, inputBufferCapacity),
		inputClosedCh: make(chan struct{}),
		outputCh:      make(chan *valueio.Packet, outputBufferCapacity),
		done:          make(chan struct{}),
	}
	i.invocationValueAccess = valueio.NewAccess(i, &valueio.Endpoint{
		Limits: limits, CaptureInput: i.captureInput, SendInput: i.writePacket, ReadInput: i.readInputPacket,
		CaptureOutput: i.captureOutput, SendOutput: i.emitPacket, ClaimOutput: i.claimOutput,
		ReadOutput: i.readOutputPacket, Stop: i.Cancel, FailValue: func(err error) { ie := valueError(err, "internal value"); ie.Code = ErrCodeRuntime; i.FireError(ie) },
	})
	if configErr != nil {
		i.FireError(&InvocationError{Code: ErrCodeRuntime})
		return i
	}
	if ctx != nil {
		if ctx.Err() != nil {
			// Every caller-owned lifetime cancellation, including a deadline,
			// uses the invocation interface's one cancellation code.
			i.FireError(&InvocationError{Code: ErrCodeCancelled})
			return i
		}
		stop := context.AfterFunc(ctx, func() {
			i.FireError(&InvocationError{Code: ErrCodeCancelled})
		})
		// The store must hold i.mu: ctx can be cancelled in the window between
		// the Err check above and this registration, and context.AfterFunc then
		// runs the callback immediately on its own goroutine. That callback's
		// FireError reads i.stopWatch under i.mu, so an unlocked write here
		// races it. Storing after the callback has already fired is harmless —
		// the watcher has done its work and stop() is then a no-op.
		i.mu.Lock()
		i.stopWatch = stop
		i.mu.Unlock()
	}
	return i
}

// ----- Caller-facing -----

func (i *InvocationImpl[I, O]) Write(ctx context.Context, input I) error {
	return i.captureInput(ctx, input)
}

func (i *InvocationImpl[I, O]) writePacket(ctx context.Context, input *valueio.Packet) error {
	if err := i.writableErr(); err != nil {
		return err
	}
	if i.validateInput != nil {
		logical, err := input.View(ctx, i.limits, false)
		if err != nil {
			ie := valueError(err, "input validation")
			i.FireError(ie)
			return ie
		}
		typed, ok := logical.(I)
		if !ok {
			typed, err = valueio.Construct[I](ctx, input, i.limits)
			if err != nil {
				ie := valueError(err, "input validation")
				i.FireError(ie)
				return ie
			}
		}
		if verr := i.validateInput(typed); verr != nil {
			// Input-validation dual signal: terminal AND the offending write rejects
			// with the same error. The binding never sees the message.
			i.FireError(verr)
			return verr
		}
		if err := i.writableErr(); err != nil {
			return err
		}
	}
	select {
	case i.inputCh <- input:
		return nil
	case <-i.inputClosedCh:
		// A terminal transition also closes the input side; prefer the
		// terminal error over ERR_INPUT_CLOSED when both raced this select.
		i.mu.Lock()
		state, terr, data := i.state, i.terminalErr, i.terminalData
		i.mu.Unlock()
		switch {
		case state == stateErrored:
			return i.detachTerminal(terr, data)
		case state == stateClosed:
			return classifiedError(ErrCodeInvocationClosed)
		default:
			return classifiedError(ErrCodeInputClosed)
		}
	case <-i.done:
		return i.terminalOrClosedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *InvocationImpl[I, O]) writableErr() error {
	i.mu.Lock()
	state, inputClosed := i.state, i.inputClosed
	terr, data := i.terminalErr, i.terminalData
	i.mu.Unlock()
	if state != stateOpen {
		if terr != nil {
			return i.detachTerminal(terr, data)
		}
		return classifiedError(ErrCodeInvocationClosed)
	}
	if inputClosed {
		return classifiedError(ErrCodeInputClosed)
	}
	return nil
}

func (i *InvocationImpl[I, O]) terminalOrClosedErr() *InvocationError {
	i.mu.Lock()
	terr, data := i.terminalErr, i.terminalData
	i.mu.Unlock()
	if terr != nil {
		return i.detachTerminal(terr, data)
	}
	return classifiedError(ErrCodeInvocationClosed)
}

// Close signals that no more input is coming (graceful; idempotent). The
// invocation continues; outputs flow until the binding closes its output
// side. For abrupt termination use Cancel.
func (i *InvocationImpl[I, O]) Close() error { return i.CloseInput() }

// InputClosed returns a channel closed once the input side has closed (by
// the caller, by the binding from below, or by a terminal transition).
func (i *InvocationImpl[I, O]) InputClosed() <-chan struct{} { return i.inputClosedCh }

// Cancel aborts the invocation. Idempotent; a no-op once terminal (it never
// overwrites a real terminal error with ERR_CANCELLED — load-bearing for
// Single's Stop-after-error path).
func (i *InvocationImpl[I, O]) Cancel() {
	i.FireError(&InvocationError{Code: ErrCodeCancelled})
}

// Outputs acquires the output sequence. The first call claims it; a second
// PANICS with ErrCodeAlreadyConsumed — eager, at the call site, symmetric to
// the TS SDK throwing at the second iterator acquisition. A stray second
// consumer would otherwise silently split outputs.
func (i *InvocationImpl[I, O]) claimOutput() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.outputsClaimed {
		panic(fmt.Sprintf("openbindings: %s: outputs already consumed", ErrCodeAlreadyConsumed))
	}
	i.outputsClaimed = true
}

func (i *InvocationImpl[I, O]) Outputs() OutputStream[O] {
	i.claimOutput()
	return &outputStream[I, O]{impl: i}
}

// ----- Binding-facing -----

func (i *InvocationImpl[I, O]) readInputPacket(ctx context.Context) (*valueio.Packet, error) {
	var zero *valueio.Packet

	if n := i.inputReaders.Add(1); n > 1 {
		i.inputReaders.Add(-1)
		// A second concurrent reader is a binding bug (e.g. a bidi binding
		// spawning two read loops): fail loudly instead of racing the buffer.
		panic(fmt.Sprintf("openbindings: %s: concurrent ReadInput on one invocation", ErrCodeAlreadyConsumed))
	}
	defer i.inputReaders.Add(-1)

	// Terminal failure surfaces immediately (terminal-first on the input
	// side: the binding must stop, not act on more inputs); a normal close
	// still drains buffered inputs below.
	if err := i.erroredErr(); err != nil {
		return zero, err
	}

	select {
	case v := <-i.inputCh:
		return v, nil
	default:
	}
	select {
	case v := <-i.inputCh:
		return v, nil
	case <-i.inputClosedCh:
		// A terminal transition also closes the input side; terminal-first
		// applies here exactly as at entry — the binding must stop, not act
		// on one more buffered input (and must never see a clean EOF).
		if err := i.erroredErr(); err != nil {
			return zero, err
		}
		// NORMAL close after the caller buffered a value: drain it first.
		select {
		case v := <-i.inputCh:
			return v, nil
		default:
		}
		return zero, io.EOF
	case <-i.done:
		if err := i.erroredErr(); err != nil {
			return zero, err
		}
		return zero, io.EOF
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (i *InvocationImpl[I, O]) erroredErr() error {
	i.mu.Lock()
	state, terr, data := i.state, i.terminalErr, i.terminalData
	i.mu.Unlock()
	if state == stateErrored {
		return i.detachTerminal(terr, data)
	}
	return nil
}

// CloseInput closes the input side (binding-side name; the caller-facing
// Close delegates here). Idempotent.
func (i *InvocationImpl[I, O]) CloseInput() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closeInputLocked()
	return nil
}

func (i *InvocationImpl[I, O]) closeInputLocked() {
	if i.inputClosed {
		return
	}
	i.inputClosed = true
	close(i.inputClosedCh)
}

func (i *InvocationImpl[I, O]) EmitOutput(output O) error { return i.captureOutput(output) }

func (i *InvocationImpl[I, O]) emitPacket(output *valueio.Packet) error {
	i.mu.Lock()
	if i.state != stateOpen {
		terr, data := i.terminalErr, i.terminalData
		i.mu.Unlock()
		if terr != nil {
			return i.detachTerminal(terr, data)
		}
		return classifiedError(ErrCodeInvocationClosed)
	}
	// The pending-emit window opens INSIDE the critical section: any emit
	// that passed the open-state check is counted before a racing terminal
	// can acquire the mutex and close `done`, so the reader's terminal path
	// always observes it (the increment is ordered before close(done) by
	// the mutex, and close(done) is the reader's synchronization point).
	i.pendingEmits.Add(1)
	i.mu.Unlock()
	defer i.pendingEmits.Add(-1)

	// The bounded buffered channel IS the backpressure: a full-channel send
	// parks; `done` wakes a parked producer on terminal so it returns the
	// terminal error instead of stranding (decision 13c).
	select {
	case i.outputCh <- output:
		return nil
	case <-i.done:
		return i.terminalOrClosedErr()
	}
}

func (i *InvocationImpl[I, O]) CloseOutput() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != stateOpen {
		return
	}
	i.state = stateClosed
	i.closeInputLocked()
	close(i.done)
	if i.stopWatch != nil {
		i.stopWatch()
	}
}

func (i *InvocationImpl[I, O]) FireError(err *InvocationError) {
	// Code-only cancellation can win while an application codec is running.
	var record *value.Snapshot
	code := ErrCodeRuntime
	var cause error
	if err != nil && err.Code != "" {
		code, cause = err.Code, err.cause
	}
	if err != nil && err.HasData() {
		i.errorCapture.Lock()
		defer i.errorCapture.Unlock()
		i.mu.Lock()
		closed := i.state != stateOpen
		i.mu.Unlock()
		if closed {
			return
		}
		var captureErr error
		record, captureErr = value.Capture(context.Background(), err.Data, value.Options{Limits: i.limits})
		if captureErr != nil || !validInvocationValue(reflect.ValueOf(err.Data), map[visit]bool{}) {
			if captureErr != nil {
				cause = valueError(captureErr, "terminal capture").cause
			}
			record = nil
			code = ErrCodeRuntime
		}
	}
	if code == ErrCodeContextRequired {
		valid := false
		if record != nil {
			data, conversionErr := value.Construct[any](context.Background(), record, value.Options{Limits: i.limits})
			details, ok := contextRequiredData(data)
			valid = conversionErr == nil && ok && ValidContextRequiredDetails(details)
		}
		if !valid {
			record = nil
			code = ErrCodeRuntime
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != stateOpen {
		return
	}
	i.state = stateErrored
	i.terminalErr = &InvocationError{Code: code, cause: cause, dataPresent: record != nil}
	i.terminalData = record
	i.closeInputLocked()
	close(i.done)
	if i.stopWatch != nil {
		i.stopWatch()
	}
}

// detachTerminal builds a caller-owned copy of the terminal error from the
// record read under i.mu. Construction runs outside the lock; public readers
// never share a mutable error. The record is immutable once set, so reading
// the pointers under the lock and constructing afterwards is race-free.
func (i *InvocationImpl[I, O]) detachTerminal(terr *InvocationError, data *value.Snapshot) *InvocationError {
	if terr == nil {
		return nil
	}
	e := *terr
	if data != nil {
		detached, err := value.Construct[any](context.Background(), data, value.Options{Limits: i.limits})
		if err != nil {
			return &InvocationError{Code: ErrCodeRuntime}
		}
		e.Data = detached
	}
	return &e
}

func (i *InvocationImpl[I, O]) Done() <-chan struct{} { return i.done }

// ----- Output stream -----

type outputStream[I, O any] struct {
	impl    *InvocationImpl[I, O]
	readers atomic.Int32
}

func (i *InvocationImpl[I, O]) readOutputPacket(ctx context.Context) (*valueio.Packet, error) {
	s := &outputStream[I, O]{impl: i}
	var zero *valueio.Packet

	if n := s.impl.outputReaders.Add(1); n > 1 {
		s.impl.outputReaders.Add(-1)
		// A second concurrent reader on the single acquired stream is a
		// caller bug (it would silently split outputs): fail loudly,
		// symmetric with the ReadInput guard.
		panic(fmt.Sprintf("openbindings: %s: concurrent OutputStream.Read on one invocation", ErrCodeAlreadyConsumed))
	}
	defer s.impl.outputReaders.Add(-1)

	// Drain-before-terminal: buffered outputs always surface before the
	// terminal error, even when emit and FireError raced in one critical
	// section.
	select {
	case v := <-s.impl.outputCh:
		return v, nil
	default:
	}
	select {
	case v := <-s.impl.outputCh:
		return v, nil
	case <-s.impl.done:
		// Settle the seam between the state machine and the channel select:
		// an emit that passed its open-state check may win the send AFTER
		// done closed (the racing-terminal window). Wait for in-flight emits
		// to resolve, draining anything they land, before surfacing the
		// terminal — preserving drain-before-terminal unconditionally.
		for {
			select {
			case v := <-s.impl.outputCh:
				return v, nil
			default:
			}
			if s.impl.pendingEmits.Load() == 0 {
				// The final emitter publishes to outputCh before decrementing
				// pendingEmits. The first drain above can race just ahead of
				// that send, then observe the decrement and otherwise surface
				// terminal with the accepted value still buffered. Once zero is
				// observed, no new emitter can enter after done closed, so one
				// final drain closes that window.
				select {
				case v := <-s.impl.outputCh:
					return v, nil
				default:
					break
				}
				break
			}
			runtime.Gosched()
		}
		if err := s.impl.erroredErr(); err != nil {
			return zero, err
		}
		return zero, io.EOF
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (s *outputStream[I, O]) Stop() { s.impl.Cancel() }

// ---------------------------------------------------------------------------
// The one blessed terminal: Single
// ---------------------------------------------------------------------------

// Single yields exactly one output. It errors ErrCodeExpectedSingle on zero
// outputs or on a second output — short-circuiting on the second item (no
// whole-stream buffering) and cancelling the invocation via Stop.
//
// Single is a checked assertion ("I expect one; verify it"), never a mode
// ("run it unary"): a short-circuit tears down a live invocation, so use it
// only when confident the selected binding yields one output. It consumes
// the sequence (single-consumer, acquire-once) and returns the payload only.
//
// A terminal error after the first output surfaces as that error, not as a
// false "got more".
func Single[O any](ctx context.Context, out OutputStream[O]) (O, error) {
	var zero O
	first, err := out.Read(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return zero, classifiedError(ErrCodeExpectedSingle)
		}
		// Errors pass through without Stop: a conversion or per-call context
		// error can leave later outputs usable. The caller owns abandonment
		// through out.Stop().
		return zero, err
	}
	if _, err := out.Read(ctx); err == nil {
		out.Stop() // abandon -> cancel: the one Stop Single owns
		return zero, classifiedError(ErrCodeExpectedSingle)
	} else if !errors.Is(err, io.EOF) {
		// A real terminal error after the first output surfaces as-is.
		return zero, err
	}
	return first, nil
}

// ---------------------------------------------------------------------------
// Typed adapter (codegen boundary)
// ---------------------------------------------------------------------------

// TypedInvocation constructs owned inputs and fresh checked typed outputs over
// an ordinary invocation. SDK snapshots preserve native leaves internally.
// A failed output construction consumes that output and returns a zero O with
// ValueConversionError; it does not change the invocation's terminal status.
type TypedInvocation[I, O any] struct {
	invocationValueAccess
	inner  Invocation[any, any]
	limits value.Limits
}

// NewTypedInvocation wraps an untyped invocation with concrete I/O types.
func NewTypedInvocation[I, O any](inner Invocation[any, any], opts ...InvocationOption) *TypedInvocation[I, O] {
	var cfg invocationOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	limits, err := cfg.limits.resolve()
	if err != nil {
		inner = NewErroredInvocation[any, any](&InvocationError{Code: ErrCodeRuntime})
		limits = defaultValueLimits()
	}
	t := &TypedInvocation[I, O]{inner: inner, limits: limits}
	if ep := valueio.From(inner); ep != nil {
		copy := *ep
		t.limits = ep.Limits
		t.invocationValueAccess = valueio.NewAccess(t, &copy)
	}
	return t
}

func (t *TypedInvocation[I, O]) Write(ctx context.Context, input I) error {
	if ep := valueio.From(t); ep != nil {
		return ep.CaptureInput(ctx, input)
	}
	p, err := valueio.Capture(ctx, t.limits, input)
	if err != nil {
		return valueError(err, "input capture")
	}
	raw, err := valueio.Construct[any](ctx, p, t.limits)
	if err != nil {
		return valueError(err, "input delivery")
	}
	return t.inner.Write(ctx, raw)
}

func (t *TypedInvocation[I, O]) Close() error { return t.inner.Close() }
func (t *TypedInvocation[I, O]) Cancel()      { t.inner.Cancel() }

func (t *TypedInvocation[I, O]) InputClosed() <-chan struct{} { return t.inner.InputClosed() }

func (t *TypedInvocation[I, O]) Outputs() OutputStream[O] {
	if ep := valueio.From(t); ep != nil {
		ep.ClaimOutput()
		return &typedOutputStream[O]{endpoint: ep, limits: t.limits}
	}
	return &typedOutputStream[O]{inner: t.inner.Outputs(), limits: t.limits}
}

var _ Invocation[any, any] = (*TypedInvocation[any, any])(nil)

type typedOutputStream[O any] struct {
	streamReadGuard
	inner    OutputStream[any]
	endpoint *valueio.Endpoint
	limits   value.Limits
}

func (s *typedOutputStream[O]) Stop() {
	if s.endpoint != nil {
		s.endpoint.Stop()
	} else {
		s.inner.Stop()
	}
}
func (s *typedOutputStream[O]) Read(ctx context.Context) (O, error) {
	var zero O
	s.begin()
	defer s.end()
	var p *valueio.Packet
	var err error
	if s.endpoint != nil {
		p, err = s.endpoint.ReadOutput(ctx)
	} else {
		var raw any
		raw, err = s.inner.Read(ctx)
		if err == nil {
			p, err = valueio.Capture(ctx, s.limits, raw)
			if err != nil {
				return zero, &ValueConversionError{Stage: "foreign delivery", Cause: valueError(err, "foreign delivery")}
			}
		}
	}
	if err != nil {
		return zero, err
	}
	out, err := valueio.Construct[O](ctx, p, s.limits)
	if err != nil {
		return zero, &ValueConversionError{Stage: "typed delivery", Cause: valueError(err, "typed delivery")}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// NewErroredInvocation returns an invocation that is already terminally
// failed with err. Used for failures knowable before any work starts
// (wiring errors: unknown operation/binding/source, no invoker for format).
func NewErroredInvocation[I, O any](err *InvocationError) *InvocationImpl[I, O] {
	impl := NewInvocationImpl[I, O](context.Background())
	impl.FireError(err)
	return impl
}

// DoneContext derives a context that is cancelled when done closes (or when
// parent is cancelled). Bindings use it to bound their underlying I/O to the
// invocation's lifetime:
//
//	bctx, stop := invoke.DoneContext(ctx, handle.Done())
//	defer stop()
func DoneContext(parent context.Context, done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// AsInvocationError coerces err into an *InvocationError, wrapping foreign
// errors as ErrCodeRuntime. Returns nil for nil.
func AsInvocationError(err error) *InvocationError {
	if err == nil {
		return nil
	}
	var ie *InvocationError
	if errors.As(err, &ie) {
		return ie
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return classifiedError(ErrCodeCancelled)
	}
	if errors.Is(err, context.Canceled) {
		return classifiedError(ErrCodeCancelled)
	}
	return classifiedError(ErrCodeRuntime)
}
