# Changelog

## 0.2.0 (working draft)

### Added

- **Standalone OpenAPI artifact engine integration.** `Invoker` now adapts
  Core invocations to `github.com/openbindings/openapi-client/go`; the former
  local HTTP/SSE execution loop has been retired. `Runtime`, `RuntimeSource`,
  and `RuntimeInvocationArgs` remain as compatibility façades, while new
  OpenAPI-only applications can use the standalone native client without an
  OpenBindings dependency.

- **Current `openbindings.openapi@1` preserves exact schema-omitted OAS 3.0
  non-JSON bytes without changing Core.** Request and response octets cross the
  protocol-independent boundary as canonical Base64. Media ranges and
  artifact-defined codecs remain unchanged. This is part of the unreleased
  first `@1` candidate.

- **Current `openbindings.openapi@1` preserves declaration-complex exact JSON
  bodies without changing Core.** Top-level combinators, conditionals,
  dependent schemas, and explicit `unevaluatedProperties` stay under one
  protocol-neutral application property and a binding-private whole-body
  route. The binding does not choose a schema branch.

- **`openbindings.openapi@1` preserves explicitly dynamic object
  bodies without changing Core.** Explicit `additionalProperties` and
  `patternProperties` schemas stay under one protocol-neutral application
  property and a binding-private whole-body route. Runtime form and multipart
  members resolve their effective exact, pattern, additional-property, and
  `allOf` schemas.

- **`openbindings.openapi@1` added declaration-led response range and
  raw-byte carriage without changing Core.** The actual concrete response
  media selects the most-specific exact or wildcard declaration. Strict JSON,
  text/SSE, and artifact-authorized raw-byte lanes produce protocol-independent
  application values; exact bytes use canonical Base64.

- **Current `openbindings.openapi@1` adds declaration-led raw-octet and range
  request carriage without changing Core.** OAS 3.0 concrete binary schemas
  and OAS 3.1 schema-omitted concrete media use a canonical Base64 JSON
  boundary and emit exact bytes; OAS 3.1 `contentEncoding` strings ride
  unchanged. Configured concrete request media selects exact, type-range, or
  universal-range declarations by specificity and declared parameters, never
  emits a wildcard `Content-Type`, and refuses unsupported lane/schema pairs.
  Required range-only bodies challenge before input consumption, and synthesis
  coverage records `configuration.requestMedia` at alternative and applicable
  target scope. Form and multipart carriage follows each accepted OAS
  edition's own rules, including the older 3.0.0–3.0.3 urlencoded
  defaults; OAS 3.0 binary parts retain author-declared non-default media
  types; and underdefined nested or declaration-free form lanes fail closed.

- **Response completion retains exact native failure evidence.**
  Empty non-2xx bodies remain distinguishable from uncaptured bodies, and SSE
  invalid UTF-8 follows WHATWG maximal-subpart replacement rather than a
  library-specific replacement grouping. Both remain diagnostic binding facts,
  never operation output fields.

- **`openbindings.openapi@1` preserves same-named application inputs.**
  Synthesis retains unique author names, assigns deterministic neutral suffixes
  only where declarations collide, and carries their exact OpenAPI
  name/location correspondence in a binding-private core `inputTransform`.
  Invocation validates and consumes that routed value before dispatch; no HTTP
  identity enters the operation schema. Native differential tests cover
  path/query/body collisions, and the shared portable corpus covers the new
  processor and synthesis behavior.

