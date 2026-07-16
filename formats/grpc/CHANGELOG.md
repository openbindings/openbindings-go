# Changelog

## 0.2.0 (working draft)

### Changed

- **Conformance to the published `openbindings.grpc@1` binding
  specification.** The invoker now implements the spec's rules end to end;
  breaking changes from the pre-spec behavior:
  - **Input mapping (GRPC-P-03):** unknown input fields are refused loudly
    before dispatch (previously silently discarded); the accepted input
    shape is the request type's canonical ProtoJSON form, so a
    `google.protobuf.Duration`-typed request takes a JSON string (any
    canonical form, not only objects); a close-without-write is the §9.1
    ABSENT input value and dispatches the empty request message
    (`ERR_MISSING_INPUT` is no longer produced by this module).
  - **Dial address grammar (GRPC-D-02):** `location` (and the target
    configuration point) must be one of the three port-explicit forms —
    `host:port` (TLS iff port 443), `grpc://host:port` (plaintext),
    `grpcs://host:port` (TLS) — with bracketed IPv6 literals. The
    pre-spec `https://` affordance is cut (one spelling per meaning), no
    port is defaulted, and path/query/fragment/userinfo components are
    refused.
  - **Configuration points (§9.3):** `context.configuration.target`
    replaces the source's location entirely; `context.configuration
    .transport` (`"plaintext"`, `"tls"`, or `{ca, clientCert, clientKey,
    serverName}`) replaces the address-form transport determination
    entirely — winning even against an explicit scheme — and outranks the
    invoker-level `WithTransportCredentials` identity. The former
    `metadata.baseURL` fallback is renamed into the target point.
    Connections are now cached per (target, transport identity) pair.
  - **Descriptor-set content (GRPC-D-01):** a source's object `content` is
    accepted as a `google.protobuf.FileDescriptorSet` in canonical
    ProtoJSON — unknown members and bracket-keyed extension members refused
    loudly, self-contained closure required. String `content` remains
    single-file `.proto` source text; its `google/protobuf/*` imports now
    resolve from the processor's bundled copies and any other import is
    refused loudly at load.
  - **Accepted schema range (§3):** the bound method's transitive message
    closure is validated at descriptor load — proto2 groups, required
    presence (proto2 `required` / editions `LEGACY_REQUIRED`),
    `message_encoding = DELIMITED`, and editions files resolving
    `json_format != ALLOW` are refused pre-dispatch; files outside every
    bound closure are inert carriage.
  - **Reflection (GRPC-P-01):** `grpc.reflection.v1` is consulted first,
    falling back to `v1alpha` iff the v1 stream fails `UNIMPLEMENTED`; any
    other status is the source's failure (the previous client also fell
    back on `UNAVAILABLE`).
  - **Metadata placement (GRPC-P-07):** context header keys that violate
    the gRPC metadata name grammar or use the reserved `grpc-` prefix are
    UNPLACEABLE — surfaced as a loud pre-dispatch refusal, never normalized
    or case-folded into place (previously keys were silently lowercased).
  - **Interaction kinds (GRPC-P-04):** client-streaming and bidirectional
    methods remain uncovered and are refused pre-dispatch as this
    implementation's declared limitation, with messages citing the rule.
  - Conformance tests keyed to the rule identifiers live in
    `conformance_test.go`.

- **Migrated to the rewritten invoker core.** `InvokeBinding` now takes
  `*openbindings.BindingInvocationArgs` and synchronously returns the
  cardinality-agnostic `openbindings.Invocation[any, any]` handle instead of
  `(<-chan InvocationOutput, error)`. Input flows through the handle
  (`Write` one request message; methods whose request message has no fields
  dispatch without one), outputs flow through `Outputs()` (one output for
  unary, per-message for server-streaming, with `EmitOutput` backpressure
  flow-controlling the gRPC stream), and gRPC leading/trailing metadata maps
  natively onto `Header(ctx)`/`Trailer()`. Error codes use the new SCREAMING
  wire values; gRPC statuses map to `ERR_AUTH_REQUIRED` (`Unauthenticated`),
  `ERR_PERMISSION_DENIED`, `ERR_CONNECT_FAILED` (`Unavailable`), and
  `ERR_TIMEOUT` (`DeadlineExceeded`), with the gRPC code and any status
  details carried in the error's `Details`. A close-without-write on a method
  that requires input is `ERR_MISSING_INPUT`; a missing server address is
  `ERR_SOURCE_CONFIG_ERROR`. Cancelling the handle (or the invocation
  context) tears down the underlying RPC stream.

- **Renamed binding "executor" terminology to "invoker"** to track the spec 0.2.0 rename in `openbindings-go`. The module's exported types and methods follow the same pattern (`Executor` -> `Invoker`, `ExecuteBinding(...)` -> `InvokeBinding(...)`, etc.). See the root `openbindings-go` CHANGELOG for the full rename table.

- **Migrated to protobuf v2.** Direct dependencies on `github.com/jhump/protoreflect` (v1) and `github.com/golang/protobuf` are gone. The module now consumes `github.com/jhump/protoreflect/v2/grpcdynamic`, `github.com/jhump/protoreflect/v2/grpcreflect`, `google.golang.org/protobuf/types/dynamicpb`, `google.golang.org/protobuf/encoding/protojson`, and `google.golang.org/protobuf/reflect/protoreflect` directly. The reflection client's `ResolveService` shorthand was replaced with `FileContainingSymbol` plus a small in-package walker. Source-info comment extraction moved from per-method `GetSourceInfo()` to file-level `SourceLocations().ByDescriptor(method)`. Behavior is preserved across the change; the bufconn integration suite passes against the same fixtures.

### Removed

- **The store/security/callbacks paths.** Credentials now arrive solely via
  `BindingInvocationArgs.Context` (`bearerToken`, `apiKey`, `basic`,
  `headers`); the post-hoc `ResolveSecurity` auth retry and the
  `ContextStore` enrichment inside the invoker are gone. Credential
  resolution happens above the binding (operation-layer context
  negotiation).

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
