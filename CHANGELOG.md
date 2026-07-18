# Changelog

## 0.2.0 (working draft)

### Changed

- **`IsSupportedVersion` now answers OBI-T-04 acceptance (patch-lenient within a
  supported minor line), matching `Validate`/`ParseDocument`; previously it was
  the strict tested-range check.** A 0.2.0 SDK now reports `true` for `0.2.1`,
  `0.2.99`, etc. — the versions `Validate`/`ParseDocument` actually process —
  and continues to report `false` for a different major, a pre-1.0 different
  minor, and unsupported prereleases. The oracle now shares the single refusal
  predicate the validation paths use, so it cannot drift from them.
  `MinSupportedVersion`/`MaxTestedVersion`/`SupportedRange()` are unchanged and
  remain the maintainer-*tested* range — a distinct, narrower notion (a version
  can be accepted without being inside the tested range).

- **New transient error code `ErrCodeUnavailable` (`ERR_UNAVAILABLE`) and the
  transport status→code tables realigned to the binding-invoker contract.**
  The service was reached but refused the request as retryable — distinct from
  `ErrCodeConnectFailed`, which never reached a server. `HTTPErrorCode` now
  implements the full contract table: `429`/`502`/`503` → `ERR_UNAVAILABLE`,
  `408`/`504` → `ERR_TIMEOUT`, `401`/`403` → the auth codes, and every other
  4xx/5xx → `ERR_EXECUTION_FAILED` (previously only 401/403 were mapped and
  everything else defaulted to `ERR_EXECUTION_FAILED`, overclaiming vs. the
  contract). `effects` is per-status: `429`/`503` are `none` (refused before
  execution), `502` is left possible. The gRPC invoker maps
  `UNAVAILABLE`/`RESOURCE_EXHAUSTED` → `ERR_UNAVAILABLE` (`effects: none`) and
  `CANCELLED` → `ERR_CANCELLED`. A new status→category table test guards the
  mapping against future drift.

- **The consumer hook seam (specification + configuration = complete
  invocation).** New core types `OutputDecoder`, `ResultClassifier`, and
  `FieldRouter` — generic callbacks consulted by every format invoker for the
  wire questions a source artifact cannot answer (how output bytes decode,
  which completion statuses are success, which channel an input field rides).
  Consultation decline-chains per axis: per-invocation options
  (`WithOutputDecoder`/`WithResultClassifier`/`WithFieldRouter`) → invoker-level
  fields (`OperationInvoker.OutputDecoder` etc.) → the format built-in, with
  `ErrUseDefault` as the uniform decline. Hooks see an `InvokeSite` (canonical
  operation key, format, ref, target) and a `RawResult` (status, body, meta);
  failures carry tier provenance. `SnapshotHooks` exposes the both-tier
  snapshot to direct binding-layer callers; `WithRuntime` carries hook fields.
  Diagnostics ride invocation metadata: `x-ob-decode`/`x-ob-classify`/
  `x-ob-route` success stamps and the unvalidated-assumption warning
  (`x-ob-warning`).

- **BREAKING: content-independent decode/classify in the openapi and asyncapi
  invokers (de-sniffed).** openapi now decodes by the response's Content-Type
  HEADER (strict JSON for `application/json`/`+json` — a declared-JSON body
  that fails to parse is a loud `ERR_RESPONSE_ERROR` — text otherwise) and
  classifies success as 2xx; asyncapi decodes by the operation's declared
  message `contentType` and no longer unwraps `{error}`/`{data}` convention
  envelopes in the builtin (attach an `OutputDecoder` for convention lanes).
  The `MaybeJSON` helper (payload sniffing) is REMOVED from the core surface;
  error details carry the raw capture, never a parsed value.

