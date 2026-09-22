package operationgraph

import (
	"context"
	"errors"
	"strings"

	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
	"github.com/openbindings/openbindings-go/invoke"
)

// Values inside the engine are immutable snapshots (valueio.Packet) plus a
// read-only logical view the engine's own bookkeeping reads. Every event
// stored anywhere (a queue slot, buffer or combine state, an in-flight
// worker) is its own copy from retainEvent, sharing the snapshot. The only
// retention the graph accounts for is the caller-written root value each
// lineage descends from, and only when an expression can observe it as
// $input: a root lives exactly as long as some stored event carries its
// index. There is no live budget; per-value limits apply at every capture,
// mutable view and construction.

// referencesInput reports whether any node expression can observe $input.
// It is a textual check over every expression position (transform, and the
// transform of filter and map); a false positive only retains roots the
// graph never reads. When it is false the engine roots nothing and binds
// nothing.
func referencesInput(g *Graph) bool {
	for _, n := range g.Nodes {
		if n.Transform != nil && strings.Contains(*n.Transform, "$input") {
			return true
		}
	}
	return false
}

// rootRef is one caller-written root value and the count of live holders
// whose lineage descends from it: retained events in queues, buffer and
// combine state, in-flight workers, and conduits whose merged root it is.
type rootRef struct {
	packet *valueio.Packet
	refs   int
}

// addRoot makes the input event the root of a new lineage. The event itself
// holds the first reference; the pump releases it after enqueueing.
func (eng *engine) addRoot(ev *event) {
	if !eng.usesInput {
		ev.root = noRoot
		return
	}
	eng.rootMu.Lock()
	defer eng.rootMu.Unlock()
	eng.nextRoot++
	eng.roots[eng.nextRoot] = &rootRef{packet: ev.packet, refs: 1}
	ev.root, ev.held, ev.eng = eng.nextRoot, true, eng
}

// holdRoot takes one reference on a live root. A hold is only ever taken
// while another holder of the same root is live (the event being copied,
// or the conduit or worker that produced it), so a missing entry is an
// engine invariant violation and is loud rather than silently unbound.
func (eng *engine) holdRoot(root int) {
	eng.rootMu.Lock()
	defer eng.rootMu.Unlock()
	r := eng.roots[root]
	if r == nil {
		panic("operationgraph: hold on a released root")
	}
	r.refs++
}

// dropRoot releases one reference; the last release drops the root value.
func (eng *engine) dropRoot(root int) {
	eng.rootMu.Lock()
	defer eng.rootMu.Unlock()
	r := eng.roots[root]
	if r == nil {
		return
	}
	r.refs--
	if r.refs == 0 {
		delete(eng.roots, root)
	}
}

// rootPacket resolves an event's root to the caller-written value, or
// reports that $input is undefined for it.
func (eng *engine) rootPacket(root int) (*valueio.Packet, bool) {
	if root == noRoot {
		return nil, false
	}
	eng.rootMu.Lock()
	defer eng.rootMu.Unlock()
	r := eng.roots[root]
	if r == nil {
		return nil, false
	}
	return r.packet, true
}

// liveRoots is the number of root values currently retained.
func (eng *engine) liveRoots() int {
	eng.rootMu.Lock()
	defer eng.rootMu.Unlock()
	return len(eng.roots)
}

