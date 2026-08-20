// Package invoke is the OpenBindings invocation runtime: the realization
// of the published binding-invoker and operation-invoker interfaces and
// the invocation pattern built on them.
//
// An OperationInvoker dispatches operations through registered binding
// invokers. Every operation returns a cardinality-agnostic Invocation[I, O]
// handle — one shape for unary, server-streaming, client-streaming, and
// bidirectional bindings. Creation is inert (no I/O until the handle is
// driven), wiring failures surface as already-errored handles rather than
// panics, and missing credentials surface as CONTEXT_REQUIRED terminal
// errors raised before any side effect:
//
//	opInv := invoke.NewOperationInvoker(openapi.NewInvoker())
//	call := invoke.Invoke(ctx, opInv, iface,
//	    invoke.NewOperationSignature[any, any]("listItems"))
//	_ = call.Write(ctx, map[string]any{"limit": 10})
//	out, err := invoke.Single(ctx, call.Outputs())
//
// InvokeHooks is the consumer seam for wire questions a source artifact
// leaves open; TransformEvaluator is the seam for JSONata binding
// transforms (the SDK bundles no JSONata runtime; the README carries the
// adapter recipe). ContextStore and StoreContextResolver are the optional
// storage seam for binding invocation context.
package invoke
