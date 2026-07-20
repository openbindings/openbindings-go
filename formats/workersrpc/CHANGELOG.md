# Changelog

## 0.2.0 (working draft)

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The module
remains a Go-side stub — dispatch still requires the Workers runtime and
`@openbindings/workers-rpc`.

### Changed

- **Renamed binding "executor" terminology to "invoker"** to track the spec
  0.2.0 rename in `openbindings-go`: `Executor`/`NewExecutor`/`ExecuteBinding`
  → `Invoker`/`NewInvoker`/`InvokeBinding` (`executor.go` → `invoker.go`).
  See the root `openbindings-go` CHANGELOG for the full rename table.
- **Renamed the interface "creator" to "synthesizer"**:
  `Creator`/`NewCreator`/`CreateInterface` →
  `Synthesizer`/`NewSynthesizer`/`SynthesizeInterface`. The stub still
  refuses synthesis with an error explaining that workers-rpc OBIs are
  hand-authored.
- **Invocation handle migration.** `InvokeBinding` now synchronously returns
  the cardinality-agnostic `Invocation[any, any]` handle instead of
  `(<-chan StreamEvent, error)`; the stub returns an already-errored handle
  (terminal `ERR_SOURCE_CONFIG_ERROR`) whose message points at
  `@openbindings/workers-rpc`. See the root CHANGELOG for the full shape.
- **Adopted exact binding-specification identifiers**: advertisement moved
  from `Formats() []FormatInfo` to `BindingSpecs() []BindingSpecInfo`, and
  the exported constant `FormatToken` was renamed to `BindingSpec`. The token
  itself remains the draft `workers-rpc@^1.0.0` until the format's binding
  specification is promoted.
- README modernization pass: repo split, synthesizer vocabulary, and the
  codegen rationale for why the Go-side stub exists.

## 0.1.0 — 2026-04-15

Initial public release.

Go-side stub registration for the `workers-rpc@^1.0.0` binding format: makes
the format token recognized by Go tooling (validate, diff, codegen) while
directing actual dispatch to `@openbindings/workers-rpc` inside the Cloudflare
Workers runtime.