- **BREAKING: the usage invoker consumes bare jdx artifacts.** The
  `openbindings.usage` wrapper format is deleted; the artifact IS the source
  (`usage@^2.0.0`), refs are space-separated command paths, and the exec
  assumptions are documented and hook-overridable: stdout decodes as text
  (command-substitution semantics; the JSON heuristic is gone), exit 0 is
  success, fields ride argv. Channel constants (`RouteArgv`, `RouteStdinDash`,
  `RouteStdin`, `RouteFile`) name the `FieldRouter` value space with loud
  argv-assembly refusals; `HookTable` compiles per-CLI elections
  (JSON lanes, ok-exits, routes) into guarded hooks. Synthesis emits
  floor-true `{"type":"string"}` output schemas carrying an in-schema
  `x-ob.floor` stamp that keys the diagnostics and clears on election.

- **Operations are invoked through signatures.** Added `OperationSignature[I, O]`
  (an inert `{Key}` carrying its input/output types as phantom parameters),
  `NewOperationSignature[I, O](key)`, the variadic functional options `InvokeOption`
  (`WithContext`, `WithBindingKey`), and the public free function
  `Invoke(ctx, invoker, obi, sig, ...InvokeOption) *TypedInvocation[I, O]`. The interface is a
  runtime argument, never part of the signature, so a signature is
  provider-agnostic; `[any, any]` is the dynamic flavor. The previous
  `(*OperationInvoker).Invoke(ctx, *OperationInvocationArgs)` method and the exported
  `OperationInvocationArgs` type are removed — the engine logic lives directly in the
  free `Invoke`, the single public invocation verb (no separate dispatch method and no
  args carrier).

- **Invocation is now a cardinality-agnostic handle.** `BindingInvoker.InvokeBinding`
  and the free `Invoke` return an `Invocation[I, O]` synchronously
  instead of `(<-chan InvocationOutput, error)`: the caller writes input
  messages (`Write`/`Close`), acquires the output sequence (`Outputs()
  OutputStream[O]`, read to `io.EOF` / terminal), and observes lifecycle via
  `Cancel`, `Header`, and `Trailer`. One call shape serves unary,
  server-streaming, client-streaming, and bidirectional bindings; cardinality
  lives in the binding, never in the signature. Bindings implement the
  push-side `BindingHandle[I, O]` (`ReadInput`, `CloseInput`, `EmitOutput`,
  `CloseOutput`, `FireError`, `Done`, `SetHeader`/`SetTrailer`) over the
  shared reference `InvocationImpl` (`NewInvocationImpl(ctx)`). The impl uses
  bounded buffered channels that are never closed on terminal — terminal state
  is a `done` channel plus `terminalErr`, every blocking op `select`s on it,
  and readers drain buffered outputs before surfacing the error (verified under
  `-race`). `Outputs()` is acquire-once (a second call panics
  `ERR_ALREADY_CONSUMED`); `OutputStream.Stop()` cancels. The one blessed
  terminal is the free function `Single(ctx, out)` — strict, short-circuiting
  "exactly one" (`ERR_EXPECTED_SINGLE`). `TypedInvocation[I, O]` adapts the
  untyped handle at the codegen boundary. The `InvocationOutput` envelope and
  its `Status`/`DurationMs` fields are gone: outputs are bare values; transport
  facts surface via `Header` metadata and error `Details`.
  - `BindingInvocationInput`/`BindingInvocationSource` → `BindingInvocationArgs`/
    `InvocationSource` (`{Source, Ref, Binding, Context, Interface, InputSchema}`;
    no `Input`, no `Security`, no `Store`, no `Callbacks`);
    `OperationInvocationInput` is removed — input flows through the handle, and
    invocation goes through the free `Invoke` + `OperationSignature`.
    `SingleEventChannel`, `FailedOutput`, and `HTTPErrorOutput`
    are removed (`HTTPError`/`HTTPErrorCode` replace the HTTP helpers).
  - OBI-T-07 failures are terminal AND reject the offending `Write` with the
    same `*InvocationError`; OBI-T-08 failures are terminal and the invalid
    value is not emitted (previously surfaced data-alongside-error in the
    envelope). Transforms evaluate per message in both directions.
  - `Invoke` returns a pre-errored handle (local codes
    `ERR_OPERATION_NOT_FOUND` / `ERR_BINDING_NOT_FOUND` / `ERR_UNKNOWN_SOURCE`)
    for wiring errors; runtime outcomes travel on the handle.

