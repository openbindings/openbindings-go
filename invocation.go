package openbindings

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
// Context negotiation shapes (openbindings.binding-invoker role)
// ---------------------------------------------------------------------------

// ContextRequirement is one runtime prerequisite. Type names a requirement
// family (e.g. "auth.bearer", "auth.apiKey", "auth.basic", "auth.oauth2");
// additional fields are family-specific.
type ContextRequirement struct {
	Type string `json:"type"`
	// Durable reports whether resolved context MAY be cached under the
	// challenge's Key. nil means true; non-durable context MUST be
	// re-satisfied per call.
	Durable     *bool          `json:"durable,omitempty"`
	Description string         `json:"description,omitempty"`
	Extra       map[string]any `json:"-"`
}

// ContextAlternative is a conjunctive requirement set: ALL requirements must
// be satisfied.
type ContextAlternative struct {
	Requirements []ContextRequirement `json:"requirements"`
}

// ContextRequiredDetails is the details payload of a CONTEXT_REQUIRED
// terminal error, per the openbindings.binding-invoker role. Alternatives is
// disjunctive: satisfying any one alternative suffices.
type ContextRequiredDetails struct {
	// Key is the stable context identity (typically a normalized target
	// origin). Store-backed resolvers use it as a cache key; others ignore it.
	Key          string               `json:"key"`
	Alternatives []ContextAlternative `json:"alternatives"`
}

// NewContextRequiredError constructs the canonical CONTEXT_REQUIRED terminal error.
func NewContextRequiredError(message string, details *ContextRequiredDetails) *InvocationError {
	return &InvocationError{Code: ErrCodeContextRequired, Message: message, Details: details}
}

// ContextRequiredFrom narrows a terminal error to a CONTEXT_REQUIRED
// challenge, returning its details, or nil when err is not one.
func ContextRequiredFrom(err *InvocationError) *ContextRequiredDetails {
	if err == nil || err.Code != ErrCodeContextRequired {
		return nil
	}
	d, _ := err.Details.(*ContextRequiredDetails)
	return d
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
	Write(ctx context.Context, input I) error
	Close() error
	// Outputs acquires the output sequence. Single-consumer, acquire-once:
	// a second call PANICS with ErrCodeAlreadyConsumed (a second consumer is
	// a programming bug, like a concurrent map write).
	Outputs() OutputStream[O]
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

	// validateInput is the OBI-T-07 hook installed by the operation layer:
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
			i.FireError(&InvocationError{Code: ErrCodeCancelled, Message: "invocation cancelled"})
			return i
		}
		i.stopWatch = context.AfterFunc(ctx, func() {
			i.FireError(&InvocationError{Code: ErrCodeCancelled, Message: "invocation cancelled"})
		})
	}
	return i
}

// ----- Caller-facing -----

