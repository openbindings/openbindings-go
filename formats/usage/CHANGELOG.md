# Changelog

## 0.2.0 (working draft)

**Binding conventions v2** (design: the wire-conformance loop's transport
proposal). The format's conventions — now versioned independently of the
`usage@` artifact token, with the README's Conventions section as the
authority — gain transport members and a defined value grammar:

- **`x-usage` binding member.** `delivery` routes a transport-input field off
  argv: `"stdin"` (value on child stdin, `-` in its argv slot) or `"file"`
  (value materialized to a temp file, path in its slot); strings write raw,
  other values as compact JSON; at most one stdin field. `stdout` declares
  zero-exit stdout: `"json"` (strict parse, numbers included, empty = `null`,
  parse failure = terminal error) or `"text"` (the raw string is the output
  value). Members are read from the selected binding entry
  (`BindingInvocationArgs.Binding`); absent members preserve prior behavior.
- **Breaking — stderr leaves the output value.** The `{data, stderr}`
  envelope is gone, and the heuristic `{stdout}` wrap no longer carries a
  `stderr` member: captured stderr rides trailing metadata (`x-stderr`,
  alongside `x-exit-code`). Non-zero-exit error details are unchanged.
- **Breaking — canonical JSON argv encoding.** Non-string values render onto
  argv as compact JSON: objects/arrays no longer print as Go `map[k:v]`, and
  large floats no longer render in exponent form.

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the root SDK (executor → invoker rename, `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`, `InvocationOptions` folded into `BindingContext`, `InterfaceClient` removed in favor of codegen-emitted `<Name>Invoker` types, `.Data` event field renamed to `.Output`) propagate to this module's exported surface. See the root `openbindings-go` CHANGELOG for the full table. No format-specific behavior changed.

**Invocation handle migration.** `InvokeBinding` now returns the cardinality-agnostic `Invocation[I, O]` handle (input via `Write`, outputs via `Outputs().Read` to `io.EOF`/terminal) instead of `(<-chan InvocationOutput, error)`. Auth moved off the removed `SecurityMethod`/`ContextStore` path to context (credentials in the binding context; `CONTEXT_REQUIRED` where the source declares requirements). Error codes use the new SCREAMING wire values. See the root CHANGELOG for the full shape.

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
