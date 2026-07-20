# Changelog

## 0.2.0 (working draft)

### Added

- **Configurable delivery-unit bound**: the invocation lane's captured
  stdout honors `BindingInvocationArgs.MaxDeliveryUnitBytes` (default
  `openbindings.DefaultMaxDeliveryUnitBytes`, 10 MiB — the previous fixed
  cap). Overflow error identity is unchanged (`ERR_EXECUTION_FAILED`, same
  message template, dynamic value). The stderr tail, `x-stderr` trailer
  bound, artifact-fetch guard, and field-routing cap stay fixed (not
  delivery units).

**The openbindings.usage binding-unit format** (ratified 2026-07-06;
authority: the companion spec at spec/binding-specs/usage/openbindings.usage.md).
The format's source is now a JSON wrapper document embedding a pristine jdx
usage.kdl plus per-command invocation units {command, delivery, stdout,
exit}; OBI binding entries carry only {operation, source, ref} with refs as
JSON Pointers (#/units/<name>). This supersedes both the pre-design
heuristic transport and the interim x-usage extension member (which never
shipped). Highlights:

- Invoker claims `openbindings.usage@^0.1.0`; bare `usage@2.x` artifacts
  are derivation input only (the synthesizer wraps them with trivial,
  deterministically named units).
- Delivery modes `stdin-dash` / `stdin` (slotless) / `file`, with static
  load-time validation against the artifact (value-taking-slot arity,
  choices, slotless-means-no-slot; per-unit failure granularity).
- Stdout modes `json` / `text` (default; trailing newlines stripped) /
  `none` — the shape-sniffing heuristic and its {"stdout": ...} wrap are
  REMOVED.
- Exit classification `{"ok": [0, 1]}` with fail-closed edges (non-empty,
  0-255, signal death never ok) and `values` literals for status-only CLIs
  (requires stdout "none").
- spec.format declares the artifact version (checked at load); spec.hash is
  required (and verified) in location-only mode; fail-closed unknown-member
  policy at every level, x- members ignored, description allowed.
- Diagnostics: x-stderr trailer now carries the LAST 64 KiB (tail), with
  x-stderr-truncated; stderr capture is a tail ring that never fails a
  successful invocation; the delivery cap refuses as a validation failure.

**Binding conventions v2** (design: the wire-conformance loop's transport
proposal). The format's conventions — now versioned independently of the
`usage@` artifact token, with the README's Conventions section as the
authority — gain transport members and a defined value grammar:

- **`x-usage` binding member.** `delivery` routes a transport-input field off
  argv: `"stdin"` (value on child stdin, `-` in its argv slot when the field
  maps to a flag/arg, nothing emitted when it maps to none — the no-operand
  filter class) or `"file"` (value materialized to a temp file, path in its
  slot); strings write raw, other values as compact JSON; a present-but-null
  routed field is a no-op like the argv null rule; at most one declared
  stdin field (static rule); routed values are capped at 10 MiB. `stdout`
  declares zero-exit stdout: `"json"` (strict parse, numbers included,
  empty/whitespace-only = `null`, parse failure = terminal error) or
  `"text"` (stdout with trailing newlines stripped — command-substitution
  semantics). `exit` classifies success codes (`{"ok": [0, 1]}` for the
  diff(1) class; default `[0]`). Members are read from the selected binding
  entry (`BindingInvocationArgs.Binding`); absent members preserve prior
  behavior.
- **Version-skew rules.** Parsing of `x-usage` is fail-closed: unknown
  members and unknown mode values are invocation errors, never ignored. A
  tool implementing the format without `x-usage` support must treat
  bindings carrying it as unactionable (OBI-T-09 exclusion), not invoke
  with the member ignored. The package exports `ConventionsVersion`.
- **Breaking — stderr leaves the output value.** The `{data, stderr}`
  envelope is gone, and the heuristic `{stdout}` wrap no longer carries a
  `stderr` member: captured stderr rides trailing metadata (`x-stderr`,
  bounded to 64 KiB with an `x-stderr-truncated` marker, alongside
  `x-exit-code`). stderr capture overflow no longer fails a successful
  invocation (truncate and mark; stdout overflow remains fatal).
  Non-ok-exit error details are unchanged.
- **Breaking — canonical JSON argv encoding.** Non-string values render onto
  argv as compact JSON: objects/arrays no longer print as Go `map[k:v]`, and
  large floats no longer render in exponent form.

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the root SDK (executor → invoker rename, `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`, `InvocationOptions` folded into `BindingContext`, `InterfaceClient` removed in favor of codegen-emitted `<Name>Invoker` types, `.Data` event field renamed to `.Output`) propagate to this module's exported surface. See the root `openbindings-go` CHANGELOG for the full table. No format-specific behavior changed.

**Invocation handle migration.** `InvokeBinding` now returns the cardinality-agnostic `Invocation[I, O]` handle (input via `Write`, outputs via `Outputs().Read` to `io.EOF`/terminal) instead of `(<-chan InvocationOutput, error)`. Auth moved off the removed `SecurityMethod`/`ContextStore` path to context (credentials in the binding context; `CONTEXT_REQUIRED` where the source declares requirements). Error codes use the new SCREAMING wire values. See the root CHANGELOG for the full shape.

- **A present non-object input is refused loudly (usage@1 §9.1)** instead of
  silently running the bare command (a typed-nil map slipped past the object
  guard); the routing/HookTable subsystem gained direct tests.

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
