package invoke

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/openbindings/openbindings-go/internal/valueio"
)

func acquireCapture(ctx context.Context, done <-chan struct{}, permit chan struct{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case permit <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return context.Canceled
	}
}
func (i *InvocationImpl[I, O]) captureInput(ctx context.Context, input any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := i.writableErr(); err != nil {
		return err
	}
	if err := acquireCapture(ctx, i.done, i.inputPermit); err != nil {
		if state := i.writableErr(); state != nil {
			return state
		}
		return err
	}
	defer func() { <-i.inputPermit }()
	p, err := valueio.Capture(ctx, i.done, i.scope, input)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if state := i.writableErr(); state != nil {
			return state
		}
		ie := valueError(err, "input capture")
		if resourceFailure(err) {
			i.FireError(ie)
		}
		return ie
	}
	return i.writePacket(ctx, p)
}
func (i *InvocationImpl[I, O]) captureOutput(output any) error {
	if err := acquireCapture(context.Background(), i.done, i.outputPermit); err != nil {
		return i.terminalOrClosedErr()
	}
	defer func() { <-i.outputPermit }()
	p, err := valueio.Capture(context.Background(), i.done, i.scope, output)
	if err != nil {
		select {
		case <-i.done:
			return i.terminalOrClosedErr()
		default:
		}
		ie := valueError(err, "output capture")
		i.FireError(ie)
		return ie
	}
	return i.emitPacket(p)
}
func (i *InvocationImpl[I, O]) ReadInput(ctx context.Context) (I, error) {
	var zero I
	if i.inputDeliveryReaders.Add(1) > 1 {
		i.inputDeliveryReaders.Add(-1)
		panic(fmt.Sprintf("openbindings: %s: concurrent ReadInput", ErrCodeAlreadyConsumed))
	}
	defer i.inputDeliveryReaders.Add(-1)
	p, err := i.readInputPacket(ctx)
	if err != nil {
		return zero, err
	}
	defer p.Release()
	out, err := valueio.Construct[I](ctx, p, true)
	if err != nil {
		ie := valueError(err, "handler input")
		i.FireError(ie)
		return zero, ie
	}
	return out, nil
}
func (s *outputStream[I, O]) Read(ctx context.Context) (O, error) {
	var zero O
	if s.readers.Add(1) > 1 {
		s.readers.Add(-1)
		panic(fmt.Sprintf("openbindings: %s: concurrent OutputStream.Read", ErrCodeAlreadyConsumed))
	}
	defer s.readers.Add(-1)
	p, err := s.impl.readOutputPacket(ctx)
	if err != nil {
		return zero, err
	}
	defer p.Release()
	out, err := valueio.Construct[O](ctx, p, false)
	if err != nil {
		return zero, &ValueConversionError{Stage: "typed delivery", Cause: valueError(err, "typed delivery")}
	}
	return out, nil
}
func (i *InvocationImpl[I, O]) stopOutputs() {
	i.mu.Lock()
	i.stopped = true
	i.terminalData.Release()
	i.terminalData = nil
	if i.terminalErr != nil {
		i.terminalErr.dataPresent = false
	}
	i.mu.Unlock()
	i.Cancel()
	i.discardStoppedOutputs()
}
func (i *InvocationImpl[I, O]) discardStoppedOutputs() {
	i.mu.Lock()
	stopped := i.stopped
	i.mu.Unlock()
	if !stopped {
		return
	}
	for {
		select {
		case p := <-i.outputCh:
			p.Release()
		default:
			return
		}
	}
}
func (i *InvocationImpl[I, O]) discardTerminalInputs() {
	i.mu.Lock()
	terminal := i.state != stateOpen
	i.mu.Unlock()
	if !terminal {
		return
	}
	for {
		select {
		case p := <-i.inputCh:
			p.Release()
		default:
			return
		}
	}
}

// readLocalInput keeps native leaves through checked local-handler construction.
func readLocalInput[T any](ctx context.Context, h BindingHandle[any, any]) (T, error) {
	var zero T
	if ep := valueio.From(h); ep != nil {
		p, err := ep.ReadInput(ctx)
		if err != nil {
			return zero, err
		}
		defer p.Release()
		x, err := valueio.Construct[T](ctx, p, true)
		if err != nil {
			return zero, valueError(err, "local input")
		}
		return x, nil
	}
	raw, err := h.ReadInput(ctx)
	if err != nil {
		return zero, err
	}
	scope, _ := valueio.NewScope(valueio.Limits{})
	p, err := valueio.Capture(ctx, nil, scope, raw)
	if err != nil {
		return zero, valueError(err, "local input")
	}
	defer p.Release()
	x, err := valueio.Construct[T](ctx, p, true)
	if err != nil {
		return zero, valueError(err, "local input")
	}
	return x, nil
}
func emitLocalOutput[T any](h BindingHandle[any, any], output T) error {
	if ep := valueio.From(h); ep != nil {
		return ep.CaptureOutput(output)
	}
	scope, _ := valueio.NewScope(valueio.Limits{})
	p, err := valueio.Capture(context.Background(), h.Done(), scope, output)
	if err != nil {
		ie := valueError(err, "local output")
		h.FireError(ie)
		return ie
	}
	defer p.Release()
	raw, err := valueio.Construct[any](context.Background(), p, true)
	if err != nil {
		ie := valueError(err, "local output")
		h.FireError(ie)
		return ie
	}
	return h.EmitOutput(raw)
}

// streamReadGuard covers conversion as well as dequeue on a typed adapter.
type streamReadGuard struct{ reading atomic.Int32 }

func (g *streamReadGuard) begin() {
	if g.reading.Add(1) > 1 {
		g.reading.Add(-1)
		panic(fmt.Sprintf("openbindings: %s: concurrent Read", ErrCodeAlreadyConsumed))
	}
}
func (g *streamReadGuard) end() { g.reading.Add(-1) }