func (i *InvocationImpl[I, O]) Write(ctx context.Context, input I) error {
	if err := i.writableErr(); err != nil {
		return err
	}
	if i.validateInput != nil {
		if verr := i.validateInput(input); verr != nil {
			// OBI-T-07 dual signal: terminal AND the offending write rejects
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
		state, terr := i.state, i.terminalErr
		i.mu.Unlock()
		switch {
		case state == stateErrored:
			return terr
		case state == stateClosed:
			return &InvocationError{Code: ErrCodeInvocationClosed, Message: "invocation is closed"}
		default:
			return &InvocationError{Code: ErrCodeInputClosed, Message: "input is closed"}
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
		return &InvocationError{Code: ErrCodeInvocationClosed, Message: "invocation is closed"}
	}
	if i.inputClosed {
		return &InvocationError{Code: ErrCodeInputClosed, Message: "input is closed"}
	}
	return nil
}

func (i *InvocationImpl[I, O]) terminalOrClosedErr() *InvocationError {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.terminalErr != nil {
		return i.terminalErr
	}
	return &InvocationError{Code: ErrCodeInvocationClosed, Message: "invocation is closed"}
}

// Close signals that no more input is coming (graceful; idempotent). The
// invocation continues; outputs flow until the binding closes its output
// side. For abrupt termination use Cancel.
func (i *InvocationImpl[I, O]) Close() error { return i.CloseInput() }

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
	return i.headerMD
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
	return i.trailerMD
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
		// Input closed after the caller buffered a value: drain it first.
		select {
		case v := <-i.inputCh:
			return v, nil
		default:
		}
		// A terminal transition also closes the input side; the binding
		// must see the terminal error, not a clean EOF.
		if err := i.erroredErr(); err != nil {
			return zero, err
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
		return &InvocationError{Code: ErrCodeInvocationClosed, Message: "invocation is closed"}
	}
	// Header settles with the first output if the binding never set it.
	i.settleHeaderLocked()
	i.mu.Unlock()

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
	i.headerMD = md
	i.settleHeaderLocked()
	return nil
}

func (i *InvocationImpl[I, O]) SetTrailer(md Metadata) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.state != stateOpen {
		// Must precede CloseOutput/FireError; late sets are dropped.
		return
	}
	i.trailerMD = md
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
		// A value may have landed between the drain attempt and done firing.
		select {
		case v := <-s.impl.outputCh:
			return v, nil
		default:
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
		if err == io.EOF {
			return zero, &InvocationError{Code: ErrCodeExpectedSingle, Message: "expected one output, got none"}
		}
		return zero, err
	}
	if _, err := out.Read(ctx); err == nil {
		out.Stop() // abandon -> cancel
		return zero, &InvocationError{Code: ErrCodeExpectedSingle, Message: "expected one output, got more"}
	} else if err != io.EOF {
		// A real terminal error after the first output surfaces as-is.
		// (Stop after a real terminal is a no-op; it cannot overwrite it.)
		out.Stop()
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
// O), then a JSON round-trip (the OBI-T-08-validated outputs of the operation
// layer are generic maps). On a decode failure Read returns a zero O and a
// terminal ERR_TYPE_MISMATCH; it never panics — so the `O` a typed invoker
// hands out is a guarantee backed by T-08 plus a checked decode, not an IOU.
type TypedInvocation[I, O any] struct {
	inner Invocation[any, any]
}

// NewTypedInvocation wraps an untyped invocation with concrete I/O types.
func NewTypedInvocation[I, O any](inner Invocation[any, any]) *TypedInvocation[I, O] {
	return &TypedInvocation[I, O]{inner: inner}
}

func (t *TypedInvocation[I, O]) Write(ctx context.Context, input I) error {
	// Encode at the typed boundary, symmetric with the output decode:
	// OBI-T-07 validation and format invokers operate on generic JSON values
	// (maps/slices/primitives), not Go structs.
	b, err := json.Marshal(input)
	if err != nil {
		return &InvocationError{
			Code:    "ERR_TYPE_MISMATCH",
			Message: fmt.Sprintf("openbindings: input %T is not JSON-encodable: %v", input, err),
		}
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return &InvocationError{
			Code:    "ERR_TYPE_MISMATCH",
			Message: fmt.Sprintf("openbindings: input %T round-trip failed: %v", input, err),
		}
	}
	return t.inner.Write(ctx, generic)
}

func (t *TypedInvocation[I, O]) Close() error { return t.inner.Close() }
func (t *TypedInvocation[I, O]) Cancel()      { t.inner.Cancel() }

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
	return zero, &InvocationError{
		Code:    "ERR_TYPE_MISMATCH",
		Message: fmt.Sprintf("openbindings: expected %T, got %T", zero, raw),
	}
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
	if ie, ok := err.(*InvocationError); ok { //nolint:errorlint // exact type carries the code
		return ie
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return &InvocationError{Code: ErrCodeCancelled, Message: err.Error()}
	}
	return &InvocationError{Code: ErrCodeRuntime, Message: err.Error()}
}