- **Error-code wire values are now SCREAMING_SNAKE with the `ERR_` prefix**
  (`ErrCodeCancelled = "ERR_CANCELLED"`, etc.), plus the un-prefixed
  negotiation signal `CONTEXT_REQUIRED` (`ErrCodeContextRequired`), in lockstep
  with the TypeScript SDK and the `openbindings.binding-invoker` role. New
  codes: `ErrCodeAlreadyConsumed`, `ErrCodeExpectedSingle`, `ErrCodeInputClosed`,
  `ErrCodeInvocationClosed`, `ErrCodeTooManyInputs`, `ErrCodeMissingInput`,
  `ErrCodeProtocol`, `ErrCodeTransportClosed`, `ErrCodeRuntime`. Consumers
  switching on `Code` must update. `ErrCodeInvalidInput` is removed (use
  `ErrCodeValidationFailed`).

- **Authentication is negotiated context, not a document field.** Bindings that
  need missing runtime context fire `CONTEXT_REQUIRED` (details:
  `ContextRequiredDetails` — `Key` + disjunctive `Alternatives` over
  conjunctive `Requirements`, families
  `auth.bearer`/`auth.apiKey`/`auth.basic`/`auth.oauth2`) BEFORE any observable
  side effect. The `OperationInvoker` resolves challenges via a
  composition-time `ContextResolver`, re-driving the binding against the same
  input buffer (the already-forwarded prefix is replayed; once a binding shows
  observable progress the challenge surfaces instead). Invokers that can derive
  requirements from their source implement the side-effect-free `BindingPreparer`
  preflight; `StoreContextResolver(store)`/`ContextSatisfies` compose the
  binding-invoker and context-store roles. `OperationInvoker.WithRuntime` now
  takes a `ContextResolver`.

- **Renamed binding "executor" terminology to "invoker" / "invoke"** to align with the OpenBindings spec 0.2.0 rename. Pre-1.0 hard rename, no deprecated aliases. Both layers — the per-format component and the orchestrator — use the `Invoker` noun, with the verb `Invoke` shared across them.
  - Types: `BindingExecutor` → `BindingInvoker`; `OperationExecutor` → `OperationInvoker`; per-format `*.Executor` → `*.Invoker` (e.g., `openapi.Executor` → `openapi.Invoker`); `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`; `OperationExecutionInput` → `OperationInvocationInput`; `ExecuteOutput`/`ExecuteError`/`ExecutionOptions` → `InvocationOutput`/`InvocationError`/`InvocationOptions`.
  - Methods: `BindingExecutor.ExecuteBinding(...)` → `BindingInvoker.InvokeBinding(...)`; `OperationExecutor.ExecuteOperation(...)` → the free `Invoke(...)`; `OperationExecutor.AddBindingExecutor(...)` → `OperationInvoker.AddBindingInvoker(...)`; `InterfaceClient.Execute(...)`/`ExecuteWithOptions(...)` → `InterfaceClient.Invoke(...)`/`InvokeWithOptions(...)`.
  - Constructors: `NewOperationExecutor` → `NewOperationInvoker`; per-format `NewExecutor` → `NewInvoker`.
  - Helpers: `CombineExecutors` → `CombineInvokers`; `ErrNoExecutor` → `ErrNoInvoker`.
  - File renames: `executor.go` → `binding_invoker.go`, `operation_executor.go` → `operation_invoker.go`, `executor_types.go` → `invoker_types.go`; per-format `executor.go` → `invoker.go`, `execute.go` → `invoke.go`.

- **`Interface.ValidateInterface()` renamed to `Interface.Validate()`.** The package-name-flavored verb was redundant when the receiver was already an `Interface`. `ValidateDocument(data)` (which parses then validates) keeps its name.

- **Validation options trimmed.** `WithExampleValidation` and `WithRequireSupportedVersion` removed: example schema validation (OBI-D-15 then; OBI-D-11 under the current numbering) and the supported-version check (OBI-T-04) are now unconditional in `Validate()`. `WithRejectUnknownTypedFields` is the only remaining option.

