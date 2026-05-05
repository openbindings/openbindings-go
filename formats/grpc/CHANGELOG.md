# Changelog

## 0.1.1 — 2026-04-20

This release fixes several schema-emission issues that prevented
real-world proto services from round-tripping through OB. OBI schemas
now describe semantic types; wire encoding (JSON number vs string,
proto varint, base64) is treated as an executor concern.

### Fixed

- Well-known proto types (`google.protobuf.Timestamp`, `Duration`,
  `FieldMask`, `Struct`, `Value`, `ListValue`, `Any`, `Empty`, and all
  `*Value` wrappers) now emit their canonical JSON Schema forms per
  the proto3 JSON mapping, instead of being traversed as generic
  messages. Previously, a `Timestamp` field produced a `{seconds, nanos}`
  object in the OBI schema, which the executor's jsonpb unmarshaler
  rejected at request time, making any proto service that used
  well-known types unusable through OB.
- 64-bit integer fields (`int64`, `uint64`, `sint64`, `sfixed64`,
  `fixed64`, and the `Int64Value`/`UInt64Value` wrappers) now emit as
  `{"type":"integer","format":"int64"}` instead of `{"type":"string"}`.
  The schema describes the value's semantic type; downstream codegen
  reads `format:int64` to pick precision-preserving language types
  (TypeScript `string`, Go `int64`, Rust `i64`). `format` is stripped
  during schema-profile normalization, so compatibility remains
  unaffected.
- Bytes fields (scalar `bytes` and `google.protobuf.BytesValue`) now
  emit as `{"type":"string"}` without a `contentEncoding` annotation.
  The v0.1 schema profile rejects `contentEncoding` as outside its
  supported keyword set; emitting it silently produced OBIs that
  failed compatibility checks. The base64 wire encoding is an
  executor-side concern, not a schema contract. The tradeoff: bytes
  and strings are structurally indistinguishable at compat time. This
  is a documented v0.1 limitation.
- Proto `oneof` groups are now represented as `oneOf` constraints in
  the emitted JSON Schema, preserving the "exactly one of" semantics
  that was silently lost when all fields were emitted as independent
  optional properties. Each variant is
  `{"type":"object","properties":{field:...},"required":[field]}`, and
  oneof members are not duplicated into the outer `properties`.
- Response field names now use the proto3 JSON canonical form
  (camelCase) rather than the original proto names (snake_case). The
  executor previously marshaled responses with `OrigName: true`, which
  disagreed with the creator's use of `field.GetJSONName()` and broke
  any field whose proto name contained an underscore: the OBI
  advertised `itemId` while the wire delivered `item_id`, causing
  silent `undefined` on codegen clients.

### Added

- Messages with multiple `oneof` groups emit a `grpc.multi_group_oneof`
  creator warning. The emitted OBI is still valid and executable;
  exclusivity among members cannot be expressed by the v0.1 schema
  profile, so members are emitted as independent optional properties.
  Callers wire the warning sink via the new `CreateInput.OnWarning`
  handler in the core SDK. The warning carries the containing
  message's fully-qualified name and the list of affected oneof group
  names for programmatic handling.

### Known limitations

- Multi-group `oneof` exclusivity is not enforced by the emitted OBI
  (see the `grpc.multi_group_oneof` warning above). A future schema
  profile revision can lift this when `oneOf` inside `allOf` becomes
  supported.
- `oneof` is emitted as exactly-one-of; proto3 permits no member to be
  set, which the JSON Schema form does not express. This matches how
  most APIs intend `oneof` to be used in practice.
- Bytes and strings are structurally indistinguishable in emitted
  schemas. Services whose semantics depend on this distinction should
  carry it in operation descriptions or out-of-band documentation.

## 0.1.0 — 2026-03-31

Initial public release.

- gRPC binding executor (`grpc`) via server reflection
- Unary and server-streaming RPC execution
- Service discovery via gRPC reflection API
- Protobuf-to-JSON Schema conversion for interface creation
- Credential application (bearer, basic, apiKey) as gRPC metadata
- Connection pooling with thread-safe caching
- Deterministic interface output with sorted services and methods
