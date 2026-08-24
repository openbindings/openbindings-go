# Changelog

## 0.2.0 (working draft)

> A 0.1.1 patch release was prepared 2026-04 but never tagged or published; its entries are folded into this section.

### Changed

- **The binding entry's target member is `selector`, not `ref`** (breaking;
  the ratified pre-launch rename, executed with no aliases or deprecation
  shims). The OBI member `bindings[*].ref` is now `bindings[*].selector`,
  and every public symbol naming that concept follows: `BindingEntry.Selector`,
  `invoke.BindingInvocationArgs.Selector`, `invoke.InvokeSite.Selector`,
  `synthesize.BindableTarget.Selector` (wire `selector`),
  `synthesize.SynthesisCoverageEntry.BindingSelector` (wire `bindingSelector`,
  synthesis-scenario `bindingSelector` likewise), and the SDK implementation
  error codes `invoke.ErrCodeInvalidSelector` (`ERR_INVALID_SELECTOR`) and
  `invoke.ErrCodeSelectorNotFound` (`ERR_SELECTOR_NOT_FOUND`). JSON Schema
  `$ref` handling is deliberately untouched everywhere: the rename covers the
  binding-target-selector concept, never JSON References.

- **config.value requirements carry an engine-asserted `schema` instead of
  `choices`** (breaking; the 2026-08-20 working-draft amendment of the
  binding-invoker contract — one mechanism, no sugar).
  `invoke.NewConfigValueRequirement` takes a `map[string]any` JSON Schema
  (nil = absent = unconstrained); a present `schema` member must be a JSON
  object (not metaschema-validated) or the challenge is invalid; satisfaction
  validates the selected configuration value against the schema when one is
  carried (an `enum` member is a closed admissible set). Engines emit
  `{"enum": […]}` exactly where they previously emitted `choices` — where the
  admissible set is already computed at the emission site — and stay absent
  otherwise.

- **`invoke.StoreContextResolver` keys config-only alternatives by the exact
  asserted target** (the 2026-08-19 context-scope model): an alternative
  consisting solely of config.value requirements fetches under the verbatim
  challenge target — the engine-asserted artifact-bound scope — while any
  credential-bearing alternative keeps the endpoint-normalized
  (`NormalizeEndpoint`) convention. Endpoint normalization would conflate a
  canonicalized source URL with its origin, letting one artifact's
  configuration answers resolve a same-host sibling's challenge. The asyncapi
  engine's challenge target accordingly falls back resolved server URL →
  artifact host hint → canonicalized source location (empty for a
  content-only source, which asserts nothing).

- **The SDK is layered into core plus `invoke`, `synthesize`, and `compare`
  sub-packages** (breaking; import-path changes only, no renames or behavior
  changes). Placement follows the authority source: what `openbindings.md`
  defines stays in the root package (document model, validation, operation
  resolution, boundary schema validation, versions/constants); the
  binding-invoker/operation-invoker realization lives in `invoke`; the
  interface-synthesizer/source-inspector realization — including
  `FetchInterface` and the synthesis-scenarios runner, now at
  `synthesize/synthesisscenarios` — lives in `synthesize`; interface and
  operation compatibility checking lives in `compare`. The root package
  imports none of the three. The former unexported `compileOperationSchema`
  is exported as `CompileOperationSchema` (core) for the invocation runtime;
  `IsOBInterface`, `IsHTTPURL`, and `BindingSpecInfo` are seated in core.

