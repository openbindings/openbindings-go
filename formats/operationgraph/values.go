package operationgraph

import (
	"context"
	"errors"

	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
	"github.com/openbindings/openbindings-go/invoke"
)

// Each queued event, retained state entry and asynchronous worker owns a full
// root charge. Immutable snapshots may share storage, never accounting credit.
func (ev *event) release() {
	if ev == nil {
		return
	}
	ev.owner.Release()
	if ev.viewRelease != nil {
		ev.viewRelease()
	}
	if ev.metadata != nil {
		ev.metadata.Release()
	}
}

func (eng *engine) failValue(err error) {
	if eng.ctx.Err() != nil {
		return
	}
	eng.exitFlag.Store(true)
	if ep := valueio.From(eng.handle); ep != nil {
		ep.FailValue(err)
	} else {
		eng.handle.FireError(invoke.NewInvocationError(invoke.ErrCodeRuntime))
	}
}

func (eng *engine) retainEvent(ev *event) *event {
	if eng.ctx.Err() != nil {
		return nil
	}
	meta := eng.scope.NewReservation()
	units := int64(256 + len(ev.source))
	for key := range ev.lineage {
		units += int64(128 + 2*len(key))
	}
	// Graph-internal capacity is never considered independently drainable.
	if err := meta.Adjust(eng.ctx, nil, units); err != nil {
		eng.failValue(err)
		return nil
	}
	var p *valueio.Packet
	var err error
	if !ev.complete && ev.fatal == nil {
		if ev.owner == nil {
			p, err = valueio.Capture(eng.ctx, nil, eng.scope, ev.data)
		} else {
			p, err = ev.owner.Retain(eng.ctx, nil)
		}
	} else if ev.fatal != nil && ev.fatal.HasData() {
		p, err = valueio.Capture(eng.ctx, nil, eng.scope, ev.fatal.Data)
	}
	if err != nil {
		meta.Release()
		eng.failValue(err)
		return nil
	}
	out := cloneEvent(ev)
	out.complete, out.fatal = ev.complete, ev.fatal
	out.owner, out.metadata = p, meta
	if p != nil {
		out.data, out.viewRelease, err = p.View(eng.ctx, false)
		if err != nil {
			out.release()
			eng.failValue(err)
			return nil
		}
		if ev.fatal != nil {
			copyError := *ev.fatal
			copyError.Data = out.data
			out.fatal = &copyError
		}
	}
	return out
}

// readOwned transfers an SDK queue packet directly into Graph ownership. A
// foreign implementation is admitted through its ordinary public interface.
func (eng *engine) readOwned(ctx context.Context, read func(context.Context) (*valueio.Packet, error)) (*event, error) {
	p, err := read(ctx)
	if err != nil {
		return nil, err
	}
	p, err = valueio.Transfer(ctx, nil, eng.scope, p)
	if err != nil {
		return nil, err
	}
	v, release, err := p.View(ctx, false)
	if err != nil {
		p.Release()
		return nil, err
	}
	return &event{data: v, owner: p, viewRelease: release}, nil
}

func (eng *engine) input(ctx context.Context) (*event, error) {
	if ep := valueio.From(eng.handle); ep != nil {
		return eng.readOwned(ctx, ep.ReadInput)
	}
	v, err := eng.handle.ReadInput(ctx)
	if err != nil {
		return nil, err
	}
	ev := eng.retainEvent(&event{data: v})
	if ev == nil {
		return nil, context.Canceled
	}
	return ev, nil
}

func (eng *engine) outputs(call invoke.Invocation[any, any]) (func(context.Context) (*event, error), func()) {
	if ep := valueio.From(call); ep != nil {
		ep.ClaimOutput()
		return func(ctx context.Context) (*event, error) { return eng.readOwned(ctx, ep.ReadOutput) }, ep.Stop
	}
	out := call.Outputs()
	return func(ctx context.Context) (*event, error) {
		v, err := out.Read(ctx)
		if err != nil {
			return nil, err
		}
		ev := eng.retainEvent(&event{data: v})
		if ev == nil {
			return nil, context.Canceled
		}
		return ev, nil
	}, out.Stop
}

func (eng *engine) writeChild(ctx context.Context, call invoke.Invocation[any, any], ev *event) error {
	if ep := valueio.From(call); ep != nil && ev.owner != nil {
		p, err := ev.owner.Retain(ctx, nil)
		if err != nil {
			eng.failValue(err)
			return err
		}
		return ep.SendInput(ctx, p)
	}
	// A foreign override receives its own mutable tree.
	raw, release, err := ev.owner.View(ctx, true)
	if err != nil {
		eng.failValue(err)
		return err
	}
	defer release()
	return call.Write(ctx, raw)
}

func (eng *engine) emit(ev *event) error {
	if ep := valueio.From(eng.handle); ep != nil && ev.owner != nil {
		p, err := ev.owner.Retain(eng.ctx, nil)
		if err != nil {
			eng.failValue(err)
			return err
		}
		return ep.SendOutput(p)
	}
	raw, release, err := ev.owner.View(eng.ctx, true)
	if err != nil {
		eng.failValue(err)
		return err
	}
	defer release()
	return eng.handle.EmitOutput(raw)
}

func isValueFailure(err error) bool {
	var limit *value.LimitError
	return errors.As(err, &limit)
}
