// Package valueio contains the SDK-private ownership bridge and accounting
// mechanism. It knows no operation, evaluator, protocol or portable error code.
package valueio

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"

	"github.com/openbindings/openbindings-go/internal/value"
)

const DefaultMaxLiveUnits int64 = 256 << 20

type Limits struct {
	MaxValueUnits, MaxLiveUnits int64
	MaxDepth                    int
}

func (l Limits) Resolve() (Limits, error) {
	v, err := (value.Limits{MaxUnits: l.MaxValueUnits, MaxDepth: l.MaxDepth}).Resolve()
	if err != nil {
		return l, err
	}
	l.MaxValueUnits = v.MaxUnits
	l.MaxDepth = v.MaxDepth
	if l.MaxLiveUnits == 0 {
		l.MaxLiveUnits = DefaultMaxLiveUnits
	}
	if l.MaxLiveUnits < 0 || l.MaxLiveUnits > math.MaxInt64/8 {
		return l, errors.New("invalid live value limit")
	}
	return l, nil
}
func (l Limits) Value() value.Limits {
	return value.Limits{MaxUnits: l.MaxValueUnits, MaxDepth: l.MaxDepth}
}

type Scope struct {
	mu              sync.Mutex
	limits          Limits
	live, drainable int64
	changed         chan struct{}
}

func NewScope(l Limits) (*Scope, error) {
	l, err := l.Resolve()
	if err != nil {
		return nil, err
	}
	return &Scope{limits: l, changed: make(chan struct{})}, nil
}
func (s *Scope) Limits() Limits        { return s.limits }
func (s *Scope) Usage() (int64, int64) { s.mu.Lock(); defer s.mu.Unlock(); return s.live, s.drainable }
func (s *Scope) signal()               { close(s.changed); s.changed = make(chan struct{}) }

type Reservation struct {
	scope     *Scope
	units     int64
	drainable bool
	released  bool
}

func (s *Scope) NewReservation() *Reservation { return &Reservation{scope: s} }
func (r *Reservation) Adjust(ctx context.Context, done <-chan struct{}, n int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s := r.scope
	for {
		s.mu.Lock()
		if r.released {
			s.mu.Unlock()
			if n == 0 {
				return nil
			}
			return errors.New("reservation already released")
		}
		if n < 0 {
			if n < -r.units {
				s.mu.Unlock()
				panic("valueio: excess release")
			}
			r.units += n
			s.live += n
			if r.drainable {
				s.drainable += n
			}
			s.signal()
			s.mu.Unlock()
			return nil
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		select {
		case <-done:
			s.mu.Unlock()
			return context.Canceled
		default:
		}
		if n <= s.limits.MaxLiveUnits-s.live {
			r.units += n
			s.live += n
			if r.drainable {
				s.drainable += n
			}
			s.mu.Unlock()
			return nil
		}
		// Public outputs have protected delivery capacity and can drain without
		// this reservation. No other owner is a safe reason to park a producer.
		if n > s.limits.MaxLiveUnits-(s.live-s.drainable) {
			s.mu.Unlock()
			return &value.LimitError{Stage: "retention", Kind: "live", Allowance: s.limits.MaxLiveUnits}
		}
		wake := s.changed
		s.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return context.Canceled
		}
	}
}

// TryAdjust admits terminal/control storage without waiting for a public reader.
// Cancellation and failure must remain able to complete when output is full.
func (r *Reservation) TryAdjust(n int64) error {
	s := r.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.released {
		return errors.New("reservation already released")
	}
	if n < -r.units {
		panic("valueio: excess release")
	}
	if n > s.limits.MaxLiveUnits-s.live {
		return &value.LimitError{Stage: "retention", Kind: "live", Allowance: s.limits.MaxLiveUnits}
	}
	r.units += n
	s.live += n
	if r.drainable {
		s.drainable += n
	}
	if n < 0 {
		s.signal()
	}
	return nil
}

func (r *Reservation) Account(ctx context.Context, done <-chan struct{}) func(int64) error {
	return func(n int64) error { return r.Adjust(ctx, done, n) }
}
func (r *Reservation) MarkDrainable() {
	s := r.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if !r.released && !r.drainable {
		r.drainable = true
		s.drainable += r.units
		s.signal()
	}
}
func (r *Reservation) Release() {
	if r == nil {
		return
	}
	s := r.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.released {
		return
	}
	s.live -= r.units
	if r.drainable {
		s.drainable -= r.units
	}
	r.units = 0
	r.released = true
	s.signal()
}

type Packet struct {
	Snapshot    *value.Snapshot
	reservation *Reservation
}

func Capture(ctx context.Context, done <-chan struct{}, scope *Scope, input any) (*Packet, error) {
	r := scope.NewReservation()
	accepted := false
	defer func() {
		if !accepted {
			r.Release()
		}
	}()
	s, err := value.Capture(ctx, input, value.Options{Limits: scope.limits.Value(), Adjust: r.Account(ctx, done)})
	if err != nil {
		r.Release()
		return nil, err
	}
	accepted = true
	return &Packet{s, r}, nil
}

