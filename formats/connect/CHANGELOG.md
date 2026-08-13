# Changelog

## 0.2.0 (working draft)

### Added

- **Configurable delivery-unit bound**: the unary response body and each
  streaming envelope payload honor
  `BindingInvocationArgs.MaxDeliveryUnitBytes` (default
  `openbindings.DefaultMaxDeliveryUnitBytes`, 10 MiB — the previous fixed
  cap). Overflow error code is unchanged. The streaming dispatch path's
  below-bridge HTTP error-body read stays fixed and is not a delivery unit.

> A 0.1.1 patch release was prepared 2026-04 but never tagged or published; its entries are folded into this section.

### Changed

- **apiKey rides the consumer-named header per connect@1 §9.6.** The
  grpc-transcribed fixed `Authorization: ApiKey` placement is gone; bearer and
  apiKey coexist; a bare apiKey with no consumer-named header refuses
  pre-dispatch with `ERR_SOURCE_CONFIG_ERROR` (mirroring grpc's
  unplaceable-credential surfacing).

- **Synthesizer: a type reused in sibling positions synthesizes in full**
  (delete-on-unwind visited tracking); true cycles keep their placeholder.

- **Connect failures now follow the binding specification's structural
  unsuccessful-completion rule.** Native Connect codes and transport evidence
  remain below the abstract invocation rather than defining a universal retry
  taxonomy.
- **Alignment with the unreleased first `openbindings.connect@1` binding
  specification.** The invoker now implements the spec's rules end to end
  (connect@1 incorporates `openbindings.grpc@1` as its schema layer by
  exact-identifier citation; the shared behaviors match `formats/grpc`).
  Breaking changes from the pre-spec behavior:
  - **Input mapping (CONN-P-02, via GRPC-P-03):** unknown input fields are
    refused loudly before dispatch (previously silently discarded); the
    accepted input shape is the request type's canonical ProtoJSON form,
    so a `google.protobuf.Duration`-typed request takes a JSON string (any
    canonical form, not only objects); a close-without-write is the ABSENT
    input value and dispatches the empty request message
    (`ERR_MISSING_INPUT` is no longer produced by this module).
  - **Decode (CONN-P-02, via GRPC-P-05):** schema-mode output values are
    now rendered through the response descriptor by the canonical JSON
    mapping (64-bit integers render as JSON strings, matching
    `formats/grpc`); a response that fails to unmarshal against its
    descriptor is a loud `ERR_RESPONSE_ERROR`, never a passed-through
    value. Unknown response members are tolerated and dropped, matching
    the incorporated layer's wire behavior.
  - **Base URL grammar (CONN-D-02):** `location` (and the target
    configuration point) must be an absolute `http`/`https` base URL —
    optional path prefix with NO trailing `/`; query, fragment, and
    userinfo components refused. The request URL is the base URL
    string-concatenated with `/<service>/<method>`, preserving path
    prefixes.
  - **Target configuration point (§9.1):** `context.configuration.target`
    replaces the source's location entirely, in the same §4 form; the
    former `metadata.baseURL` fallback is renamed into the ruled point.
    Nothing else is configurable.
  - **Content carriages (CONN-D-01, via GRPC-D-01):** a source's object
    `content` is accepted as a `google.protobuf.FileDescriptorSet` in
    canonical ProtoJSON — unknown members and bracket-keyed extension
    members refused loudly (pins carry option-stripped descriptors),
    self-contained closure required. String `content`'s
    `google/protobuf/*` imports now resolve from the processor's bundled
    copies, and any other import is refused loudly at load.
  - **Accepted schema range (openbindings.grpc@1 §3, via CONN-D-01):** the
    bound method's transitive message closure is validated at descriptor
    load (`schemarange.go`) — proto2 groups, required presence,
    `message_encoding = DELIMITED`, and editions `json_format != ALLOW`
    refused pre-dispatch; out-of-closure files are inert carriage.
  - **Modes (CONN-P-01/CONN-P-03/CONN-P-04):** the two modes are
    discriminated by `content` presence. Descriptorless mode is unary-only
    BY DEFINITION with verbatim JSON values — absent input sends `{}`, an
    explicit `null` rides as `null`, an empty 200 body yields `null`, and
    a 200 body that fails to parse as JSON (or a non-`application/json`
    content type) is a loud protocol error, never a string. There is no
    "unary fallback": unary framing is the mode. Ref resolution failures
    in schema mode now surface as `ERR_REF_NOT_FOUND` (byte-exact,
    CONN-D-03).
  - **Classification (CONN-P-06):** unary success is final-status 200 and
    only 200 (previously any status below 400 decoded as success);
    streaming success requires riding 200 AND an error-free END_STREAM
    envelope — a stream that ends without END_STREAM is now a loud
    failure. Emitted values stand on late failure.
  - **Framing (CONN-P-05):** every dispatch is a POST (the protocol's GET
    lane is excluded from revision 1 by definition) with
    `Connect-Protocol-Version: 1` on every request; streaming requests
    advertise only the encodings the processor implements
    (`Connect-Accept-Encoding: identity`), and a compressed envelope from
    the server is refused as a negotiation violation. The 10 MiB response
    cap and 10-redirect limit remain implementation policy, outside the
    specification.
  - Conformance tests keyed to the rule identifiers live in
    `conformance_test.go`.

