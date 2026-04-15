# Changelog

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
