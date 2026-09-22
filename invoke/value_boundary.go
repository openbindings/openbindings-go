package invoke

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/openbindings/openbindings-go/internal/valueio"
)

func (i *InvocationImpl[I, O]) captureInput(ctx context.Context, input any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := i.writableErr(); err != nil {
		return err
	}
	p, err := valueio.Capture(ctx, i.limits, input)
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
	// Avoid running a producer codec once the terminal is already known.
	// emitPacket still checks state after capture to handle a racing terminal.
	select {
	case <-i.done:
		return i.terminalOrClosedErr()
	default:
	}
	p, err := valueio.Capture(context.Background(), i.limits, output)
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
	out, err := valueio.Construct[I](ctx, p, i.limits)
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
	out, err := valueio.Construct[O](ctx, p, s.impl.limits)
	if err != nil {
		return zero, &ValueConversionError{Stage: "typed delivery", Cause: valueError(err, "typed delivery")}
	}
	return out, nil
}

// readLocalInput keeps native leaves through checked local-handler construction.
func readLocalInput[T any](ctx context.Context, h BindingHandle[any, any]) (T, error) {
	var zero T
	if ep := valueio.From(h); ep != nil {
		p, err := ep.ReadInput(ctx)
		if err != nil {
			return zero, err
		}
		x, err := valueio.Construct[T](ctx, p, ep.Limits)
		if err != nil {
			return zero, valueError(err, "local input")
		}
		return x, nil
	}
	raw, err := h.ReadInput(ctx)
	if err != nil {
		return zero, err
	}
	limits := defaultValueLimits()
	p, err := valueio.Capture(ctx, limits, raw)
	if err != nil {
		return zero, valueError(err, "local input")
	}
	x, err := valueio.Construct[T](ctx, p, limits)
	if err != nil {
		return zero, valueError(err, "local input")
	}
	return x, nil
}
func emitLocalOutput[T any](h BindingHandle[any, any], output T) error {
	if ep := valueio.From(h); ep != nil {
		return ep.CaptureOutput(output)
	}
	limits := defaultValueLimits()
	p, err := valueio.Capture(context.Background(), limits, output)
	if err != nil {
		ie := valueError(err, "local output")
		h.FireError(ie)
		return ie
	}
	raw, err := valueio.Construct[any](context.Background(), p, limits)
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