- **The portable synthesis corpus runs at
  `openbindings.binding-spec-synthesis-scenarios@4`** (revved from @3 for the
  binding-identity member rename `bindingRef` → `bindingSelector`; `sourceRef`
  is unchanged). `synthesisscenarios.Verify`
  takes a `SynthesizerFactory` rather than one synthesizer, because a scenario
  may now declare companion documents that the family adapter has to serve
  through its own artifact-resolver seam. `synthesisscenarios.Fixed` adapts a
  single synthesizer for the six families whose corpus sources are
  self-contained and **refuses** a scenario declaring `resources` rather than
  running it against a resolver that would never see them; `fixedSynthesizer`
  in `@openbindings/sdk` is its twin, with the same message and the same
  placement outside the expected-outcome handling. A scenario's optional
  `assertions` are evaluated against the emitted OBI document through
  `processorscenarios.CheckAssertions`, which is the processor runner's own
  evaluator exported rather than a second implementation of the same five
  verbs, and are not part of the compared identity surface.

- **Synthesis coverage: an `invalid` entry now clears `FullyRepresented`.**
  Every non-represented status — lossy, excluded, invalid,
  implementation-unsupported — clears the derived flag; previously an
  upstream-invalid unit left it standing, so a document whose every target
  was invalid could report `fullyRepresented: true` (MC5 seal-1 finding
  F-V3-1). The TS SDK carries the identical change.

- **Invocation failures now use the minimal abstract record `{code,data?}`.**
  Portable message, details, and diagnostics members were removed; data is
  normalized to the JSON domain and preserves absent versus explicit null.
  `CONTEXT_REQUIRED` retains its closed OR-of-AND challenge in data and is
  validated before resolution. Frame and operation-schema mechanics now use
  the collision-resistant owned codes `ERR_FRAME_PROTOCOL` and
  `ERR_OPERATION_VALIDATION_FAILED`; binding-specific `ERR_PROTOCOL` and
  `ERR_VALIDATION_FAILED` remain open identifiers. Caller cancellation and
  caller-supplied lifetime deadlines uniformly produce `ERR_CANCELLED`;
  native timeout evidence remains below the bridge. `config.value` now uses a
  relative JSON Pointer path, stored context is reused only when every
  requirement of the selected alternative explicitly permits durability, and
  named credentials remain scheme-scoped. The Core OBI document model is
  unchanged.

- **The OpenAPI module now exposes a standalone artifact runtime.** Direct
  callers can use `openapi.Runtime` without constructing an OBI, while
  `openapi.Invoker` remains the thin SDK adapter and `openapi.Synthesizer`
  owns OBI construction. The extraction preserves the complete unreleased
  first `openbindings.openapi@1` candidate behavior; it changes neither Core
  nor the candidate's meaning.

- **The OpenAPI module now defaults to `openbindings.openapi@1`.** Exact
  schema-omitted OAS 3.0 non-JSON request and response representations cross
  the protocol-independent boundary as canonical Base64. Media ranges and
  artifact-defined codecs remain unchanged. No OpenAPI binding specification
  has been published; this is part of its first `@1` candidate. Core is
  unchanged.

- **The OpenAPI module now defaults to `openbindings.openapi@1`.** Exact
  JSON-family request schemas whose top-level declarations require
  combinators, conditionals, dependent schemas, or explicit
  `unevaluatedProperties` remain one protocol-neutral application value.
  Binding-private routing preserves the complete value without choosing a
  schema branch or exposing HTTP concepts. Dynamic-object carriage remains
  part of the same first candidate. Core is unchanged.

- **OpenAPI security and request-channel handling now preserves artifact
  alternatives without leaking HTTP concepts into OBI contracts.** Invocation
  selects one complete Security Requirement Object instead of unioning OR
  alternatives, never volunteers ambient credentials when the operation
  declares no security, and refuses processor-owned `Host`, `Content-Length`,
  and conflicting raw/structured cookie declarations before dispatch.
  Synthesis excludes parameter-content media that the candidate cannot
  faithfully carry instead of emitting an operation guaranteed to refuse.
  Undefined security-scheme names fail closed, including in mixed OR sets.
  These changes remain entirely in the OpenAPI binding adapter; the core OBI
  document model is unchanged.

