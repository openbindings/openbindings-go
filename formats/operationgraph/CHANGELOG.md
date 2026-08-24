# Changelog

## 0.2.0 (working draft)

### Changed

- **Breaking**: the project-wide binding-target rename (`bindings[*].ref` →
  `bindings[*].selector`): bindings ride
  `invoke.BindingInvocationArgs.Selector`, and refusals use
  `ERR_INVALID_SELECTOR` / `ERR_SELECTOR_NOT_FOUND`. The OG-V-18 `$ref`
  prohibition inside embedded schemas is untouched.

- **Pre-execution refusal (OG-V-11)**: an operation-node graph invoked without
  an interface fails `ERR_VALIDATION_FAILED` instead of reaching the operation
  invoker with no interface.

- **Transparency rewrite.** The engine now implements the rewritten
  `openbindings.operation-graph@0.2.0` spec, governed by the identity law
  (`input → operation(y) → output` is observationally indistinguishable from
  direct invocation):
  - `operation` is the **conduit**: one held invocation per graph invocation,
    fed every arriving event in order, its input closed on upstream
    completion. A graph invoked with zero writes drives a no-input invocation
    (the old `nil` injection is gone). Conduits cannot sit on cycles (OG-V-10).
  - **`each`** is the per-event node (the old `operation` behavior): one
    single-write invocation per arriving event; `maxIterations` moved here and
    bounds cycles per event lineage (OG-V-09).
  - **Caller input is a stream.** Every write becomes one event at the input
    node and roots a lineage; `$input` is the lineage root, undefined at
    merges whose contributors disagree. The graph **back-closes** the
    caller-facing input side when every direct consumer of the input node is
    a non-accepting conduit; a write to a non-accepting conduit is a
    `WRITE_REJECTED` per-event error.
  - **Error model per the identity law.** Per-event failures route via
    `onError` (error events are `{error, event}`, with spec identifiers
    `TIMEOUT_EXCEEDED`, `WRITE_REJECTED`, `MAP_NOT_ARRAY`,
    `TRANSFORM_UNDEFINED`) or drop; an unhandled conduit terminal error
    terminates the graph invocation with the inner error verbatim. `exit`
    with `error: true` surfaces the event as the terminal `Details`.
  - **Semantics corrections**: combine waits for readiness (no null-bearing
    partials), lineage merges element-wise max through buffer/combine/conduit,
    buffer flush precedence is add-then-limit-then-until/through, an undefined
    transform result fails the node (`openbindings.ErrTransformUndefined`
    sentinel), and cyclic/error-route completion resolves via a quiescence
    pass.
  - **Addressing and versioning**: `ref` is a required JSON Pointer fragment
    against an unconstrained host document (bare graph keys rejected with
    `ERR_INVALID_REF`); each graph declares its own
    `openbindings.operation-graph` version, refused per OG-T-02
    (`ERR_UNSUPPORTED_FORMAT_VERSION`); validation implements OG-V-01..17
    with per-type field whitelists.
  - **Structured validation**: `ValidateGraph` returns
    `[]GraphValidationIssue` (`{Rule, Message, NodeKeys}`, mirroring the TS
    SDK) so editors can attribute failures to nodes; `Validate` remains the
    error-returning OG-T-01 form.
  - **Conformance**: the test suite runs the spec repository's corpus
    unmodified (19 execution fixtures including the identity-law suite, plus
    the OG-V validation fixtures) through the real `OperationInvoker` against
    a mock binding invoker.
- **Invocation handle migration.** `InvokeBinding` now returns the
  cardinality-agnostic `Invocation[I, O]` handle, and the engine drives
  sub-operations through `OperationInvoker.Invoke`'s handle (Write the node
  input, Read outputs to `io.EOF`/terminal) with cancellation chained through
  the sub-call context. The `InvocationOutput` envelope is gone; graph events
  are bare outputs and terminal failures use the SCREAMING error codes
  (`ERR_OPERATION_GRAPH_EXIT`, `ERR_EVENT_LIMIT_EXCEEDED`, `ERR_MAP_NOT_ARRAY`).
  See the root CHANGELOG for the full shape.
- **Breaking:** Transforms are now plain JSONata expression strings instead of
  `{"type": "jsonata", "expression": "..."}` objects, matching the
  operation-graph format spec v0.2.0.
- Removed `TransformDef` struct; `Node.Transform` is now `*string`.
- Updated `FormatToken` to `openbindings.operation-graph@0.2.0`.

## 0.1.0

Initial release.

- Binding executor for `openbindings.operation-graph@0.1.0`
- Document parsing and validation against all spec rules
- Event-driven execution engine with in-flight tracking
- Node types: input, output, operation, buffer, filter, transform, map, combine, exit
- onError routing on all nodes with silent drop default
- maxIterations safety bounds for cyclic graphs
- Cycle detection via Tarjan's algorithm
- Timeout support on operation nodes
