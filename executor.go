package openbindings

import "context"

// BindingExecutor executes bindings against format-specific sources.
// Implementations handle a specific binding format (e.g., OpenAPI, gRPC, MCP).
//
// ExecuteBinding returns a channel of StreamEvent. Unary operations send a
// single event and close the channel. Streaming operations send multiple events.
// Callers MUST drain the channel to avoid goroutine leaks.
//
// A concrete executor may also implement InterfaceCreator; check via type assertion.
type BindingExecutor interface {
	Formats() []FormatInfo
	ExecuteBinding(ctx context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error)
}

// InterfaceCreator creates OpenBindings interfaces from format-specific sources.
// Independent of BindingExecutor — an implementation may provide one, the other, or both.
type InterfaceCreator interface {
	Formats() []FormatInfo
	CreateInterface(ctx context.Context, in *CreateInput) (*Interface, error)
}

// RefLister is an optional interface that InterfaceCreators can implement to
// enumerate the bindable refs available in a source. Check via type assertion:
//
//	if lister, ok := creator.(RefLister); ok { ... }
//
// Not all creators need to implement this. When absent, tooling should fall
// back to manual ref entry.
type RefLister interface {
	ListBindableRefs(ctx context.Context, source *Source) (*ListRefsResult, error)
}