// release gives up this copy's reference on its lineage root, if it holds
// one. Snapshots need no release; garbage collection owns them.
func (ev *event) release() {
	if ev == nil || !ev.held {
		return
	}
	ev.held = false
	ev.eng.dropRoot(ev.root)
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

// retainEvent gives one place that stores an event its own copy. The copy
// shares the immutable snapshot and takes one reference on the lineage
// root. A value the graph assembled itself (a batch, an error event, a map
// element) has no snapshot yet and is admitted here under the per-value
// limits; a refusal ends the invocation. Markers carry no value and hold no
// root. A nil result means the invocation is over.
func (eng *engine) retainEvent(ev *event) *event {
	if eng.ctx.Err() != nil {
		return nil
	}
	out := cloneEvent(ev)
	marker := out.complete || out.fatal != nil
	if out.packet == nil && !marker {
		p, err := valueio.Capture(eng.ctx, eng.limits, ev.data)
		if err != nil {
			eng.failValue(err)
			return nil
		}
		view, err := p.View(eng.ctx, eng.limits, false)
		if err != nil {
			eng.failValue(err)
			return nil
		}
		out.packet, out.data = p, view
	}
	if eng.usesInput && !marker && out.root != noRoot {
		eng.holdRoot(out.root)
		out.held, out.eng = true, eng
	}
	return out
}

// packetEvent wraps an admitted snapshot read from an SDK-owned queue as an
// unrooted event with a read-only view. A view failure ends the invocation.
func (eng *engine) packetEvent(ctx context.Context, p *valueio.Packet) (*event, error) {
	v, err := p.View(ctx, eng.limits, false)
	if err != nil {
		eng.failValue(err)
		return nil, err
	}
	return &event{data: v, packet: p}, nil
}

// captureEvent admits a value read from a foreign handle through its
// ordinary public interface. A refusal ends the invocation.
func (eng *engine) captureEvent(ctx context.Context, v any) (*event, error) {
	p, err := valueio.Capture(ctx, eng.limits, v)
	if err != nil {
		eng.failValue(err)
		return nil, err
	}
	return eng.packetEvent(ctx, p)
}

func (eng *engine) input(ctx context.Context) (*event, error) {
	if ep := valueio.From(eng.handle); ep != nil {
		p, err := ep.ReadInput(ctx)
		if err != nil {
			return nil, err
		}
		return eng.packetEvent(ctx, p)
	}
	v, err := eng.handle.ReadInput(ctx)
	if err != nil {
		return nil, err
	}
	return eng.captureEvent(ctx, v)
}

func (eng *engine) outputs(call invoke.Invocation[any, any]) (func(context.Context) (*event, error), func()) {
	if ep := valueio.From(call); ep != nil {
		ep.ClaimOutput()
		return func(ctx context.Context) (*event, error) {
			p, err := ep.ReadOutput(ctx)
			if err != nil {
				return nil, err
			}
			return eng.packetEvent(ctx, p)
		}, ep.Stop
	}
	out := call.Outputs()
	return func(ctx context.Context) (*event, error) {
		v, err := out.Read(ctx)
		if err != nil {
			return nil, err
		}
		return eng.captureEvent(ctx, v)
	}, out.Stop
}

// writeChild hands an event to an inner invocation: the same snapshot to an
// SDK-owned handle (no copy), a mutable tree of its own to a foreign one.
func (eng *engine) writeChild(ctx context.Context, call invoke.Invocation[any, any], ev *event) error {
	if ep := valueio.From(call); ep != nil {
		return ep.SendInput(ctx, ev.packet)
	}
	raw, err := ev.packet.View(ctx, eng.limits, true)
	if err != nil {
		eng.failValue(err)
		return err
	}
	return call.Write(ctx, raw)
}

// emit delivers an event to the caller: the same snapshot through an
// SDK-owned handle, a mutable tree through a foreign one.
func (eng *engine) emit(ev *event) error {
	if ep := valueio.From(eng.handle); ep != nil {
		return ep.SendOutput(ev.packet)
	}
	raw, err := ev.packet.View(eng.ctx, eng.limits, true)
	if err != nil {
		eng.failValue(err)
		return err
	}
	return eng.handle.EmitOutput(raw)
}

// resolvedLimits are the per-value limits the engine applies: the session's
// when the handle is SDK-owned, the resolved defaults otherwise.
func resolvedLimits(handle invoke.BindingHandle[any, any]) value.Limits {
	if ep := valueio.From(handle); ep != nil {
		return ep.Limits
	}
	limits, _ := value.Limits{}.Resolve()
	return limits
}

func isValueFailure(err error) bool {
	var limit *value.LimitError
	return errors.As(err, &limit)
}
