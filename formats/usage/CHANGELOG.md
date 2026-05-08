# Changelog

## 0.2.0 (working draft)

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the executor->invoker rename (`BindingExecutor` -> `BindingInvoker`, `ExecuteBinding(...)` -> `InvokeBinding(...)`, `NewExecutor` -> `NewInvoker`, `ExecutionOptions` -> `InvocationOptions`, etc.) propagate to this module's exported surface; see the root `openbindings-go` CHANGELOG for the full rename table. No format-specific behavior changed.

## 0.1.0 — 2026-03-31

Initial public release.

- Usage-spec binding executor (`usage@^2.0.0`) for CLI tool execution
- Lossless KDL parsing with ergonomic helper views
- Interface creation from usage-spec documents (commands, flags, args to operations)
- CLI argument building from OBI input (flags, positional args, variadic, count, negate)
- Spec caching with thread-safe read/write locking
- Spec validation with configurable strictness
- Support for `exec:` artifact locations (resolve usage spec from CLI `--help` output)
- Direct binary execution via metadata hint
