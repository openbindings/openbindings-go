# Changelog

## 0.2.0

### Changed

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
