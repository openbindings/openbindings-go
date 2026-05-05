# Changelog

## 0.1.1 — 2026-04-20

### Fixed

- Bytes fields (`bytes`) now emit as `{"type":"string"}` without a
  `contentEncoding` annotation. The v0.1 schema profile rejects
  `contentEncoding` as outside its supported keyword set; emitting it
  silently produced OBIs that failed compatibility checks. The base64
  wire encoding is an executor-side concern, not a schema contract.
- Request field names now use the proto3 JSON canonical form
  (camelCase) rather than the original proto names (snake_case). The
  executor previously serialized requests with `OrigName: true`, which
  disagreed with the creator's use of `field.GetJSONName()` and broke
  any field whose proto name contained an underscore.

### Known limitations

- Well-known proto types, proto `oneof` groups, and 64-bit integer
  precision are not handled with the same fidelity as in the `grpc`
  format. Callers using Connect servers should prefer the `grpc`
  format binding when available until Connect catches up in a future
  release.

## 0.1.0

Initial public release.

- Connect binding executor: execute unary RPCs via HTTP POST with JSON
- Interface creator: parse .proto files and inline protobuf definitions
- Same ref convention as gRPC (package.Service/Method)
- Proto-aware input marshaling when descriptors are available
- Standard auth retry flow (401 -> resolve credentials -> retry once)
- Context store integration with scheme-agnostic key normalization
