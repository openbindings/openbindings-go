# Changelog

## 0.2.0 (working draft)

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the executor->invoker rename (`BindingExecutor` -> `BindingInvoker`, `ExecuteBinding(...)` -> `InvokeBinding(...)`, `NewExecutor` -> `NewInvoker`, `ExecutionOptions` -> `InvocationOptions`, etc.) propagate to this module's exported surface; see the root `openbindings-go` CHANGELOG for the full rename table. No format-specific behavior changed.

## 0.1.0 — 2026-03-31

Initial public release.

- AsyncAPI 3.x binding executor (`asyncapi@^3.0.0`)
- HTTP/SSE execution for receive actions
- HTTP POST execution for send actions
- WebSocket streaming for bidirectional operations (via nhooyr.io/websocket)
- Spec-driven security scheme credential placement (apiKey, http bearer/basic, httpBearer, userPassword)
- Credential fallback when no security schemes defined
- Document caching with thread-safe locking
- Interface creation from AsyncAPI documents with deterministic output