- **OpenAPI synthesis now projects request and response schemas in their
  authored data directions.** Request contracts omit `readOnly` properties,
  response contracts omit `writeOnly` properties, and nested, composed, map,
  and recursive required sets remain coherent. OpenAPI 3.1 Schema Object
  `$ref` siblings are normalized before typed artifact resolution so their
  constraints and annotations compose; strict 3.0 Reference Object siblings
  remain ignored, and legal 3.1 Reference Object descriptions remain local to
  each reference site. Schema-shaped data in examples/extensions remains
  opaque. A synthesis-only raw presence sidecar now preserves authored null,
  empty, zero, false, and `x-*` Schema Object values that the typed upstream
  parser cannot distinguish from absence; typed OpenAPI objects remain the
  operational authority, and invocation does not use the sidecar. Unsupported
  custom schema dialects fail portable synthesis honestly without globally
  disabling artifact-native invocation or reference listing. This stays
  within the OpenAPI loader/projection layer and does not alter Core.

- **OpenAPI invocation, inspection, and synthesis now resolve complete
  multi-document descriptions through the caller-supplied HTTP client and
  context.** External artifact retrieval is cached, cancellation propagates,
  redirects contribute their final retrieval URI, non-success retrievals fail
  loudly, and resolved external SchemaRefs are internalized with stable
  collision-resistant local identities before OBI projection so
  artifact-relative references cannot escape as dangling OBI schema
  references. Resolver configuration remains binding-private and does not
  alter the protocol-blind OBI document model.

- **`openbindings.openapi@1` added response-carriage fidelity.** It adds
  response media-range selection and exact artifact-authorized raw response
  bytes as canonical Base64 application values, without exposing HTTP facts
  in ordinary outputs.

- **`openbindings.openapi@1` added request-media fidelity.** It adds
  declaration-led raw-octet requests and configured media-range selection
  while keeping HTTP identities out of operation contracts. The same first
  candidate retains collision-preserving routed inputs.

- **Per-operation dependencies compose compatibility, invocability, and
  caller policy without introducing a registry.** The core SDK now exposes
  `OperationRequirement`, `CheckOperationCompatibility`,
  `MatchOperationRequirement`, and `ResolveOperationRequirement`. A consumer
  pairs an ordinary required OBI with a typed operation signature; an
  application supplies concrete interfaces and its explicitly installed
  `OperationInvoker`s. Matching is alias-aware, checks only the requested
  operation against both complete schema graphs, performs side-effect-free
  binding preflight, and carries advisory context requirements. The neutral
  matcher returns every invocable match; the route-to-one convenience selects
  a unique highest caller preference and refuses a tie as
  `OperationRequirementAmbiguous`. Format modules remain optional and
  separately linked.

- **`FetchInterface` retains synthesis coverage.** A synthesized
  `FetchedInterface` now carries the durable `SynthesisCoverage` emitted by a
  coverage-capable synthesizer instead of discarding it at acquisition. A
  synthesizer without that optional surface still falls back to strict
  synthesis. Direct and well-known OBI fetches leave coverage absent.

- **MCP synthesis and invocation now support a fidelity-tested native
  round trip.** Synthesized tool outputs describe the complete
  `CallToolResult` (with an upstream `outputSchema` correctly scoped to
  `structuredContent`) and admit solicited progress; resource operations
  describe complete `ReadResourceResult` values. Live embedding retains the
  raw pagination-exhausted listing for descriptor-preserving adapters,
  unsupported negotiated revisions are gated consistently, native
  `isError` results retain their complete MCP payload in structured error
  details, and `bearerToken` uses the specification's declared
  `Authorization: Bearer` carrier.

- **Portable synthesis conformance now proves refusal as well as successful
  coverage.** The shared version-2 corpus requires loud whole-source failure
  where faithful synthesis is impossible, and records runtime configuration
  prerequisites on represented targets. The resulting loop fixed gRPC and
  Connect authoring paths that could emit sources with non-conforming target
  addresses; OpenAPI coverage now identifies unresolved server selection, and
  gRPC coverage identifies the transport election required by bare
  `host:port`.