- **Degenerate media/schema combinations refuse pre-dispatch**
  (`openbindings.openapi@1` §9.2, amended 2026-07-21): a request-media
  selection landing on `multipart/form-data` or
  `application/x-www-form-urlencoded` while the declared body schema does
  not flatten (§9.1's declaration-only determination — no `properties` and
  no explicit `object` type), or on `text/plain` while it does, has no
  OAS-defined wire form and now refuses loudly before dispatch
  (`ERR_SOURCE_CONFIG_ERROR`, zero I/O) instead of inventing carriage
  (previously an invented multipart part or urlencoded field named `body`
  rode the wire, and the text lane misfired the §9.1 unmatched-field
  refusal against the contract's own flattened fields). Reachable only for
  operations declaring no JSON-family request media — a co-declared JSON
  media type is selected first and carries any shape. Synthesis emits the
  new **`openapi.media_schema_mismatch`** warning
  (`SynthesizeInput.OnWarning`) when the produced contract's only declared
  request media cannot carry it, so authors hear at synthesis time what a
  conformant invoker refuses at dispatch. Mirrors the TS SDK
  (byte-identical refusal messages and warning).

- **Configurable delivery-unit bound**: the unary response body honors
  `BindingInvocationArgs.MaxDeliveryUnitBytes` (default
  `openbindings.DefaultMaxDeliveryUnitBytes`, 10 MiB — the previous fixed
  cap). Overflow error identity is unchanged (`ERR_RESPONSE_ERROR`, same
  message template, dynamic value). The SSE lane now enforces the same
  bound per event — one event is one delivery unit, `ERR_RESPONSE_ERROR`
  `SSE event exceeds N byte limit` (asyncapi parity); the bound is
  per-emission, never cumulative, so a long-lived stream legitimately
  delivers more than the bound in total (previously the lane had no
  per-event bound at all, only the line-scanner guard). The SSE
  line-scanner guard stays fixed (parser internal, not a delivery unit).

### Fixed

- **Security alternatives, credential ownership, and synthesis soundness now
  follow the first OpenAPI candidate exactly.** Security Requirement Objects remain an
  OR of complete AND sets; a selected request never combines credentials from
  separate alternatives or sends undeclared fallback credentials. Effective
  `Host` and `Content-Length` parameters and ambiguous raw/structured cookie
  ownership refuse before dispatch, while a later complete collision-free
  security alternative may still be used. Content-form parameters outside the
  candidate's JSON/text carriage are excluded during synthesis with explicit
  exhaustive coverage rather than becoming statically uninvocable operations.

- **Schema projection is now direction- and edition-aware.** Request schemas
  omit `readOnly` properties, response schemas omit `writeOnly` properties,
  and required lists are repaired through nested, composed, map, and recursive
  graphs. Raw OpenAPI resources are normalized at the loader boundary so 3.1
  Schema Object `$ref` siblings compose before kin-openapi resolution can
  overlay them, while 3.0 Reference Object siblings retain strict ignore
  semantics and examples/extensions remain opaque data. Legal 3.1 Reference
  Object descriptions remain site-local rather than mutating a shared target;
  a per-load synthesis sidecar restores authored null, empty, zero, false, and
  `x-*` Schema Object values erased by the typed parser without restoring
  ignored 3.0 `$ref` siblings or changing invocation; and unsupported custom
  schema dialects refuse portable synthesis without globally disabling
  artifact-native invocation or reference listing.

- **A typeless request-body schema rides the synthetic `body` property on
  the wire, matching the candidate contract** (`openbindings.openapi@1`
  §9.1's declaration-only object determination: a body schema is object
  iff it declares `properties` or an explicit `object` type). The
  synthesizer wrapped a typeless body (a bare `{}` or a description-only
  schema) under the synthetic `body` property while the invoker treated it
  as flattened, so a caller following the candidate contract got
  `{"body": X}` on the wire instead of `X` — and the §9.1 unmatched-field
  refusal for non-object bodies did not fire. Both sites now route through
  one shared predicate (`bodySchemaFlattens`), so the contract and the
  wire cannot diverge. Red-proven in the mirrored §9.1 conformance tests;
  behavior matches the TS SDK, where the same fix also makes a 3.1
  two-element `type: ["object", "null"]` body (not an *explicit* object
  type) synthetic at the invoker.

**Alignment with the unreleased first `openbindings.openapi@1` candidate.** The invoker implements the candidate rules end to end; earlier package behavior that diverged from them changed:

- **Input mapping (OAPI-P-02/P-03).** Parameter serialization follows the OAS `style`/`explode`/`allowReserved` rules wholesale (matrix/label/simple, form/spaceDelimited/pipeDelimited/deepObject, per-location defaults, `content`-form parameters). Cross-location same-name declarations refuse as unflattenable; unmatched input fields refuse loudly when no request body is declared (previously silently sent as a body); a missing declared path parameter refuses pre-dispatch; non-object body schemas ride the synthetic `body` property, unwrapped at the wire.
- **Request/response media (OAPI-P-04).** Request media selection follows the declared preference order (exact JSON → least `+json` → multipart → urlencoded → `text/plain` for string bodies) with pre-dispatch refusal of out-of-family-only declarations. Multipart parts are binary-signaled per edition (3.0 `format: binary`; 3.1 `contentMediaType`/`contentEncoding`), with caller strings decoded per declared `contentEncoding` or Base64 (previously required Go `[]byte`, which still passes through raw as an in-process convenience). urlencoded bodies serialize per the OAS `encoding` rules. The `Accept` header advertises the declared success-response media (previously a fixed `application/json, text/event-stream`).
- **Servers (OAPI-P-05).** The OAS effective server list (operation → path item → document → implied `/`) with variable substitution; `context.configuration.server` is the named configuration point (entry selection by url/index, variable values, outright `baseUrl`); relative server URLs resolve against the source's location per RFC 3986. `metadata.baseURL` still works, below the configuration point.
- **Ref (OAPI-D-03).** `#/paths/<escaped-path>/<method>` is enforced exactly: lowercase method (an uppercase method is refused, never case-folded), `#/paths/` prefix required, single escaped path token.
- **Interaction shape (OAPI-P-06).** Streaming capability is static — declared `text/event-stream` on a success response — and the response's `Content-Type` framing selects among declared shapes; an undeclared event-stream response is an `ERR_PROTOCOL` failure (previously any 2xx SSE response silently streamed). SSE extraction is WHATWG-exact: empty-data/fields-only events emit nothing, incomplete final events are discarded, CR/CRLF line endings and the leading BOM are handled, `id` follows lastEventId semantics.
- **Decode (OAPI-P-07).** The text lane honors the `charset` parameter (UTF-8 default) and refuses invalid sequences loudly.
- **Channel assembly (OAPI-P-10).** Declared cookie parameters and cookie-riding credentials merge into one `Cookie` header (parameters in declaration order, credentials appended); credential/parameter name collisions on a channel refuse pre-dispatch.
- **Loading (OAPI-P-01, §3–§6).** The artifact's `openapi` field discriminates the exact accepted 3.0.0–3.0.4 and 3.1.0–3.1.2 editions (Swagger 2.0 and every other value refuse loudly); duplicate YAML mapping keys refuse; embedded content without a location must be self-contained (relative external `$ref`s fail with a readable error instead of resolving against the process working directory).

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the root SDK (executor → invoker rename, `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`, `InvocationOptions` folded into `BindingContext`, `InterfaceClient` removed in favor of codegen-emitted `<Name>Invoker` types, `.Data` event field renamed to `.Output`) propagate to this module's exported surface. See the root `openbindings-go` CHANGELOG for the full table. No format-specific behavior changed.

**Invocation handle migration.** `InvokeBinding` now returns the cardinality-agnostic `Invocation[I, O]` handle (input via `Write`, outputs via `Outputs().Read` to `io.EOF`/terminal) instead of `(<-chan InvocationOutput, error)`. Auth moved off the removed `SecurityMethod`/`ContextStore` path to context (credentials in the binding context; `CONTEXT_REQUIRED` where the source declares requirements). Error codes use the new SCREAMING wire values. See the root CHANGELOG for the full shape.

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