- **Invocation handle migration.** `InvokeBinding` now returns the cardinality-agnostic `Invocation[I, O]` handle (input via `Write`, outputs via `Outputs().Read` to `io.EOF`/terminal) instead of `(<-chan InvocationOutput, error)`. Auth moved off the removed `SecurityMethod`/`ContextStore` path to context (credentials in the binding context; `CONTEXT_REQUIRED` where the source declares requirements). Error codes use the new SCREAMING wire values. See the root CHANGELOG for the full shape.

- **Renamed binding "executor" terminology to "invoker"** to track the spec 0.2.0 rename in `openbindings-go`. See the root `openbindings-go` CHANGELOG for the full rename table.

- **Migrated to protobuf v2 + protocompile.** Direct dependencies on `github.com/jhump/protoreflect` (v1) and `github.com/golang/protobuf` are gone. Proto file parsing moved from `jhump/protoreflect/desc/protoparse` to `github.com/bufbuild/protocompile`, the v2-native successor maintained by Buf. Dynamic message construction now uses `google.golang.org/protobuf/types/dynamicpb`; JSON marshaling uses `google.golang.org/protobuf/encoding/protojson`. The unary and server-streaming integration tests pass against the same fixtures.

### Fixed

- Binding refs now enforce CONN-D-03's exactly-one-`/`, byte-exact grammar
  before resolution. The parser no longer trims whitespace or lets an extra
  separator survive until schema resolution.

- **Non-2xx streaming error bodies read the full cap**: an off-by-one
  truncated a cap-sized Connect error body into invalid JSON, misclassifying
  the refusal.
- Bytes fields (`bytes`) now emit as `{"type":"string"}` without a
  `contentEncoding` annotation. The v0.1 schema profile rejects
  `contentEncoding` as outside its supported keyword set; emitting it
  silently produced OBIs that failed compatibility checks. The base64
  wire encoding is an invoker-side concern, not a schema contract.
- Request field names now use the proto3 JSON canonical form
  (camelCase) rather than the original proto names (snake_case). The
  executor previously serialized requests with `OrigName: true`, which
  disagreed with the synthesizer's use of `field.GetJSONName()` and broke
  any field whose proto name contained an underscore.

## 0.1.0

Initial public release.

- Connect binding executor: execute unary RPCs via HTTP POST with JSON
- Interface creator: parse .proto files and inline protobuf definitions
- Same ref convention as gRPC (package.Service/Method)
- Proto-aware input marshaling when descriptors are available
- Standard auth retry flow (401 -> resolve credentials -> retry once)
- Context store integration with scheme-agnostic key normalization
