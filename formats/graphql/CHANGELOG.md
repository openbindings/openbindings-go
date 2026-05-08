# Changelog

## 0.2.0 (working draft)

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the executor->invoker rename (`BindingExecutor` -> `BindingInvoker`, `ExecuteBinding(...)` -> `InvokeBinding(...)`, `NewExecutor` -> `NewInvoker`, `ExecutionOptions` -> `InvocationOptions`, etc.) propagate to this module's exported surface; see the root `openbindings-go` CHANGELOG for the full rename table. No format-specific behavior changed.

## 0.1.0 -- 2026-03-31

Initial public release.

- GraphQL binding executor: execute queries, mutations, and subscriptions
- GraphQL interface creator: introspect endpoints and generate OBI documents
- Automatic selection set construction with depth-limited recursion and cycle detection
- Subscription support via the `graphql-transport-ws` WebSocket sub-protocol
- Introspection result caching on the executor
- GraphQL type to JSON Schema conversion (scalars, enums, objects, input objects, lists, unions, interfaces)
- Standard auth retry flow (401 -> resolve credentials -> retry once)
- Context store integration with scheme-agnostic key normalization