- **The experimental Workers RPC stub was removed.** It could neither invoke
  the runtime-local protocol from Go nor synthesize an interface, and no
  published binding specification governed its legacy token. The Go SDK now
  exposes only binding modules with complete first-party implementations.

- **GraphQL now implements the unreleased first `openbindings.graphql@1`
  candidate end to end.** Invocation requires the exact executable document,
  verifies its selected kind and one-root-field correspondence, passes caller
  input wholesale as variables, and emits the selected root application value.
  GraphQL response envelopes remain diagnostic; a selected partial value is
  preserved before unsuccessful completion. Synthesis inventories query and
  mutation root fields with root-value schemas and exhaustive coverage;
  subscriptions are excluded rather than approximating their continuing
  partial-data/error lifecycle. Go and TypeScript exercise the same candidate
  semantics. No GraphQL binding specification has been published.

- **Comparison-engine cross-SDK canon (three rulings, 2026-07-20).**
  (1) `CheckInterfaceCompatibility`'s issue ordering — sorted
  required-operation-key order, output before input within an operation — is
  now a documented, pinned contract (the TS SDK changed to match; the Go
  order is unchanged). (2) Property/`required` member names in reason
  strings now interpolate in JCS (RFC 8785) rendering — the same rendering
  values already get — instead of Go `%q`; visible only for names carrying
  quotes, backslashes, or control characters (e.g. a name carrying U+0001
  now renders it as `\u0001`, not Go's `\x01`); plain names render
  byte-identically to before.
  (3) The package-level `schemaprofile.InputCompatible` /
  `OutputCompatible` now refuse tell-tale non-normalized inputs loudly with
  the new `NotNormalizedError` (`not normalized at <path>: keyword "<kw>"
  must be <requirement>`) instead of risking silently divergent verdicts:
  a scalar `type`, an unresolved `$ref`, or an unflattened `allOf`,
  anywhere the comparison would recurse. Normalized-path callers
  (`Normalizer` methods, `CheckInterfaceCompatibility`) are unaffected.
  All three are pinned byte-for-byte against the TS SDK in the mirrored
  alignment tables (`schemaprofile/reasons_test.go` ↔
  `packages/sdk/src/schema-profile/reasons.test.ts`).

- **Type names in comparison reason strings join the JCS canon** (a direct
  extension of ruling (2) above). The missing-type diagnostics (`type:
  candidate does not allow ...` / `type: candidate allows ... but target
  does not`) now render type names via the same JCS (RFC 8785) string
  rendering member names and values use, instead of Go `%q`. For the seven
  legitimate JSON Schema type names the output is byte-identical to before
  (verified, not assumed); the change is visible only for pathological type
  names reachable via non-normalized input (a name carrying a quote,
  backslash, or control character — e.g. U+0001 now renders `\u0001`, not
  Go's `\x01`). Pinned byte-for-byte against the TS SDK in the mirrored
  alignment tables.

- **OBI-D-05 literal form is enforced.** A percent-encoded same-document
  fragment (`#/schemas/T%61sk`) now fails validation at OBI positions, and the
  OBI-D-16 resolver no longer percent-decodes — the non-conformant spelling is
  never honored.

- **One endpoint-key derivation.** `NormalizeContextKey` strips URL userinfo
  and case-folds the host, and the resolver read path (`NormalizeEndpoint`)
  delegates to it. Derived context-store keys change for URLs with mixed-case
  hosts or userinfo; they now match the TS SDK byte-for-byte.

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

- **Added `ErrCodeUnavailable` (`ERR_UNAVAILABLE`) to the open code space.**
  Binding implementations decide when their governing rules use it. The
  abstract code carries no universal retry category, side-effect claim, or
  native status mapping.

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
  Decode/classify/route provenance and unvalidated-assumption warnings remain
  below the abstract invocation boundary as binding-interpretation evidence.

- **BREAKING: content-independent decode/classify in the openapi and asyncapi
  invokers (de-sniffed).** openapi now decodes by the response's Content-Type
  HEADER (strict JSON for `application/json`/`+json` — a declared-JSON body
  that fails to parse is a loud `ERR_RESPONSE_ERROR` — text otherwise) and
  classifies success as 2xx; asyncapi decodes by the operation's declared
  message `contentType` and no longer unwraps `{error}`/`{data}` convention
  envelopes in the builtin (attach an `OutputDecoder` for convention lanes).
  The `MaybeJSON` helper (payload sniffing) is REMOVED from the core surface.
  Raw captures remain below the abstract invocation boundary and never become
  failure data.

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
  `Cancel`. One call shape serves unary,
  server-streaming, client-streaming, and bidirectional bindings; cardinality
  lives in the binding, never in the signature. Bindings implement the
  push-side `BindingHandle[I, O]` (`ReadInput`, `CloseInput`, `EmitOutput`,
  `CloseOutput`, `FireError`, `Done`) over the
  shared reference `InvocationImpl` (`NewInvocationImpl(ctx)`). The impl uses
  bounded buffered channels that are never closed on terminal — terminal state
  is a `done` channel plus `terminalErr`, every blocking op `select`s on it,
  and readers drain buffered outputs before surfacing the error (verified under
  `-race`). `Outputs()` is acquire-once (a second call panics
  `ERR_ALREADY_CONSUMED`); `OutputStream.Stop()` cancels. The one blessed
  terminal is the free function `Single(ctx, out)` — strict, short-circuiting
  "exactly one" (`ERR_EXPECTED_SINGLE`). `TypedInvocation[I, O]` adapts the
  untyped handle at the codegen boundary. The `InvocationOutput` envelope and
  its `Status`/`DurationMs` fields are gone: outputs are bare values.
  Unsuccessful completion is exactly `Code` plus optional `Data`; transport
  facts remain below the abstract boundary.
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

- **Compatibility checking now handles the boolean `false` schema — the
  spec's spelling for "carries no caller input" / "emits no output".**
  `CheckInterfaceCompatibility` previously rewrote `false` to its object
  spelling `{"not": {}}`, which the schema-compatibility profile rejects
  (`outside profile: keyword "not"`), so any operation declaring
  `input: false` failed requirement resolution as `input_incompatible`
  even against itself. `false` now short-circuits before normalization:
  compatible exactly with `false`, incompatible (with a clear reason)
  against any other specified schema; `true` continues to flow through
  the normal check as the empty schema. Caught live by the Panjir dogfood
  loop resolving a no-input contract operation. Mirrored in the TS SDK.

- **Operation-boundary schema validation now preserves the OBI document as
  the same-document reference root.** Input, output, and example validation
  compile schemas at their canonical `#/operations/...` addresses instead of
  extracting them into a synthetic root, so operation-local recursive
  `$defs`, cross-operation pointers, escaped operation keys, named schemas,
  and embedded absolute `$id` resources retain their JSON Schema meaning.
  `ValidateOperationInput` and `ValidateOperationOutput` expose the same
  interface-aware boundary to applications that drive binding invokers
  directly.

- **Schema-comparison `allOf` normalization is sound.** Branches normalize
  fully before merging (`$ref` branches resolved and profile-checked, nested
  `allOf` flattened), sibling keywords merge as one additional branch, and
  `oneOf`/`anyOf` are refused whether inline, ref-carried, or alongside
  `allOf`. Closes the false-`compatible` family; red-proven against the seven
  new comparison-corpus fixture families.

- **Interface compatibility resolves each side's `#/schemas/` refs against its
  own document.** `CheckInterfaceCompatibility` normalized both sides with a
  single unrooted normalizer, so any fragment-only `$ref` on either side
  failed resolution and surfaced as a spurious
  `output_incompatible`/`input_incompatible` issue — an interface using the
  `schemas` section got a different verdict here than from the TS SDK, which
  roots per side. Each side now normalizes against its own document view and
  the pre-normalized pair runs the package-level directional check. The
  comparison-corpus harness applies the same per-side rooting (it previously
  resolved the right document's refs against the left document), pinned by
  the corpus's `subsumption/schemas-root-per-side-*` fixtures.

- **Comparison reason strings are deterministic and TS-aligned.** Multi-member
  diagnostics no longer leak Go map iteration order into the detail string:
  missing-type lists render sorted lexicographically, and enum/required/
  properties faults name the lexicographically first failing member —
  byte-identical with the TS SDK, pinned by the mirrored reason-string table
  (`schemaprofile/reasons_test.go`).

- **`Validate` accepts leading-digit identifiers (`2fa.verify`) per the
  committed OBI-D-03 grammar**, so `ParseDocument` and `Validate` agree again.

- **Scheme-scoped `apiKeys` redact like every other credential field.**
  Redaction and scoping single-source the one credential registry, pinned by a
  per-field sentinel drift-guard test.

- **`InvokeHooks` decode/classify provenance is published atomically**, with
  the concurrency contract stated in godoc (previously it rode an incidental
  happens-before edge through the emit path).

- **README**: the module bundles a JSONata 2.x parser (gnata) for OBI-D-18
  parse-checks — the "no bundled JSONata runtime" claim was stale. Retired-rule
  citations repointed (OBI-T-08 → OBI-T-16).

- **Schema validation failure rendering.** `splitSchemaError` and
  `collectValidationFailures` formatted jsonschema/v6 `ErrorKind` leaves with
  `%v`, printing the raw struct (`&{[customer]}` for a missing required
  property) instead of the kind's localized message (`missing property
  'customer'`). The garbage text also flowed into
  `ValidationFailureDetails.Failures[].Message` — the wire-crossing details
  payload consumers render per-field, where the TS SDK already produced
  readable messages. Leaves now render via `ErrorKind.LocalizedString`.

### Added

- **Configurable delivery-unit bound.** `BindingInvocationArgs.MaxDeliveryUnitBytes`
  bounds ONE DELIVERY UNIT — the bytes materialized to produce one emitted
  output value (a unary response body, one SSE event, one streaming envelope,
  one WebSocket message, one captured stdout). `OperationInvoker` gains the
  matching public policy field, stamped into args exactly like the hook
  fields; direct binding-layer callers set it per invocation on args. Zero or
  negative selects the exported `DefaultMaxDeliveryUnitBytes` (10 MiB —
  byte-identical to the per-lane constants it replaces; no unlimited
  sentinel, set an explicitly huge bound instead);
  `BindingInvocationArgs.DeliveryUnitLimit()` is the single resolution point
  formats call. Overflow error identity per lane is unchanged — same codes,
  same message templates, the value is now dynamic. Read sites: openapi
  (unary body), asyncapi (unary reply, SSE per-event, WebSocket per-message),
  connect (unary body, per-envelope), graphql (response body, subscription
  per-message), usage (stdout capture). Named exclusions, documented in each
  format README's "Resource bounds" note: grpc (message size rides grpc-go's
  native `MaxCallRecvMsgSize` via the `WithDialOptions` pass-through), mcp
  (no read-bound seam in the official MCP Go SDK), operationgraph doc-fetch
  (artifact guard). Diagnostics and artifact-side bounds (error-body
  captures, stderr tail, SSE line-scanner guards, artifact fetches, routing
  caps) stay fixed by design.

- **Transforms compile-lane conformance gate**: the spec
  `conformance/transforms` agreement corpus runs through `gnata.Compile` — the
  exact parse surface `Validate` ships.

- **CI corpus gating (`OB_CORPUS_REQUIRED`)**: CI checks out both corpus roots
  (spec + interfaces) and every corpus locator fails loudly when a corpus is
  required and absent; local skip-if-absent behavior is unchanged.

- **Conformance-runner `requiresSupports` annotation**: the corpus harness
  honors the per-test `requiresSupports: "X.Y.Z"` annotation — the test is
  administered only when this SDK's OBI-T-04 version-acceptance predicate
  (`IsSupportedVersion`) accepts X.Y.Z; otherwise it is skipped and reported
  as a skip, never a failure, alongside the existing `requiresMaxTested` /
  `requiresMinSupported` gates. A corpus without the annotation runs
  unchanged.

- **`Invocation.InputClosed()`** — a channel closed once the invocation's input side has closed: by the caller's `Close`, by the binding from below (a unary binding after its first read), or by a terminal transition. Lets consumers that pipe a stream into an invocation (the operation-graph conduit) observe non-acceptance without probing with a failing `Write`. Implemented by `InvocationImpl` and forwarded by `TypedInvocation`.

- **`ErrTransformUndefined`** sentinel — evaluators return it (possibly wrapped) when an expression yields no result, since Go's `any` cannot distinguish JSONata's undefined from JSON null. The operation-graph engine maps it to the spec's `TRANSFORM_UNDEFINED` node failure; null flows downstream normally.

- **`ErrCodeUnsupportedFormatVersion`** (`ERR_UNSUPPORTED_FORMAT_VERSION`) for format-version refusal (e.g. operation-graph OG-T-02 mirroring OBI-T-04). `ErrCodeMapNotArray` is removed: per-node graph failure identifiers (`TIMEOUT_EXCEEDED`, `WRITE_REJECTED`, `MAP_NOT_ARRAY`, `TRANSFORM_UNDEFINED`) are format error identifiers and live in `formats/operationgraph`.

- **URI helpers** `CanonicalizeLocation` and `ResolveRef` per spec §10 (Location Equality) and §12 (Reference Resolution). `CanonicalizeLocation` lifts bare absolute paths to `file://`, lowercases scheme and host, IDN-punycodes via `golang.org/x/net/idna`, strips the default port and fragment, removes dot-segments, and normalizes percent-encoding of unreserved characters; reassembly is manual to preserve encoded reserved characters (e.g., `%2F`) that `url.URL.String()` would otherwise discard. `ResolveRef` is a thin wrapper over `url.URL.ResolveReference` with the spec-required guards for empty/non-absolute bases.

- **`drainStream` helper** extracted in `operation_invoker.go` so the producer-drain pattern used by transform short-circuits and stream cancellation is named once and reused.

- **`SynthesizeInput.OnWarning` handler and the `SynthesizerWarning` type**
  (folded from the unpublished 0.1.1, where they were introduced as
  `CreateInput.OnWarning` and `CreatorWarning`, before the
  Creator → Synthesizer rename) for surfacing non-fatal limitations
  encountered during interface construction (e.g., a source-side feature the
  schema profile cannot fully express). Synthesizers that hit such a
  limitation still produce a valid `Interface`; the warning describes what
  was lost or approximated. The handler is optional; when nil, warnings are
  dropped silently, preserving prior behavior for callers who do not opt in.

### Format submodules

- **`formats/grpc` and `formats/connect` migrated to protobuf v2.** Direct dependencies on `github.com/jhump/protoreflect` (v1) and `github.com/golang/protobuf/jsonpb` are gone; both modules now consume `github.com/jhump/protoreflect/v2/grpcdynamic`, `github.com/jhump/protoreflect/v2/grpcreflect`, `google.golang.org/protobuf/types/dynamicpb`, `google.golang.org/protobuf/encoding/protojson`, and `google.golang.org/protobuf/reflect/protoreflect` directly. `formats/connect` additionally moved off `jhump/protoreflect/desc/protoparse` to `github.com/bufbuild/protocompile`. Behavior is preserved across the change; the two integration suites pass against the same fixtures.

## 0.1.0 — 2026-03-31

> The date reflects the content freeze; the v0.1.0 tags were created 2026-04-15. From 0.2.0 on, entry dates are tag dates.

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