- **OBI-T-07 / OBI-T-08 nil guards tightened.** `Invoke` and the streaming output path now validate input/output against the operation's schema whenever the schema is specified, including when the value is `nil`. Previously these checks silently skipped on `nil`, which let invalid omissions slip past the contract.

- **Combiner format-token lookup** prefers exact token equality before falling back to range matching, so a source pinned to `openapi@3.1` no longer accidentally selects an invoker advertising `openapi@^3.0.0` when an exact entry is registered.

- **`schemaprofile`, `compatibility.go`, and `formattoken.normalizeSemverVersion` reframed as openbindings reference-tooling conventions** (not spec primitives). Spec 0.2.0 explicitly leaves schema comparison, operation matching, and format-token equivalence to tools per its §2 Scope principle. The package docstrings now state this; the helpers themselves are unchanged in behavior. `formattoken.normalizeVersion` was renamed to `normalizeSemverVersion` to make the SemVer-only scope explicit.

- **`ErrCodeExecutionFailed` retains its name** with a new comment explaining the deliberate retention: error codes name runtime outcomes (the call was *executed* and the service returned an error), not the SDK type or method that produced them, so the rename did not propagate to the error code.

### Removed

- **The `security` surface, per spec 0.2.0**: the OBI `security` section
  (`Interface.Security`), `BindingEntry.Security`, `SecurityMethod`,
  `ResolveSecurity`, and the security-reference validation
  (`security.go`/`security_test.go` deleted). Credentials are never part of an
  OBI document; they are context, supplied per call or resolved through the
  `CONTEXT_REQUIRED` protocol. Format invokers derive auth requirements from
  their source artifacts (e.g. OpenAPI `securitySchemes`) and read credentials
  from context's well-known fields. `ContextStore`/`PlatformCallbacks` are no
  longer threaded through binding invocations; interactive resolution lives in
  the app's `ContextResolver`.

- **Conformance rule IDs corrected** to match the spec: `OBI-D-16` → `OBI-D-13`
  (SemVer `openbindings` field; `OBI-D-12` under the current numbering),
  `OBI-T-13` → `OBI-T-12` (operation-name resolution).

- **`InterfaceClient`.** The struct and its `InterfaceClientOption`,
  `WithContextStore`, `WithPlatformCallbacks`, and `WithDefaultContext`
  options are gone. Generated typed invokers (from `ob codegen`) wrap an
  `*OperationInvoker` directly and take the OBI per method call. Direct
  callers use the free `Invoke(ctx, invoker, obi, sig, opts)` and configure
  runtime via `OperationInvoker.WithRuntime(resolver)`.

- **`InvocationOptions`.** Folded into `BindingContext`. Transport fields
  (`headers`, `cookies`, `environment`, `metadata`) are well-known keys
  inside the context map; helpers `ContextHeaders`/`ContextCookies`/
  `ContextEnvironment`/`ContextMetadata` read them. `BindingInvocationInput`
  no longer carries a separate `Options` field.

### Fixed

