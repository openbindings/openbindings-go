# Changelog

## 0.2.0 (working draft)

### Changed

- **BREAKING: `configuration.server` accepts exactly the SDK's composable
  carriage for the §9.2 server point** — `{"key": "<server-name>"?,
  "variables": {"<variable-name>": "<string-value>"}?, "url":
  "<connection-url>"?}`. The sole bindable effective-set member selects
  itself; several members require `key`. `variables` completes the selected
  member and `url` may replace only that member's target using the same scheme
  as its declared protocol. The previously tolerated spellings — a bare string
  (member name or URL) and `{"name": ...}` — are refused loudly with a
  teaching error naming the pinned shape (byte-identical to the TS SDK's;
  the pin exists so two implementations carry the value identically,
  and silent tolerance of extra spellings defeats it). Server variables
  substitute supplied-else-default-else-refusal: an undeclared supplied name
  is refused, never ignored, and a supplied value outside the variable's
  declared `enum` is refused (upstream SHOULD, hardened per the spec's
  2026-07-21 §9.2 amendment). The `variables` member restores the
  pre-alignment capability the 2026-07-20 pin briefly removed — it rode the
  unpinned `{"name", "variables"}` spelling — now under the sanctioned
  spelling; AsyncAPI declares Server Variable defaults OPTIONAL, so an
  undefaulted variable is satisfiable only by supply. Applies to invocation
  (`InvokeBinding`) and the endpoint-resolution export
  (`Document.ResolveEndpoint`) alike. The below-the-point `metadata.baseURL`
  legacy override is unchanged.

### Added

- **Endpoint-resolution export** (`ParseDocument`,
  `Document.ResolveEndpoint`, `Endpoint`): the §9.2 server and address
  configuration points (ASYNC-P-04) resolved for one operation without
  dispatching — for consumers that dial an AsyncAPI-described endpoint with
  their own transport instead of re-deriving server selection outside the
  format seam (the ob CLI's delegate frame lane is the consumer). The same
  code path the invoker runs before dispatch; refs per ASYNC-D-03; pure, no
  I/O. Go-only by design — a named gap, not an oversight: the TS package
  keeps its target resolution internal because ob is this seam's sole
  consumer (see README "Endpoint resolution").

- **Configurable delivery-unit bound**: the unary publish reply body, each
  SSE event, and each WebSocket message honor
  `BindingInvocationArgs.MaxDeliveryUnitBytes` (default
  `openbindings.DefaultMaxDeliveryUnitBytes`, 10 MiB). Overflow error
  identity is unchanged. On a pooled WebSocket the per-message read limit
  only ever rises across acquirers (a smaller sibling bound never severs an
  in-flight subscription). The HTTP error-body capture, SSE line-scanner
  guard, and synthesis artifact-fetch guard stay fixed (not delivery units).

### Fixed

- **WebSocket messages were accidentally capped at ~32 KiB**: no read limit
  was ever set on pooled sockets, so the websocket library's 32768-byte
  default applied per message — ~300x below the documented 10 MiB
  convention. The delivery-unit bound is now applied as the socket's read
  limit on every connection.

### Changed

- **WebSocket library migrated** from the archived `nhooyr.io/websocket` to
  its maintained rename `github.com/coder/websocket` (API-compatible).

- **Unary publish success is strict 2xx (ASYNC-P-06)**: a 3xx final status is
  now a failure, matching the SSE path's classification.
- **Pooled-WebSocket writes are detached from the invocation context**:
  cancelling one invocation mid-write no longer tears down the shared
  connection for sibling subscriptions.

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the root SDK (executor → invoker rename, `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`, `InvocationOptions` folded into `BindingContext`, `InterfaceClient` removed in favor of codegen-emitted `<Name>Invoker` types, `.Data` event field renamed to `.Output`) propagate to this module's exported surface. See the root `openbindings-go` CHANGELOG for the full table. No format-specific behavior changed.

**Invocation handle migration.** `InvokeBinding` now returns the cardinality-agnostic `Invocation[I, O]` handle (input via `Write`, outputs via `Outputs().Read` to `io.EOF`/terminal) instead of `(<-chan InvocationOutput, error)`. Auth moved off the removed `SecurityMethod`/`ContextStore` path to context (credentials in the binding context; `CONTEXT_REQUIRED` where the source declares requirements). Error codes use the new SCREAMING wire values. See the root CHANGELOG for the full shape.

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
