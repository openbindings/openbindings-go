# Changelog

## 0.2.0 (working draft)

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the executor->invoker rename (`BindingExecutor` -> `BindingInvoker`, `ExecuteBinding(...)` -> `InvokeBinding(...)`, `NewExecutor` -> `NewInvoker`, `ExecutionOptions` -> `InvocationOptions`, etc.) propagate to this module's exported surface; see the root `openbindings-go` CHANGELOG for the full rename table. No format-specific behavior changed.

## 0.1.0 — 2026-03-31

Initial public release.

- OpenAPI 3.x binding executor (`openapi@^3.0.0`)
- HTTP request construction from OpenAPI specs (path, query, header, body parameter routing)
- Security scheme-driven credential application (bearer, basic, apiKey with spec-declared placement)
- Credential fallback when no security schemes defined
- Document caching with thread-safe read/write locking
- Interface creation from OpenAPI documents (operations, bindings, schemas, refs)
- JSON Pointer ref generation and parsing (RFC 6901)
- Base URL resolution from spec servers with relative URL support
