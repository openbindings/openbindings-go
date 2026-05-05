# Changelog

## 0.2.0

### Changed

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
