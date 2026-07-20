# Changelog

## 0.2.0 (working draft)

- **Synthesizer: shared object/input-object types synthesize in full in
  sibling positions** (delete-on-unwind), output parity-pinned with the TS
  package.
- **Invoker doc: retired OBI-T-15 citation replaced** with the current
  authority (location/content composition is binding-specification-defined
  within core §5.4's content-primacy floor).

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the root SDK (executor → invoker rename, `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`, `InvocationOptions` folded into `BindingContext`, `InterfaceClient` removed in favor of codegen-emitted `<Name>Invoker` types, `.Data` event field renamed to `.Output`) propagate to this module's exported surface. See the root `openbindings-go` CHANGELOG for the full table. No format-specific behavior changed.

**Invocation handle migration.** `InvokeBinding` now returns the cardinality-agnostic `Invocation[I, O]` handle (input via `Write`, outputs via `Outputs().Read` to `io.EOF`/terminal) instead of `(<-chan InvocationOutput, error)`. Auth moved off the removed `SecurityMethod`/`ContextStore` path to context (credentials in the binding context; `CONTEXT_REQUIRED` where the source declares requirements). Error codes use the new SCREAMING wire values. See the root CHANGELOG for the full shape.

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