- **Schema validation failure rendering.** `splitSchemaError` and
  `collectValidationFailures` formatted jsonschema/v6 `ErrorKind` leaves with
  `%v`, printing the raw struct (`&{[customer]}` for a missing required
  property) instead of the kind's localized message (`missing property
  'customer'`). The garbage text also flowed into
  `ValidationFailureDetails.Failures[].Message` — the wire-crossing details
  payload consumers render per-field, where the TS SDK already produced
  readable messages. Leaves now render via `ErrorKind.LocalizedString`.

### Added

- **`Invocation.InputClosed()`** — a channel closed once the invocation's input side has closed: by the caller's `Close`, by the binding from below (a unary binding after its first read), or by a terminal transition. Lets consumers that pipe a stream into an invocation (the operation-graph conduit) observe non-acceptance without probing with a failing `Write`. Implemented by `InvocationImpl` and forwarded by `TypedInvocation`.

- **`ErrTransformUndefined`** sentinel — evaluators return it (possibly wrapped) when an expression yields no result, since Go's `any` cannot distinguish JSONata's undefined from JSON null. The operation-graph engine maps it to the spec's `TRANSFORM_UNDEFINED` node failure; null flows downstream normally.

- **`ErrCodeUnsupportedFormatVersion`** (`ERR_UNSUPPORTED_FORMAT_VERSION`) for format-version refusal (e.g. operation-graph OG-T-02 mirroring OBI-T-04). `ErrCodeMapNotArray` is removed: per-node graph failure identifiers (`TIMEOUT_EXCEEDED`, `WRITE_REJECTED`, `MAP_NOT_ARRAY`, `TRANSFORM_UNDEFINED`) are format error identifiers and live in `formats/operationgraph`.

- **URI helpers** `CanonicalizeLocation` and `ResolveRef` per spec §10 (Location Equality) and §12 (Reference Resolution). `CanonicalizeLocation` lifts bare absolute paths to `file://`, lowercases scheme and host, IDN-punycodes via `golang.org/x/net/idna`, strips the default port and fragment, removes dot-segments, and normalizes percent-encoding of unreserved characters; reassembly is manual to preserve encoded reserved characters (e.g., `%2F`) that `url.URL.String()` would otherwise discard. `ResolveRef` is a thin wrapper over `url.URL.ResolveReference` with the spec-required guards for empty/non-absolute bases.

- **`drainStream` helper** extracted in `operation_invoker.go` so the producer-drain pattern used by transform short-circuits and stream cancellation is named once and reused.

### Format submodules

- **`formats/grpc` and `formats/connect` migrated to protobuf v2.** Direct dependencies on `github.com/jhump/protoreflect` (v1) and `github.com/golang/protobuf/jsonpb` are gone; both modules now consume `github.com/jhump/protoreflect/v2/grpcdynamic`, `github.com/jhump/protoreflect/v2/grpcreflect`, `google.golang.org/protobuf/types/dynamicpb`, `google.golang.org/protobuf/encoding/protojson`, and `google.golang.org/protobuf/reflect/protoreflect` directly. `formats/connect` additionally moved off `jhump/protoreflect/desc/protoparse` to `github.com/bufbuild/protocompile`. Behavior is preserved across the change; the two integration suites pass against the same fixtures.

## 0.1.1 — 2026-04-20

### Added

- `CreatorWarning` type and `CreateInput.OnWarning` handler for
  surfacing non-fatal limitations encountered during interface
  construction (e.g., a source-side feature the schema profile cannot
  fully express). Creators that hit such a limitation still produce a
  valid `Interface`; the warning describes what was lost or
  approximated. The handler is optional; when nil, warnings are
  dropped silently, preserving prior behavior for callers who do not
  opt in.

## 0.1.0 — 2026-03-31

Initial public release.

- Core types for OpenBindings interface documents with lossless JSON round-tripping
- Interface validation with strict mode for unknown fields and format token validation
- Schema compatibility checking (Profile v0.1) with covariant/contravariant directionality and diagnostic reasons
- InterfaceClient for OBI resolution via URL, well-known discovery, and synthesis
- OperationExecutor with format token range matching (caret, exact, versionless)
- Unified stream execution model — every operation returns `<-chan StreamEvent`
- BindingKey support for explicit binding selection bypassing the default selector
- Context store with scheme-agnostic key normalization (`host[:port]`)
- Transform pipeline (input + output) with per-event error propagation
- Security types (`SecurityMethod`, `Interface.Security`, `BindingEntry.Security`) for declaring auth methods on bindings
- `ResolveSecurity` helper for interactive credential resolution via `PlatformCallbacks`
- Standard error codes (`errcodes.go`) for protocol-agnostic error handling
- HTTP error helpers (`HTTPErrorOutput`, `httpErrorCode`) for mapping HTTP status codes to error codes
- Security method pass-through in `OperationExecutor` via `BindingExecutionInput.Security`
- Subpackages: `canonicaljson` (RFC 8785), `formattoken` (semver range matching), `schemaprofile` (Profile v0.1)
