package openbindings

import "context"

// BindingInvoker invokes bindings against format-specific sources.
// Implementations handle a specific binding format (e.g., OpenAPI, gRPC, MCP).
//
// InvokeBinding returns a channel of InvocationOutput. Unary operations send a
// single event and close the channel. Streaming operations send multiple events.
// Callers MUST drain the channel to avoid goroutine leaks.
//
// A concrete invoker may also implement InterfaceCreator; check via type assertion.
type BindingInvoker interface {
	Formats() []FormatInfo
	InvokeBinding(ctx context.Context, in *BindingInvocationInput) (<-chan InvocationOutput, error)
}

// InterfaceCreator creates OpenBindings interfaces from format-specific sources.
// Independent of BindingInvoker — an implementation may provide one, the other, or both.
type InterfaceCreator interface {
	Formats() []FormatInfo
	CreateInterface(ctx context.Context, in *CreateInput) (*Interface, error)
}

// SourceInspector inspects format-specific sources and returns bindable
// targets that tooling can frame as OpenBindings operations.
type SourceInspector interface {
	Formats() []FormatInfo
	InspectSource(ctx context.Context, source *Source) (*SourceInspection, error)
}
