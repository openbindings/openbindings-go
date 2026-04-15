# Changelog

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