// CaptureTerminal reserves both an immutable record and serialized delivery
// scratch. Its reservation is not independently drainable.
func CaptureTerminal(ctx context.Context, scope *Scope, input any) (*Packet, error) {
	r := scope.NewReservation()
	accepted := false
	defer func() {
		if !accepted {
			r.Release()
		}
	}()
	s, err := value.Capture(ctx, input, value.Options{Limits: scope.limits.Value(), Adjust: r.TryAdjust})
	if err == nil {
		err = r.TryAdjust(2 * s.Cost())
	}
	if err != nil {
		r.Release()
		return nil, err
	}
	accepted = true
	return &Packet{s, r}, nil
}

func (p *Packet) Scope() *Scope { return p.reservation.scope }
func (p *Packet) Release() {
	if p != nil {
		p.reservation.Release()
	}
}
func (p *Packet) Retain(ctx context.Context, done <-chan struct{}) (*Packet, error) {
	r := p.Scope().NewReservation()
	if err := r.Adjust(ctx, done, p.Snapshot.Cost()); err != nil {
		r.Release()
		return nil, err
	}
	return &Packet{p.Snapshot, r}, nil
}
func (p *Packet) ReserveDelivery(ctx context.Context, done <-chan struct{}) error {
	return p.reservation.Adjust(ctx, done, 2*p.Snapshot.Cost())
}
func (p *Packet) MarkDrainable() { p.reservation.MarkDrainable() }

// View lends an SDK read-only view, or gives an application callback its own
// mutable logical tree. The caller releases scratch after the consumer returns.
func (p *Packet) View(ctx context.Context, mutable bool) (any, func(), error) {
	if !mutable {
		if v, ok := p.Snapshot.ReadOnlyLogical(); ok {
			return v, func() {}, nil
		}
	}
	r := p.Scope().NewReservation()
	if err := r.Adjust(ctx, nil, p.Snapshot.Cost()); err != nil {
		r.Release()
		return nil, func() {}, err
	}
	v, err := p.Snapshot.Logical(ctx, value.Options{Limits: p.Scope().limits.Value()})
	if err != nil {
		r.Release()
		return nil, func() {}, err
	}
	return v, r.Release, nil
}
func Construct[T any](ctx context.Context, p *Packet, internal bool) (T, error) {
	opts := value.Options{Limits: p.Scope().limits.Value()}
	if internal {
		r := p.Scope().NewReservation()
		defer r.Release()
		opts.Adjust = r.Account(ctx, nil)
	}
	return value.Construct[T](ctx, p.Snapshot, opts)
}

type scopeKey struct{}
type childKey struct{}
type sessionKey struct{}

// SessionContext authorizes exactly one SDK binding session to inherit a scope.
// A context later handed to application code cannot enroll unrelated sessions.
func SessionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, sessionKey{}, new(atomic.Bool))
}
func TakeScope(ctx context.Context) *Scope {
	if ctx == nil {
		return nil
	}
	permit, _ := ctx.Value(sessionKey{}).(*atomic.Bool)
	if permit == nil || !permit.CompareAndSwap(false, true) {
		return nil
	}
	return ScopeFrom(ctx)
}

func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(context.WithValue(context.WithValue(ctx, scopeKey{}, s), childKey{}, false), sessionKey{}, (*atomic.Bool)(nil))
}
func ScopeFrom(ctx context.Context) *Scope {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(scopeKey{}).(*Scope)
	return s
}

// ChildContext explicitly opts an SDK Graph descendant into the shared scope.
func ChildContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, childKey{}, true)
}

// RootContext prevents incidental application calls from inheriting an internal
// scope just because their Go context was passed through another invocation.
func RootContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	child, _ := ctx.Value(childKey{}).(bool)
	if child {
		return SessionContext(context.WithValue(ctx, childKey{}, false))
	}
	return context.WithValue(ctx, scopeKey{}, (*Scope)(nil))
}

// Transfer changes accounting scope without copying SDK-owned immutable storage.
// It consumes the old packet on failure as well as success.
func Transfer(ctx context.Context, done <-chan struct{}, dst *Scope, p *Packet) (*Packet, error) {
	if p.Scope() == dst {
		return p, nil
	}
	defer p.Release()
	if p.Snapshot.Cost() > dst.limits.MaxValueUnits {
		return nil, &value.LimitError{Stage: "transfer", Kind: "value", Allowance: dst.limits.MaxValueUnits}
	}
	if p.Snapshot.Depth() > dst.limits.MaxDepth {
		return nil, &value.LimitError{Stage: "transfer", Kind: "depth", Allowance: int64(dst.limits.MaxDepth)}
	}
	r := dst.NewReservation()
	if err := r.Adjust(ctx, done, p.Snapshot.Cost()); err != nil {
		r.Release()
		return nil, err
	}
	return &Packet{p.Snapshot, r}, nil
}
