// Package valueio contains the SDK-private ownership bridge between public
// invocation surfaces and private value snapshots. It knows no operation,
// evaluator, protocol or portable error code, and it keeps no ledger: the
// per-value limits applied at capture, delivery and construction are the only
// resource policy here.
package valueio

import (
	"context"

	"github.com/openbindings/openbindings-go/internal/value"
)

// Packet is one admitted logical value in transit inside the SDK. The
// snapshot is immutable; the packet carries no reservation and needs no
// release. Garbage collection owns its lifetime once every queue drops it.
type Packet struct {
	Snapshot *value.Snapshot
}

// Capture admits an ordinary value under the per-value limits.
func Capture(ctx context.Context, limits value.Limits, input any) (*Packet, error) {
	s, err := value.Capture(ctx, input, value.Options{Limits: limits})
	if err != nil {
		return nil, err
	}
	return &Packet{Snapshot: s}, nil
}

// View lends an SDK read-only view when the snapshot is already an ordinary
// logical tree, or gives the consumer its own mutable logical tree. Application
// callbacks and public results must ask for the mutable form.
func (p *Packet) View(ctx context.Context, limits value.Limits, mutable bool) (any, error) {
	if !mutable {
		if v, ok := p.Snapshot.ReadOnlyLogical(); ok {
			return v, nil
		}
	}
	return p.Snapshot.Logical(ctx, value.Options{Limits: limits})
}

// Construct performs fresh checked construction of T from the packet.
func Construct[T any](ctx context.Context, p *Packet, limits value.Limits) (T, error) {
	return value.Construct[T](ctx, p.Snapshot, value.Options{Limits: limits})
}
