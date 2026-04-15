# Changelog

## 0.1.0 — 2026-03-31

Initial public release.

- gRPC binding executor (`grpc`) via server reflection
- Unary and server-streaming RPC execution
- Service discovery via gRPC reflection API
- Protobuf-to-JSON Schema conversion for interface creation
- Credential application (bearer, basic, apiKey) as gRPC metadata
- Connection pooling with thread-safe caching
- Deterministic interface output with sorted services and methods
