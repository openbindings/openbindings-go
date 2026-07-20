# Changelog

## 0.2.0 (working draft)

### Added

- **Delivery-unit bound named exclusion documented** (README "Resource
  bounds"): the official MCP Go SDK exposes no read-bound seam, so
  `BindingInvocationArgs.MaxDeliveryUnitBytes` wires no read site in this
  format; re-examine when the SDK exports a message-size limit.

This release tracks the spec 0.2.0 alignment of `openbindings-go`, including the rewritten invoker core. See the root `openbindings-go` CHANGELOG for the full table.

### Changed

- **Multi-block tool content returns the verbatim content array (MCP-P-05)**
  instead of a `"\n"`-joined string — the value's type changes for multi-text
  results.

- **Pagination is bounded**: a repeated or endless `nextCursor` sequence
  refuses with `ERR_PROTOCOL` instead of looping.

- **Breaking**: conformance to the published `openbindings.mcp@1` binding specification:
  - **Pinned listings** (MCP-D-01): a source may carry `content` — pagination-exhausted entity arrays under `tools`/`resources`/`resourceTemplates`/`prompts` in the 2025-11-25 result shapes. A pin makes ref resolution offline and displaces the list requests entirely; stray members (`nextCursor`, `_meta`, anything else) are refused loudly (`ERR_SOURCE_LOAD_FAILED`).
  - **Resolution before dispatch** (MCP-P-02, §7): every ref resolves byte-exactly against the pinned or live listing (each list request followed to pagination exhaustion, capability-gated) before the entity request is sent; unresolvable and ambiguous refs are refused (`ERR_REF_NOT_FOUND`). An unknown tool no longer surfaces as the server's JSON-RPC error.
  - **Progress solicitation is a named configuration point, default OFF** (§9.2/§9.3): `tools/call` carries no `progressToken` and the output stream is the result alone unless solicited via per-invocation `context.configuration.solicit` or the new `WithSolicitProgress` option (per-invocation wins). When solicited, progress values are the notification's params minus `progressToken`, presence-preserving — an explicit `total: 0` now survives (captured from the raw event stream; the previous typed-struct lane dropped it) — and late correlated notifications are discarded.
  - **Resource results are always the array of decoded contents items** (§9.3): no single-item unwrap, `contents: []` yields `[]`; a `blob` item decodes structurally to its Base64 string whatever `mimeType` declares; `text` items keep the declared-mimeType header rule.
  - **Input mapping** (§9.1): an absent input omits the `arguments` member entirely on `tools/call` and `prompts/get` (never `arguments: {}`); prompt arguments and resource-template variables must be strings and are refused pre-dispatch, never coerced (the previous `fmt.Sprintf` stringification of prompt arguments is gone); a template-input member naming an undeclared variable is refused.
  - **RFC 6570 template expansion** (§9.1): a resource template's input is the object of its variables, expanded into the target URI before `resources/read` (unsupplied variables follow undefined-value expansion); the raw template string is no longer sent as the URI.
  - **Synthesis alignment**: static resource operations declare no input (the URI is the ref); resource-template operations declare the template's variables as string properties (`additionalProperties: false`, none required) instead of a const `uriTemplate` input.
- **Breaking**: `InvokeBinding` now takes `*openbindings.BindingInvocationArgs` and returns a cardinality-agnostic `openbindings.Invocation[any, any]` handle synchronously (no error return, no event channel). Tool and prompt arguments flow through the handle's `Write` channel as the operation's single input message; resource reads take no input. Outputs and the terminal error are read from `Outputs()`.
- **Breaking**: progress notifications are first-class outputs: a tool that emits `notifications/progress` yields each progress event as an output ahead of the final result, which is always the last output.
- **Breaking**: error codes are SCREAMING_SNAKE (`ERR_INVALID_REF`, `ERR_AUTH_REQUIRED`, ...). Invalid tool/prompt input maps to `ERR_VALIDATION_FAILED` (`invalid_input` is gone). JSON-RPC errors map to `ERR_EXECUTION_FAILED` with `{code, data}` in details; HTTP 401/403 map to `ERR_AUTH_REQUIRED`/`ERR_PERMISSION_DENIED` with the status in details. Application-level tool errors (`CallToolResult.isError`) are now terminal `ERR_EXECUTION_FAILED` instead of a success event.
- **Breaking**: the in-invoker auth retry, `ContextStore` lookup, and platform-callback paths are removed; context resolution happens above the binding. Credentials are applied from `args.Context` only.
- Pre-dispatch failures (bad ref, missing or non-HTTP endpoint, non-object input) terminate the handle before any network side effect; a missing or non-HTTP endpoint is now `ERR_SOURCE_CONFIG_ERROR`.
- The call's HTTP response headers now surface as the invocation's leading metadata (`Header`).

## 0.1.0 — 2026-03-31

Initial public release.

- MCP binding executor (`mcp@2025-11-25`) via Streamable HTTP transport
- Tool discovery and execution (`tools/list`, `tools/call`)
- Resource reading for static resources and URI templates (`resources/read`)
- Prompt retrieval with argument mapping (`prompts/get`)
- Structured content support for tool results (`structuredContent`, `outputSchema`)
- Content type handling (text, image, audio, resource links, embedded resources)
- Credential application (bearer, apiKey) as HTTP headers
- Interface creation from MCP server capabilities with deterministic output
