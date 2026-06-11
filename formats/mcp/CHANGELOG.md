# Changelog

## 0.2.0 (working draft)

This release tracks the spec 0.2.0 alignment of `openbindings-go`, including the rewritten invoker core. See the root `openbindings-go` CHANGELOG for the full table.

### Changed

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
