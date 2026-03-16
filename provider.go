package openbindings

import "context"

// BindingExecutor executes bindings against format-specific sources.
// Implementations handle a specific binding format (e.g., OpenAPI, gRPC, MCP).
//
// A concrete provider may also implement InterfaceCreator and/or BindingStreamHandler;
// check via type assertion.
type BindingExecutor interface {
	Formats() []string
	ExecuteBinding(ctx context.Context, in *BindingExecutionInput) (*ExecuteOutput, error)
}

// InterfaceCreator creates OpenBindings interfaces from format-specific sources.
// Independent of BindingExecutor — a provider may implement one, the other, or both.
type InterfaceCreator interface {
	Formats() []string
	CreateInterface(ctx context.Context, in *CreateInput) (*Interface, error)
}

// BindingStreamHandler subscribes to streaming binding operations (kind: "event").
// Optional capability — check via type assertion on a concrete provider.
type BindingStreamHandler interface {
	Formats() []string
	SubscribeBinding(ctx context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error)
}
