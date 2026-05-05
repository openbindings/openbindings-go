# Changelog

## 0.2.0 (working draft)

### Changed

- **Renamed binding "executor" terminology to "invoker" / "invoke"** to align with the OpenBindings spec 0.2.0 rename. Pre-1.0 hard rename, no deprecated aliases. Both layers — the per-format component and the orchestrator — use the `Invoker` noun, with the verb `Invoke` shared across them.
  - Types: `BindingExecutor` → `BindingInvoker`; `OperationExecutor` → `OperationInvoker`; per-format `*.Executor` → `*.Invoker` (e.g., `openapi.Executor` → `openapi.Invoker`); `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`; `OperationExecutionInput` → `OperationInvocationInput`; `ExecuteOutput`/`ExecuteError`/`ExecutionOptions` → `InvocationOutput`/`InvocationError`/`InvocationOptions`.
  - Methods: `BindingExecutor.ExecuteBinding(...)` → `BindingInvoker.InvokeBinding(...)`; `OperationExecutor.ExecuteOperation(...)` → `OperationInvoker.Invoke(...)`; `OperationExecutor.AddBindingExecutor(...)` → `OperationInvoker.AddBindingInvoker(...)`; `InterfaceClient.Execute(...)`/`ExecuteWithOptions(...)` → `InterfaceClient.Invoke(...)`/`InvokeWithOptions(...)`.
  - Constructors: `NewOperationExecutor` → `NewOperationInvoker`; per-format `NewExecutor` → `NewInvoker`.
  - Helpers: `CombineExecutors` → `CombineInvokers`; `ErrNoExecutor` → `ErrNoInvoker`.
  - File renames: `executor.go` → `binding_invoker.go`, `operation_executor.go` → `operation_invoker.go`, `executor_types.go` → `invoker_types.go`; per-format `executor.go` → `invoker.go`, `execute.go` → `invoke.go`.

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
