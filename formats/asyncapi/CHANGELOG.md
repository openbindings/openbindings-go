# Changelog

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
