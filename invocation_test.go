package openbindings

// Phase-6 acceptance: the conformance test matrix for the reference
// InvocationImpl and the Single terminal (operation-invoker-design.md),
// mirroring the TS SDK's invocation.test.ts. Run with -race.
// Cardinality tags: U unary, SS server-streaming, CS client-streaming,
// BD bidi, NI no-input.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func bg() context.Context { return context.Background() }

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var ie *InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %T: %v", err, err)
	}
	return ie.Code
}

func collectStream[O any](t *testing.T, out OutputStream[O]) ([]O, error) {
	t.Helper()
	var vals []O
	for {
		v, err := out.Read(shortCtx(t))
		if err == io.EOF {
			return vals, nil
		}
		if err != nil {
			return vals, err
		}
		vals = append(vals, v)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle & ordering
// ---------------------------------------------------------------------------

func TestDrainBeforeTerminal(t *testing.T) { // SS
	inv := NewInvocationImpl[any, int](bg())
	if err := inv.EmitOutput(1); err != nil {
		t.Fatal(err)
	}
	if err := inv.EmitOutput(2); err != nil {
		t.Fatal(err)
	}
	inv.FireError(&InvocationError{Code: ErrCodeStreamError, Message: "boom"})

	vals, err := collectStream(t, inv.Outputs())
	if len(vals) != 2 || vals[0] != 1 || vals[1] != 2 {
		t.Fatalf("expected [1 2] before terminal, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeStreamError {
		t.Fatalf("expected ERR_STREAM_ERROR after drain, got %v", err)
	}
}

func TestQueuedBeforeAcquire(t *testing.T) { // U, NI
	inv := NewInvocationImpl[any, string](bg())
	if err := inv.EmitOutput("early"); err != nil {
		t.Fatal(err)
	}
	inv.CloseOutput()

	vals, err := collectStream(t, inv.Outputs())
	if err != nil || len(vals) != 1 || vals[0] != "early" {
		t.Fatalf("expected [early], got %v err=%v", vals, err)
	}
}

func TestTerminalStickyAndSingle(t *testing.T) {
	inv := NewInvocationImpl[any, any](bg())
	inv.CloseOutput()
	inv.FireError(&InvocationError{Code: ErrCodeRuntime, Message: "late"}) // no-op
	inv.Cancel()                                                           // no-op
	if _, err := collectStream(t, inv.Outputs()); err != nil {
		t.Fatalf("normal close must win: %v", err)
	}

	inv2 := NewInvocationImpl[any, any](bg())
	inv2.FireError(&InvocationError{Code: ErrCodeRuntime, Message: "first"})
	inv2.FireError(&InvocationError{Code: ErrCodeRuntime, Message: "second"}) // no-op
	inv2.CloseOutput()                                                        // no-op
	_, err := collectStream(t, inv2.Outputs())
	if err == nil || err.Error() != "first" {
		t.Fatalf("first terminal must stick, got %v", err)
	}
}

func TestCancelDoesNotOverwriteRealTerminal(t *testing.T) {
	inv := NewInvocationImpl[any, any](bg())
	real := &InvocationError{Code: ErrCodeExecutionFailed, Message: "real failure"}
	inv.FireError(real)
	inv.Cancel()
	_, err := collectStream(t, inv.Outputs())
	if codeOf(t, err) != ErrCodeExecutionFailed {
		t.Fatalf("cancel overwrote real terminal: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Single-consumer (acquire-once)
// ---------------------------------------------------------------------------

func TestSecondOutputsAcquisitionPanics(t *testing.T) {
	inv := NewInvocationImpl[any, int](bg())
	_ = inv.Outputs()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on second Outputs()")
		}
	}()
	_ = inv.Outputs()
}

func TestConcurrentInputReadersPanic(t *testing.T) { // BD
	inv := NewInvocationImpl[int, any](bg())
	ready := make(chan struct{})
	go func() {
		close(ready)
		_, _ = inv.ReadInput(bg()) // parks (no input)
	}()
	<-ready
	time.Sleep(20 * time.Millisecond) // let the first reader park
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on concurrent ReadInput")
		}
		_ = inv.Close() // unpark the goroutine reader
	}()
	_, _ = inv.ReadInput(bg())
}

// ---------------------------------------------------------------------------
// Single
// ---------------------------------------------------------------------------

func TestSingleReturnsTheOneOutput(t *testing.T) { // U
	inv := NewInvocationImpl[any, string](bg())
	_ = inv.EmitOutput("only")
	inv.CloseOutput()
	v, err := Single(shortCtx(t), inv.Outputs())
	if err != nil || v != "only" {
		t.Fatalf("got %q err=%v", v, err)
	}
}

func TestSingleErrorsOnZero(t *testing.T) {
	inv := NewInvocationImpl[any, string](bg())
	inv.CloseOutput()
	_, err := Single(shortCtx(t), inv.Outputs())
	if codeOf(t, err) != ErrCodeExpectedSingle {
		t.Fatalf("expected ERR_EXPECTED_SINGLE, got %v", err)
	}
}

func TestSingleShortCircuitsOnSecond(t *testing.T) { // SS — the critical misuse case
	inv := NewInvocationImpl[any, int](bg())
	_ = inv.EmitOutput(1)
	_ = inv.EmitOutput(2)
	_, err := Single(shortCtx(t), inv.Outputs())
	if codeOf(t, err) != ErrCodeExpectedSingle {
		t.Fatalf("expected ERR_EXPECTED_SINGLE, got %v", err)
	}
	// Abandoning cancelled the invocation: the binding's Done fired and the
	// terminal is ERR_CANCELLED.
	select {
	case <-inv.Done():
	default:
		t.Fatal("short-circuit must cancel the invocation")
	}
	if err := inv.EmitOutput(3); codeOf(t, err) != ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED for the binding, got %v", err)
	}
}

func TestSingleSurfacesTerminalAfterFirst(t *testing.T) { // SS
	inv := NewInvocationImpl[any, int](bg())
	_ = inv.EmitOutput(1)
	inv.FireError(&InvocationError{Code: ErrCodeStreamError, Message: "mid-stream death"})
	_, err := Single(shortCtx(t), inv.Outputs())
	if codeOf(t, err) != ErrCodeStreamError {
		t.Fatalf("expected the real terminal, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Backpressure (bounded-block)
// ---------------------------------------------------------------------------

func TestEmitParksAtCapacityAndWakesOnDrain(t *testing.T) { // SS
	inv := NewInvocationImpl[any, int](bg())
	for i := 0; i < outputBufferCapacity; i++ {
		if err := inv.EmitOutput(i); err != nil {
			t.Fatal(err)
		}
	}
	parked := make(chan error, 1)
	go func() { parked <- inv.EmitOutput(99) }()
	select {
	case err := <-parked:
		t.Fatalf("emit must park at capacity, returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	out := inv.Outputs()
	v, err := out.Read(shortCtx(t))
	if err != nil || v != 0 {
		t.Fatalf("expected 0, got %v err=%v", v, err)
	}
	select {
	case err := <-parked:
		if err != nil {
			t.Fatalf("parked emit must succeed after drain: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parked emit not admitted after drain")
	}
	inv.CloseOutput()
	rest, err := collectStream(t, out)
	if err != nil {
		t.Fatal(err)
	}
	// Exact order: remaining buffered values then the parked value.
	expected := make([]int, 0, outputBufferCapacity)
	for i := 1; i < outputBufferCapacity; i++ {
		expected = append(expected, i)
	}
	expected = append(expected, 99)
	if len(rest) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, rest)
	}
	for i := range expected {
		if rest[i] != expected[i] {
			t.Fatalf("order: expected %v, got %v", expected, rest)
		}
	}
}

func TestParkedEmitRejectsOnTerminal(t *testing.T) { // SS, BD
	inv := NewInvocationImpl[any, int](bg())
	for i := 0; i < outputBufferCapacity; i++ {
		_ = inv.EmitOutput(i)
	}
	parked := make(chan error, 1)
	go func() { parked <- inv.EmitOutput(99) }()
	time.Sleep(20 * time.Millisecond)
	inv.FireError(&InvocationError{Code: ErrCodeStreamError, Message: "died while parked"})
	select {
	case err := <-parked:
		if codeOf(t, err) != ErrCodeStreamError {
			t.Fatalf("parked emit must reject with the terminal, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parked emit stranded after terminal")
	}
}

func TestWriteParksAtInputCapacityAndWakesOnRead(t *testing.T) { // CS
	inv := NewInvocationImpl[int, any](bg())
	for i := 0; i < inputBufferCapacity; i++ {
		if err := inv.Write(bg(), i); err != nil {
			t.Fatal(err)
		}
	}
	parked := make(chan error, 1)
	go func() { parked <- inv.Write(bg(), 99) }()
	select {
	case err := <-parked:
		t.Fatalf("write must park at capacity, returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if v, err := inv.ReadInput(shortCtx(t)); err != nil || v != 0 {
		t.Fatalf("expected 0, got %v err=%v", v, err)
	}
	select {
	case err := <-parked:
		if err != nil {
			t.Fatalf("parked write must succeed after read: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parked write not admitted after read")
	}
}

func TestParkedWriteRejectsOnTerminal(t *testing.T) { // CS
	inv := NewInvocationImpl[int, any](bg())
	for i := 0; i < inputBufferCapacity; i++ {
		_ = inv.Write(bg(), i)
	}
	parked := make(chan error, 1)
	go func() { parked <- inv.Write(bg(), 99) }()
	time.Sleep(20 * time.Millisecond)
	inv.FireError(&InvocationError{Code: ErrCodeRuntime, Message: "terminal while write parked"})
	select {
	case err := <-parked:
		if codeOf(t, err) != ErrCodeRuntime {
			t.Fatalf("parked write must reject with the terminal, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parked write stranded after terminal")
	}
}

func TestSlowConsumerFlowControlsBinding(t *testing.T) { // SS end-to-end
	inv := NewInvocationImpl[any, int](bg())
	total := outputBufferCapacity + 5
	var emitted int
	var mu sync.Mutex
	go func() {
		for i := 0; i < total; i++ {
			if err := inv.EmitOutput(i); err != nil {
				return
			}
			mu.Lock()
			emitted++
			mu.Unlock()
		}
		inv.CloseOutput()
	}()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	ahead := emitted
	mu.Unlock()
	if ahead > outputBufferCapacity {
		t.Fatalf("binding ran %d ahead of a %d-slot buffer", ahead, outputBufferCapacity)
	}
	vals, err := collectStream(t, inv.Outputs())
	if err != nil || len(vals) != total {
		t.Fatalf("expected %d values, got %d err=%v", total, len(vals), err)
	}
}

// ---------------------------------------------------------------------------
// Input side
// ---------------------------------------------------------------------------

func TestCallerCloseEndsBindingReadCleanly(t *testing.T) { // CS
	inv := NewInvocationImpl[string, any](bg())
	if err := inv.Write(bg(), "a"); err != nil {
		t.Fatal(err)
	}
	var seen []string
	done := make(chan error, 1)
	go func() {
		for {
			v, err := inv.ReadInput(bg())
			if err != nil {
				done <- err
				return
			}
			seen = append(seen, v)
		}
	}()
	time.Sleep(20 * time.Millisecond)
	if err := inv.Write(bg(), "b"); err != nil {
		t.Fatal(err)
	}
	_ = inv.Close()
	if err := <-done; err != io.EOF {
		t.Fatalf("expected io.EOF on close, got %v", err)
	}
	if len(seen) != 2 || seen[0] != "a" || seen[1] != "b" {
		t.Fatalf("expected [a b], got %v", seen)
	}
}

func TestFireErrorMakesParkedReadThrow(t *testing.T) {
	inv := NewInvocationImpl[string, any](bg())
	done := make(chan error, 1)
	go func() {
		_, err := inv.ReadInput(bg())
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	inv.FireError(&InvocationError{Code: ErrCodeRuntime, Message: "terminal"})
	if err := <-done; codeOf(t, err) != ErrCodeRuntime {
		t.Fatalf("parked read must surface the terminal, got %v", err)
	}
}

func TestWriteAfterCloseRejectsNonTerminal(t *testing.T) {
	inv := NewInvocationImpl[string, string](bg())
	_ = inv.Close()
	if err := inv.Write(bg(), "late"); codeOf(t, err) != ErrCodeInputClosed {
		t.Fatalf("expected ERR_INPUT_CLOSED, got %v", err)
	}
	// Non-terminal: outputs still flow.
	if err := inv.EmitOutput("still alive"); err != nil {
		t.Fatal(err)
	}
	inv.CloseOutput()
	vals, err := collectStream(t, inv.Outputs())
	if err != nil || len(vals) != 1 || vals[0] != "still alive" {
		t.Fatalf("invocation must continue after input close: %v err=%v", vals, err)
	}
}

func TestBindingCloseInputFreesCaller(t *testing.T) { // NI, U
	inv := NewInvocationImpl[string, string](bg())
	_ = inv.CloseInput() // binding-side: no-input operation
	if err := inv.Write(bg(), "x"); codeOf(t, err) != ErrCodeInputClosed {
		t.Fatalf("expected ERR_INPUT_CLOSED, got %v", err)
	}
	_ = inv.EmitOutput("out")
	inv.CloseOutput()
	vals, err := collectStream(t, inv.Outputs())
	if err != nil || len(vals) != 1 {
		t.Fatalf("got %v err=%v", vals, err)
	}
}

func TestWriteAfterTerminalReturnsTerminal(t *testing.T) {
	inv := NewInvocationImpl[string, any](bg())
	term := &InvocationError{Code: ErrCodeRuntime, Message: "done for"}
	inv.FireError(term)
	err := inv.Write(bg(), "x")
	var ie *InvocationError
	if !errors.As(err, &ie) || ie != term {
		t.Fatalf("expected the terminal error itself, got %v", err)
	}
}

func TestInputDrainsBufferedValueOnClose(t *testing.T) {
	// Write then Close: the buffered value must surface before EOF.
	inv := NewInvocationImpl[string, any](bg())
	_ = inv.Write(bg(), "buffered")
	_ = inv.Close()
	v, err := inv.ReadInput(shortCtx(t))
	if err != nil || v != "buffered" {
		t.Fatalf("buffered input lost on close: %v err=%v", v, err)
	}
	if _, err := inv.ReadInput(shortCtx(t)); err != io.EOF {
		t.Fatalf("expected EOF after drain, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OBI-T-07 validation hook
// ---------------------------------------------------------------------------

func TestValidateHookDualSignal(t *testing.T) {
	t07 := &InvocationError{Code: ErrCodeValidationFailed, Message: "input validation failed"}
	inv := NewInvocationImpl[int, any](bg())
	inv.validateInput = func(v int) *InvocationError {
		if v < 0 {
			return t07
		}
		return nil
	}
	if err := inv.Write(bg(), 1); err != nil {
		t.Fatal(err)
	}
	err := inv.Write(bg(), -1)
	var ie *InvocationError
	if !errors.As(err, &ie) || ie != t07 {
		t.Fatalf("write must reject with the same T-07 error, got %v", err)
	}
	// Terminal: the binding's read surfaces the same error.
	if _, rerr := inv.ReadInput(shortCtx(t)); codeOf(t, rerr) != ErrCodeValidationFailed {
		t.Fatalf("expected terminal on binding side, got %v", rerr)
	}
}

// ---------------------------------------------------------------------------
// Cancellation & close
// ---------------------------------------------------------------------------

func TestCancelBeforeDriveTearsDownInertHandle(t *testing.T) {
	inv := NewInvocationImpl[any, any](bg())
	inv.Cancel()
	select {
	case <-inv.Done():
	default:
		t.Fatal("Done must fire on cancel")
	}
	_, err := collectStream(t, inv.Outputs())
	if codeOf(t, err) != ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", err)
	}
}

func TestUpstreamCtxConvergesWithCancel(t *testing.T) { // SS, BD
	ctx, cancel := context.WithCancel(bg())
	inv := NewInvocationImpl[any, int](ctx)
	cancel()
	out := inv.Outputs()
	_, err := out.Read(shortCtx(t))
	if codeOf(t, err) != ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED via upstream ctx, got %v", err)
	}
}

func TestPreCancelledCtxIsImmediatelyTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(bg())
	cancel()
	inv := NewInvocationImpl[any, any](ctx)
	select {
	case <-inv.Done():
	default:
		t.Fatal("pre-cancelled ctx must yield a terminal handle")
	}
}

func TestStopCancelsInvocation(t *testing.T) {
	inv := NewInvocationImpl[any, int](bg())
	_ = inv.EmitOutput(1)
	out := inv.Outputs()
	if _, err := out.Read(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	out.Stop() // Go's explicit early-abandon (no for-await return() hook)
	select {
	case <-inv.Done():
	default:
		t.Fatal("Stop must cancel the invocation")
	}
}

// ---------------------------------------------------------------------------
// Deadline vs. explicit cancellation (mid-stream) — parity with TS
// invocation.test.ts. The codes remain distinct but imply no portable retry
// or side-effect policy; outputs may already have flowed in either case.
// ---------------------------------------------------------------------------

func invErrOf(t *testing.T, err error) *InvocationError {
	t.Helper()
	var ie *InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvocationError, got %T: %v", err, err)
	}
	return ie
}

// A mid-stream lifetime deadline: already-emitted outputs STAND, then exactly
// one terminal ERR_TIMEOUT. Previously emitted outputs still stand.
func TestMidStreamDeadlineIsTimeout(t *testing.T) { // SS
	ctx, cancel := context.WithTimeout(bg(), 60*time.Millisecond)
	t.Cleanup(cancel)
	inv := NewInvocationImpl[any, int](ctx)

	// Two outputs flow before the deadline fires (buffered; non-blocking).
	if err := inv.EmitOutput(1); err != nil {
		t.Fatal(err)
	}
	if err := inv.EmitOutput(2); err != nil {
		t.Fatal(err)
	}

	<-inv.Done() // the lifetime deadline drives the terminal

	vals, err := collectStream(t, inv.Outputs())
	if len(vals) != 2 || vals[0] != 1 || vals[1] != 2 {
		t.Fatalf("previously-emitted outputs must stand, got %v", vals)
	}
	ie := invErrOf(t, err)
	if ie.Code != ErrCodeTimeout {
		t.Fatalf("code = %q, want ERR_TIMEOUT", ie.Code)
	}
}

// An explicit cancel() mid-stream: outputs stand, then exactly one
// ERR_CANCELLED. Unchanged by the deadline fix.
func TestMidStreamCancelStaysCancelled(t *testing.T) { // SS
	inv := NewInvocationImpl[any, int](bg())
	if err := inv.EmitOutput(1); err != nil {
		t.Fatal(err)
	}
	inv.Cancel()

	vals, err := collectStream(t, inv.Outputs())
	if len(vals) != 1 || vals[0] != 1 {
		t.Fatalf("previously-emitted output must stand, got %v", vals)
	}
	ie := invErrOf(t, err)
	if ie.Code != ErrCodeCancelled {
		t.Fatalf("code = %q, want ERR_CANCELLED", ie.Code)
	}
}

// A caller-supplied deadline that fires via the AfterFunc classifies as a
// TIMEOUT even with no output in flight (parity: the code, not the presence
// of outputs, decides).
func TestDeadlineWithoutOutputIsTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(bg(), 20*time.Millisecond)
	t.Cleanup(cancel)
	inv := NewInvocationImpl[any, int](ctx)
	_, err := collectStream(t, inv.Outputs())
	ie := invErrOf(t, err)
	if ie.Code != ErrCodeTimeout {
		t.Fatalf("deadline terminal code = %s, want ERR_TIMEOUT", ie.Code)
	}
}

// An already-expired lifetime is classified the same way a mid-flight one is:
// a deadline is a TIMEOUT, an explicit cancel is CANCELLED.
func TestPreExpiredDeadlineIsTimeout(t *testing.T) {
	ctx, cancel := context.WithDeadline(bg(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	inv := NewInvocationImpl[any, int](ctx)
	_, err := collectStream(t, inv.Outputs())
	if ie := invErrOf(t, err); ie.Code != ErrCodeTimeout {
		t.Fatalf("pre-expired deadline code = %q, want ERR_TIMEOUT", ie.Code)
	}
}

// AsInvocationError distinguishes a deadline (TIMEOUT) from a cancel (CANCELLED).
func TestAsInvocationErrorDeadlineIsTimeout(t *testing.T) {
	if ie := AsInvocationError(context.DeadlineExceeded); ie.Code != ErrCodeTimeout {
		t.Errorf("DeadlineExceeded → %s, want ERR_TIMEOUT", ie.Code)
	}
	if ie := AsInvocationError(context.Canceled); ie.Code != ErrCodeCancelled {
		t.Errorf("Canceled → %s, want ERR_CANCELLED", ie.Code)
	}
}

// ---------------------------------------------------------------------------
// Metadata
// ---------------------------------------------------------------------------

func TestHeaderResolvesWithBindingMetadata(t *testing.T) { // SS, U
	inv := NewInvocationImpl[any, string](bg())
	if err := inv.SetHeader(Metadata{"content-type": {"application/json"}}); err != nil {
		t.Fatal(err)
	}
	_ = inv.EmitOutput("x")
	md, err := inv.Header(shortCtx(t))
	if err != nil || len(md["content-type"]) != 1 {
		t.Fatalf("got %v err=%v", md, err)
	}
}

func TestHeaderSettlesEmptyOnFirstOutput(t *testing.T) {
	inv := NewInvocationImpl[any, string](bg())
	_ = inv.EmitOutput("x")
	md, err := inv.Header(shortCtx(t))
	if err != nil || len(md) != 0 {
		t.Fatalf("expected empty header, got %v err=%v", md, err)
	}
}

func TestHeaderSettlesOnTerminal(t *testing.T) {
	inv := NewInvocationImpl[any, any](bg())
	inv.FireError(&InvocationError{Code: ErrCodeRuntime, Message: "early death"})
	md, err := inv.Header(shortCtx(t))
	if err != nil || len(md) != 0 {
		t.Fatalf("header must settle on terminal, got %v err=%v", md, err)
	}
}

func TestTrailerValidOnlyAfterTermination(t *testing.T) {
	inv := NewInvocationImpl[any, string](bg())
	inv.SetTrailer(Metadata{"grpc-status": {"0"}})
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Trailer before termination must panic")
			}
		}()
		_ = inv.Trailer()
	}()
	inv.CloseOutput()
	if md := inv.Trailer(); len(md["grpc-status"]) != 1 {
		t.Fatalf("expected trailer after close, got %v", md)
	}
}

func TestSetHeaderAfterFirstEmitErrors(t *testing.T) {
	inv := NewInvocationImpl[any, string](bg())
	_ = inv.EmitOutput("x")
	if err := inv.SetHeader(Metadata{"late": {"true"}}); err == nil {
		t.Fatal("late SetHeader must error")
	}
}

func TestTrailerAfterSingleShortCircuit(t *testing.T) {
	inv := NewInvocationImpl[any, int](bg())
	_ = inv.SetHeader(Metadata{"x-stream": {"yes"}})
	_ = inv.EmitOutput(1)
	_ = inv.EmitOutput(2)
	if _, err := Single(shortCtx(t), inv.Outputs()); codeOf(t, err) != ErrCodeExpectedSingle {
		t.Fatalf("expected short-circuit, got %v", err)
	}
	md, _ := inv.Header(shortCtx(t))
	if len(md["x-stream"]) != 1 {
		t.Fatalf("header set before output must remain valid, got %v", md)
	}
	if tr := inv.Trailer(); len(tr) != 0 {
		t.Fatalf("trailer of a cancelled call should be empty, got %v", tr)
	}
}

// ---------------------------------------------------------------------------
// Bidi & drive
// ---------------------------------------------------------------------------

func TestBidiSeparateGoroutineDrive(t *testing.T) { // BD
	inv := NewInvocationImpl[int, int](bg())
	const n = 25 // far beyond both buffer bounds

	// The "binding": echo loop.
	go func() {
		for {
			v, err := inv.ReadInput(bg())
			if err != nil {
				inv.CloseOutput()
				return
			}
			if err := inv.EmitOutput(v * 2); err != nil {
				return
			}
		}
	}()

	// The caller: pump and drain on separate goroutines (the blessed pattern).
	go func() {
		for i := 0; i < n; i++ {
			if err := inv.Write(bg(), i); err != nil {
				return
			}
		}
		_ = inv.Close()
	}()

	vals, err := collectStream(t, inv.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != n {
		t.Fatalf("expected %d outputs, got %d", n, len(vals))
	}
	for i, v := range vals {
		if v != i*2 {
			t.Fatalf("order violated at %d: %d", i, v)
		}
	}
}

func TestTypedInvocationMismatch(t *testing.T) {
	inv := NewInvocationImpl[any, any](bg())
	typed := NewTypedInvocation[string, string](inv)
	_ = inv.EmitOutput(42) // wrong type for O=string
	inv.CloseOutput()
	_, err := typed.Outputs().Read(shortCtx(t))
	if codeOf(t, err) != "ERR_TYPE_MISMATCH" {
		t.Fatalf("expected ERR_TYPE_MISMATCH, got %v", err)
	}
}

func TestContextRequiredIsJustATerminalCode(t *testing.T) {
	inv := NewInvocationImpl[any, any](bg())
	details := &ContextRequiredDetails{
		Target:       "api.example.com",
		Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.bearer"}}}},
	}
	inv.FireError(NewContextRequiredError("bearer token required", details))
	_, err := collectStream(t, inv.Outputs())
	var ie *InvocationError
	if !errors.As(err, &ie) || ContextRequiredFrom(ie) == nil {
		t.Fatalf("expected CONTEXT_REQUIRED with details, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Regression tests (post-review fixes)
// ---------------------------------------------------------------------------

// TestAcceptedEmitNeverLostUnderRacingTerminal stresses the seam between the
// mutex-guarded state machine and the channel select: an EmitOutput that
// returns nil ("accepted") must be observable by a reader draining to
// terminal, even when the terminal raced in from another goroutine.
// (Previously ~3.5e-5 of racing emits stranded an accepted value.)
func TestAcceptedEmitNeverLostUnderRacingTerminal(t *testing.T) {
	for iter := 0; iter < 20000; iter++ {
		inv := NewInvocationImpl[any, int](bg())

		accepted := make(chan bool, 1)
		go func() {
			err := inv.EmitOutput(42)
			accepted <- err == nil
		}()
		go inv.Cancel()

		got := false
		out := inv.Outputs()
		for {
			v, err := out.Read(bg())
			if err != nil {
				break
			}
			if v == 42 {
				got = true
			}
		}
		if <-accepted && !got {
			t.Fatalf("iter %d: emit accepted but value never surfaced before terminal", iter)
		}
	}
}

func TestConcurrentOutputReadersPanic(t *testing.T) { // SS, BD
	inv := NewInvocationImpl[any, int](bg())
	out := inv.Outputs()
	ready := make(chan struct{})
	go func() {
		close(ready)
		_, _ = out.Read(bg()) // parks (no output yet)
	}()
	<-ready
	time.Sleep(20 * time.Millisecond)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on concurrent OutputStream.Read")
		}
		inv.CloseOutput() // unpark the goroutine reader
	}()
	_, _ = out.Read(bg())
}

func TestSetTrailerAfterTerminalIsDropped(t *testing.T) {
	// Terminal state is reachable via a benign cancellation race, so a late
	// SetTrailer must be silently dropped (per the documented contract),
	// not panic — panicking would crash the process.
	inv := NewInvocationImpl[any, any](bg())
	inv.SetTrailer(Metadata{"early": {"true"}})
	inv.CloseOutput()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("late SetTrailer must be dropped, not panic, got %v", r)
		}
	}()
	inv.SetTrailer(Metadata{"late": {"true"}})
	md := inv.Trailer()
	if len(md["late"]) != 0 {
		t.Fatalf("late SetTrailer must be dropped, got %v", md)
	}
	if len(md["early"]) != 1 {
		t.Fatalf("pre-terminal trailer must be retained, got %v", md)
	}
}

func TestMetadataReturnsAreIsolated(t *testing.T) {
	inv := NewInvocationImpl[any, string](bg())
	md := Metadata{"k": {"v"}}
	_ = inv.SetHeader(md)
	md["k"][0] = "mutated-by-binding" // binding mutates its map after set

	got, err := inv.Header(shortCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if got["k"][0] != "v" {
		t.Fatalf("header aliased the binding's map: %v", got)
	}
	got["k"] = []string{"mutated-by-caller"}
	again, _ := inv.Header(shortCtx(t))
	if again["k"][0] != "v" {
		t.Fatalf("header aliased a previous return: %v", again)
	}
}

func TestContextRequirementJSONRoundTripsExtra(t *testing.T) {
	durable := false
	req := ContextRequirement{
		Type:        "auth.oauth2",
		Durable:     &durable,
		Description: "OAuth token",
		Extra:       map[string]any{"authorizeUrl": "https://auth.example.com", "scopes": []any{"read"}},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var back ContextRequirement
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != "auth.oauth2" || back.Durable == nil || *back.Durable || back.Description != "OAuth token" {
		t.Fatalf("typed fields lost: %+v", back)
	}
	if back.Extra["authorizeUrl"] != "https://auth.example.com" {
		t.Fatalf("family-specific fields lost: %+v", back.Extra)
	}
}

func TestContextRequiredFromDecodesMapDetails(t *testing.T) {
	// Details that crossed a JSON boundary arrive as a generic map; the
	// operation layer must still recognize the challenge as retryable.
	err := &InvocationError{
		Code:    ErrCodeContextRequired,
		Message: "bearer required",
		Details: map[string]any{
			"target":       "api.example.com",
			"alternatives": []any{map[string]any{"requirements": []any{map[string]any{"type": "auth.bearer"}}}},
		},
	}
	d := ContextRequiredFrom(err)
	if d == nil || d.Target != "api.example.com" || len(d.Alternatives) != 1 ||
		d.Alternatives[0].Requirements[0].Type != "auth.bearer" {
		t.Fatalf("map-shaped details not decoded: %+v", d)
	}
}

// A cancellation that lands while the input side was never written nor
// closed is almost always the forgot-to-Write hang; the terminal message
// diagnoses it instead of reporting a bare cancellation.
func TestCancelDiagnosesNeverWrittenInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inv := NewInvocationImpl[string, string](ctx)
	cancel()
	_, err := inv.Outputs().Read(bg())
	if err == nil || !strings.Contains(err.Error(), "never written or closed") {
		t.Errorf("want the forgot-to-Write diagnosis, got: %v", err)
	}

	// A written invocation keeps the plain message.
	ctx2, cancel2 := context.WithCancel(context.Background())
	inv2 := NewInvocationImpl[string, string](ctx2)
	if err := inv2.Write(bg(), "x"); err != nil {
		t.Fatal(err)
	}
	cancel2()
	_, err = inv2.Outputs().Read(bg())
	if err == nil || strings.Contains(err.Error(), "never written") {
		t.Errorf("written invocation should get the plain cancellation, got: %v", err)
	}
}
