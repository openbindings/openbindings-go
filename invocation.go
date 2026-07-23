package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
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

// Metadata is multi-valued leading/trailing metadata (gRPC metadata, HTTP
// headers/trailers). Empty for formats that carry none.
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
	// indistinguishable — and keys BindingContext's scheme-scoped 'apiKeys'
	// lookup. Empty when the artifact scheme has no addressable name (e.g.
	// an inline AsyncAPI scheme object).
	Name string `json:"name,omitempty"`
	// Durable reports whether resolved context MAY be persisted and reused.
	// nil means true; non-durable context MUST be re-satisfied per call. The
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
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if t, ok := raw["type"].(string); ok {
		r.Type = t
	}
	if n, ok := raw["name"].(string); ok {
		r.Name = n
	}
	if d, ok := raw["durable"].(bool); ok {
		r.Durable = &d
	}
	if d, ok := raw["description"].(string); ok {
		r.Description = d
	}
	delete(raw, "type")
	delete(raw, "name")
	delete(raw, "durable")
	delete(raw, "description")
	if len(raw) > 0 {
		r.Extra = raw
	}
	return nil
}

// NewConfigValueRequirement builds a config.value ContextRequirement — the
// binding-invoker family for a configuration value a binding needs
// but the artifact does not supply (a server variable with no default, a
// channel address a service generates at runtime). point names the
// binding-specification configuration point the value belongs to ("server",
// "address", …); key names the specific value within it (a server-variable
// name, or "address" for a whole channel address); choices carries the
// artifact's declared choices when it enumerates them (the governing binding
// specification decides whether that list is closed or advisory). durable
// defaults to true, permitting but not requiring reuse; pass a *bool of false
// for a value that must be fresh per invocation. The resolved value is carried
// in the "configuration" context field under point; its shape there is the
// invoker's own, so this requirement names what is needed, not the resolved
// structure.
func NewConfigValueRequirement(point, key, description string, choices []string, durable *bool) ContextRequirement {
	extra := map[string]any{"point": point, "key": key}
	if len(choices) > 0 {
		c := make([]any, len(choices))
		for i, v := range choices {
			c[i] = v
		}
		extra["choices"] = c
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

// ContextRequiredDetails is the details payload of a CONTEXT_REQUIRED
// terminal error, per the openbindings.binding-invoker interface.
// Alternatives is disjunctive: satisfying any one alternative suffices.
type ContextRequiredDetails struct {
	// Target is an opaque identifier for the concrete destination or context
	// scope. A runtime may use it when resolving or reusing context; key
	// derivation and persistence are outside the contract.
	Target       string               `json:"target"`
	Alternatives []ContextAlternative `json:"alternatives"`
}

// NewContextRequiredError constructs the canonical CONTEXT_REQUIRED terminal error.
func NewContextRequiredError(message string, details *ContextRequiredDetails) *InvocationError {
	e := &InvocationError{Code: ErrCodeContextRequired, Message: message, Details: details}
	e.classify()
	return e
}

// ContextRequiredFrom narrows a terminal error to a CONTEXT_REQUIRED
// challenge, returning its details, or nil when err is not one. Details that
// crossed a JSON boundary (the binding-invoker wire protocol) arrive as a
// generic map and are decoded back into the typed shape.
func ContextRequiredFrom(err *InvocationError) *ContextRequiredDetails {
	if err == nil || err.Code != ErrCodeContextRequired {
		return nil
	}
	switch d := err.Details.(type) {
	case *ContextRequiredDetails:
		return d
	case ContextRequiredDetails:
		return &d
	case map[string]any:
		b, merr := json.Marshal(d)
		if merr != nil {
			return nil
		}
		var details ContextRequiredDetails
		if json.Unmarshal(b, &details) != nil || details.Target == "" {
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
// the other per call, never both. The method ctx on Write/Read/Header bounds
// that blocking call; the invocation's lifetime is governed by the ctx passed
// to Invoke/InvokeBinding (its cancellation converges with Cancel()).
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
	// Header returns leading metadata; blocks until the binding sets it, or
	// until the first output / terminal, whichever is first. Empty for
	// formats that carry none.
	Header(ctx context.Context) (Metadata, error)
	// Trailer returns trailing metadata. Valid only after the invocation has
	// terminated (Outputs read to io.EOF/terminal); panics if read before.
	Trailer() Metadata
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
//  1. Raise terminal errors (notably CONTEXT_REQUIRED) BEFORE any observable
//     side effect, so a no-input-consumed retry is safe.
//  2. Observe the EmitOutput result: it returns non-nil when the invocation
//     terminated while the emit was parked; stop emitting on error.
//  3. Do not add your own buffer; EmitOutput parking IS backpressure.
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

	// SetHeader sets leading metadata; must precede the first
	// EmitOutput/CloseOutput (errors after the header has settled).
	SetHeader(md Metadata) error

	// SetTrailer sets trailing metadata; must precede CloseOutput/FireError.
	// Late sets (after terminal) are dropped.
	SetTrailer(md Metadata)
}

// ---------------------------------------------------------------------------
// Reference implementation
// ---------------------------------------------------------------------------

// Buffer bounds. A capacity of one is structurally mandatory (the handle is
// returned synchronously and a binding may emit before the caller reads; a
// zero-capacity rendezvous would deadlock). Above one is pipelining slack:
// the output side gets a little decode-ahead; the input side's slack is
// already supplied by the transport's send window. Fixed internal defaults,
// deliberately not configurable — delivery is always lossless, in-order,
// exactly-once, with block-on-full backpressure in both directions.
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
	mu          sync.Mutex
	state       invocationState
	terminalErr *InvocationError

	inputCh       chan I
	inputClosedCh chan struct{}
	inputClosed   bool

	outputCh chan O

	done chan struct{}

	headerMD      Metadata
	headerCh      chan struct{}
	headerSettled bool
	trailerMD     Metadata

	outputsClaimed bool
	inputReaders   atomic.Int32
	outputReaders  atomic.Int32
	// wroteInput records that at least one Write was accepted. Diagnostic
	// only: a cancellation that arrives while the input side was never
	// written NOR closed is almost always the forgot-to-Write hang, and the
	// terminal message says so.
	wroteInput atomic.Bool
	// pendingEmits counts EmitOutput calls between their open-state check and
	// their select resolution. The reader's terminal path waits for in-flight
	// emits to settle before surfacing the terminal, closing the
	// state-machine-vs-channel-select seam where an emit could win the send
	// AFTER a racing terminal and strand an "accepted" value.
	pendingEmits atomic.Int32

	// validateInput is the input-validation hook installed by the operation layer (OBI-T-16 claim semantics):
	// a returned error is terminal AND rejects the offending Write with the
	// same *InvocationError (the binding never sees the message).
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
func NewInvocationImpl[I, O any](ctx context.Context) *InvocationImpl[I, O] {
	i := &InvocationImpl[I, O]{
		inputCh:       make(chan I, inputBufferCapacity),
		inputClosedCh: make(chan struct{}),
		outputCh:      make(chan O, outputBufferCapacity),
		done:          make(chan struct{}),
		headerCh:      make(chan struct{}),
	}
	if ctx != nil {
		if ctx.Err() != nil {
			// An already-expired lifetime is classified the same way a mid-flight
			// one is: a deadline is a TIMEOUT, an explicit cancel is CANCELLED.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				i.FireError(&InvocationError{Code: ErrCodeTimeout, Message: "invocation deadline exceeded"})
			} else {
				i.FireError(&InvocationError{Code: ErrCodeCancelled, Message: "invocation cancelled"})
			}
			return i
		}
		stop := context.AfterFunc(ctx, func() {
			// Distinguish a lifetime deadline from a caller cancel: a deadline
			// is a TIMEOUT (transient, effects: possible — outputs may already
			// have flowed, so the honest retry-safety answer is "may have
			// executed"), while an explicit cancel is genuinely caller-initiated
			// (cancelled). The classification (category + effects) is stamped by
			// FireError → classify().
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				i.FireError(&InvocationError{Code: ErrCodeTimeout, Message: "invocation deadline exceeded"})
				return
			}
			i.FireError(&InvocationError{Code: ErrCodeCancelled, Message: i.cancelMessage()})
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

// cancelMessage diagnoses the most common cancellation: a binding parked
// waiting for input the caller never sent. When the input side was neither
// written nor closed by anyone, the terminal says so instead of leaving a
// bare "cancelled" over what was actually the forgot-to-Write hang.
func (i *InvocationImpl[I, O]) cancelMessage() string {
	i.mu.Lock()
	closed := i.inputClosed
	i.mu.Unlock()
	if !i.wroteInput.Load() && !closed {
		return "invocation cancelled (the input side was never written or closed — a binding waiting for input parks until cancellation; write the operation's input, or Close() for no-input operations)"
	}
	return "invocation cancelled"
}

// ----- Caller-facing -----

func (i *InvocationImpl[I, O]) Write(ctx context.Context, input I) error {
	if err := i.writableErr(); err != nil {
		return err
	}
	if i.validateInput != nil {
		if verr := i.validateInput(input); verr != nil {
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
		i.wroteInput.Store(true)
		return nil
	case <-i.inputClosedCh:
		// A terminal transition also closes the input side; prefer the
		// terminal error over ERR_INPUT_CLOSED when both raced this select.
		i.mu.Lock()
		state, terr := i.state, i.terminalErr
		i.mu.Unlock()
		switch {
		case state == stateErrored:
			return terr
		case state == stateClosed:
			return classifiedError(ErrCodeInvocationClosed, "invocation is closed")
		default:
			return classifiedError(ErrCodeInputClosed, "input is closed")
		}
	case <-i.done:
		return i.terminalOrClosedErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (i *InvocationImpl[I, O]) writableErr() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != stateOpen {
		if i.terminalErr != nil {
			return i.terminalErr
		}
		return classifiedError(ErrCodeInvocationClosed, "invocation is closed")
	}
	if i.inputClosed {
		return classifiedError(ErrCodeInputClosed, "input is closed")
	}
	return nil
}

func (i *InvocationImpl[I, O]) terminalOrClosedErr() *InvocationError {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.terminalErr != nil {
		return i.terminalErr
	}
	return classifiedError(ErrCodeInvocationClosed, "invocation is closed")
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
	i.FireError(&InvocationError{Code: ErrCodeCancelled, Message: "invocation cancelled"})
}

// Outputs acquires the output sequence. The first call claims it; a second
// PANICS with ErrCodeAlreadyConsumed — eager, at the call site, symmetric to
// the TS SDK throwing at the second iterator acquisition. A stray second
// consumer would otherwise silently split outputs.
func (i *InvocationImpl[I, O]) Outputs() OutputStream[O] {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.outputsClaimed {
		panic(fmt.Sprintf("openbindings: %s: outputs already consumed", ErrCodeAlreadyConsumed))
	}
	i.outputsClaimed = true
	return &outputStream[I, O]{impl: i}
}

func (i *InvocationImpl[I, O]) Header(ctx context.Context) (Metadata, error) {
	// Non-blocking first: when the header has settled, return it even if ctx
	// is already cancelled (a settled header is immediately available).
	select {
	case <-i.headerCh:
		return i.headerSnapshot(), nil
	default:
	}
	select {
	case <-i.headerCh:
		return i.headerSnapshot(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (i *InvocationImpl[I, O]) headerSnapshot() Metadata {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.headerMD == nil {
		return Metadata{}
	}
	return copyMetadata(i.headerMD)
}

func (i *InvocationImpl[I, O]) Trailer() Metadata {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state == stateOpen {
		panic("openbindings: Trailer() is valid only after the invocation has terminated")
	}
	if i.trailerMD == nil {
		return Metadata{}
	}
	return copyMetadata(i.trailerMD)
}

// ----- Binding-facing -----

func (i *InvocationImpl[I, O]) ReadInput(ctx context.Context) (I, error) {
	var zero I

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
	defer i.mu.Unlock()
	if i.state == stateErrored {
		return i.terminalErr
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

func (i *InvocationImpl[I, O]) EmitOutput(output O) error {
	i.mu.Lock()
	if i.state != stateOpen {
		err := i.terminalErr
		i.mu.Unlock()
		if err != nil {
			return err
		}
		return classifiedError(ErrCodeInvocationClosed, "invocation is closed")
	}
	// Header settles with the first output if the binding never set it.
	i.settleHeaderLocked()
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
	i.settleHeaderLocked()
	i.closeInputLocked()
	close(i.done)
	if i.stopWatch != nil {
		i.stopWatch()
	}
}

func (i *InvocationImpl[I, O]) FireError(err *InvocationError) {
	if err == nil {
		err = &InvocationError{Code: ErrCodeRuntime, Message: "unknown error"}
	}
	// Stamp the classification at the one terminal chokepoint: every format's
	// FireError'd error (connect/timeout/stream/etc.) gets its Category and
	// transport-code Effects here, so no per-site wiring is needed.
	err.classify()
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != stateOpen {
		return
	}
	i.state = stateErrored
	i.terminalErr = err
	i.settleHeaderLocked()
	i.closeInputLocked()
	close(i.done)
	if i.stopWatch != nil {
		i.stopWatch()
	}
}

func (i *InvocationImpl[I, O]) Done() <-chan struct{} { return i.done }

func (i *InvocationImpl[I, O]) SetHeader(md Metadata) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.headerSettled {
		return fmt.Errorf("openbindings: SetHeader must precede the first EmitOutput/CloseOutput")
	}
	i.headerMD = copyMetadata(md)
	i.settleHeaderLocked()
	return nil
}

// copyMetadata clones md so binding-held maps and caller-held returns never
// alias unsynchronized shared state.
func copyMetadata(md Metadata) Metadata {
	if md == nil {
		return nil
	}
	cp := make(Metadata, len(md))
	for k, vs := range md {
		cp[k] = append([]string(nil), vs...)
	}
	return cp
}

func (i *InvocationImpl[I, O]) SetTrailer(md Metadata) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != stateOpen {
		// Terminal state is reachable at any time via caller/upstream
		// cancellation, so a binding may legally set trailers on a goroutine
		// after the RPC has already settled. Per the documented contract
		// ("Late sets (after terminal) are dropped"), this is a no-op rather
		// than a panic — panicking here would crash the process on a benign
		// cancellation race.
		return
	}
	i.trailerMD = copyMetadata(md)
}

func (i *InvocationImpl[I, O]) settleHeaderLocked() {
	if i.headerSettled {
		return
	}
	i.headerSettled = true
	close(i.headerCh)
}

// ----- Output stream -----

type outputStream[I, O any] struct {
	impl *InvocationImpl[I, O]
}

func (s *outputStream[I, O]) Read(ctx context.Context) (O, error) {
	var zero O

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
// the sequence (single-consumer, acquire-once) and returns the payload only;
// metadata is read from the handle you still hold.
//
// A terminal error after the first output surfaces as that error, not as a
// false "got more".
func Single[O any](ctx context.Context, out OutputStream[O]) (O, error) {
	var zero O
	first, err := out.Read(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return zero, classifiedError(ErrCodeExpectedSingle, "expected one output, got none")
		}
		// Errors pass through without Stop: a terminal already ended the
		// invocation (Stop would be a no-op), and a per-call ctx error
		// leaves the live invocation to the caller's discretion.
		return zero, err
	}
	if _, err := out.Read(ctx); err == nil {
		out.Stop() // abandon -> cancel: the one Stop Single owns
		return zero, classifiedError(ErrCodeExpectedSingle, "expected one output, got more")
	} else if !errors.Is(err, io.EOF) {
		// A real terminal error after the first output surfaces as-is.
		return zero, err
	}
	return first, nil
}

// ---------------------------------------------------------------------------
// Typed adapter (codegen boundary)
// ---------------------------------------------------------------------------

// TypedInvocation wraps an untyped Invocation[any, any] with concrete input
// and output types. Codegen emits one TypedInvocation-returning method per
// OBI operation. Each raw output is decoded into O at the typed boundary: a
// direct type assertion first (zero-cost when the binding emitted a concrete
// O), then a JSON round-trip (the schema-validated outputs of the operation
// layer are generic maps). On a decode failure Read returns a zero O and a
// terminal ERR_TYPE_MISMATCH; it never panics — so the `O` a typed invoker
// hands out is a guarantee backed by output validation (OBI-T-16) plus a checked decode, not an IOU.
type TypedInvocation[I, O any] struct {
	inner Invocation[any, any]
}

// NewTypedInvocation wraps an untyped invocation with concrete I/O types.
func NewTypedInvocation[I, O any](inner Invocation[any, any]) *TypedInvocation[I, O] {
	return &TypedInvocation[I, O]{inner: inner}
}

func (t *TypedInvocation[I, O]) Write(ctx context.Context, input I) error {
	// Encode at the typed boundary, symmetric with the output decode:
	// Input validation and format invokers operate on generic JSON values
	// (maps/slices/primitives), not Go structs. For the untyped flavor (I = any)
	// this normalizes the value through JSON (e.g. int -> float64), so a caller
	// driving an [any, any] handle must hand it generic-JSON-shaped input; a
	// non-JSON-encodable value is rejected here rather than reaching the binding.
	b, err := json.Marshal(input)
	if err != nil {
		return classifiedError(ErrCodeTypeMismatch,
			fmt.Sprintf("openbindings: input %T is not JSON-encodable: %v", input, err))
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return classifiedError(ErrCodeTypeMismatch,
			fmt.Sprintf("openbindings: input %T round-trip failed: %v", input, err))
	}
	return t.inner.Write(ctx, generic)
}

func (t *TypedInvocation[I, O]) Close() error { return t.inner.Close() }
func (t *TypedInvocation[I, O]) Cancel()      { t.inner.Cancel() }

func (t *TypedInvocation[I, O]) InputClosed() <-chan struct{} { return t.inner.InputClosed() }

func (t *TypedInvocation[I, O]) Header(ctx context.Context) (Metadata, error) {
	return t.inner.Header(ctx)
}

func (t *TypedInvocation[I, O]) Trailer() Metadata { return t.inner.Trailer() }

func (t *TypedInvocation[I, O]) Outputs() OutputStream[O] {
	return &typedOutputStream[O]{inner: t.inner.Outputs()}
}

var _ Invocation[any, any] = (*TypedInvocation[any, any])(nil)

type typedOutputStream[O any] struct {
	inner OutputStream[any]
}

func (s *typedOutputStream[O]) Stop() { s.inner.Stop() }

func (s *typedOutputStream[O]) Read(ctx context.Context) (O, error) {
	var zero O
	raw, err := s.inner.Read(ctx)
	if err != nil {
		return zero, err
	}
	if typed, ok := raw.(O); ok {
		return typed, nil
	}
	// Decode at the typed boundary: the operation layer emits generic JSON
	// values (maps/slices); round-trip them into the concrete O.
	b, merr := json.Marshal(raw)
	if merr == nil {
		var typed O
		if uerr := json.Unmarshal(b, &typed); uerr == nil {
			return typed, nil
		}
	}
	return zero, classifiedError(ErrCodeTypeMismatch,
		fmt.Sprintf("openbindings: expected %T, got %T", zero, raw))
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
//	bctx, stop := openbindings.DoneContext(ctx, handle.Done())
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
		// A deadline is a TIMEOUT (transient / effects: possible), not a
		// caller-initiated cancel — outputs may already have flowed.
		return classifiedError(ErrCodeTimeout, err.Error())
	}
	if errors.Is(err, context.Canceled) {
		return classifiedError(ErrCodeCancelled, err.Error())
	}
	return classifiedError(ErrCodeRuntime, err.Error())
}
