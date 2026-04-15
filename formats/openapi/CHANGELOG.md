# Changelog

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
