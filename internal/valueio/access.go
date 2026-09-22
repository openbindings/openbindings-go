package valueio

import (
	"context"
	"reflect"

	"github.com/openbindings/openbindings-go/internal/value"
)

// Endpoint is available only to SDK implementation packages. Limits are the
// resolved per-value limits every capture, delivery and construction on this
// session uses. Send operations enqueue an already-admitted packet. ClaimOutput
// is exactly the public single-consumer claim; Stop is exactly Cancel.
type Endpoint struct {
	Limits        value.Limits
	CaptureInput  func(context.Context, any) error
	SendInput     func(context.Context, *Packet) error
	ReadInput     func(context.Context) (*Packet, error)
	CaptureOutput func(any) error
	SendOutput    func(*Packet) error
	ClaimOutput   func()
	ReadOutput    func(context.Context) (*Packet, error)
	Stop          func()
	FailValue     func(error)
	owner         any
}

// Access is embedded under a private alias by SDK facades. Its accessor is
// package-private, so it adds no exported operation to the invocation API.
type Access struct{ endpoint *Endpoint }

func NewAccess(owner any, e *Endpoint) Access { e.owner = owner; return Access{e} }
func (a Access) valueEndpoint() *Endpoint     { return a.endpoint }
func From(target any) *Endpoint {
	access, ok := target.(interface{ valueEndpoint() *Endpoint })
	if !ok {
		return nil
	}
	e := access.valueEndpoint()
	if e == nil || reflect.TypeOf(e.owner) != reflect.TypeOf(target) {
		return nil
	}
	// SDK owners are pointers. Inherited access on a foreign wrapper is refused.
	if e.owner != target {
		return nil
	}
	return e
}
