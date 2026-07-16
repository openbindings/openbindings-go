# Changelog

## 0.2.0 (working draft)

**Conformance to the published `openbindings.openapi@1` binding specification.** The invoker now implements the normative rules end to end; behavior that predated the specification and diverged from it changed:

- **Input mapping (OAPI-P-02/P-03).** Parameter serialization follows the OAS `style`/`explode`/`allowReserved` rules wholesale (matrix/label/simple, form/spaceDelimited/pipeDelimited/deepObject, per-location defaults, `content`-form parameters). Cross-location same-name declarations refuse as unflattenable; unmatched input fields refuse loudly when no request body is declared (previously silently sent as a body); a missing declared path parameter refuses pre-dispatch; non-object body schemas ride the synthetic `body` property, unwrapped at the wire.
- **Request/response media (OAPI-P-04).** Request media selection follows the declared preference order (exact JSON → least `+json` → multipart → urlencoded → `text/plain` for string bodies) with pre-dispatch refusal of out-of-family-only declarations. Multipart parts are binary-signaled per edition (3.0 `format: binary`; 3.1 `contentMediaType`/`contentEncoding`), with caller strings decoded per declared `contentEncoding` or Base64 (previously required Go `[]byte`, which still passes through raw as an in-process convenience). urlencoded bodies serialize per the OAS `encoding` rules. The `Accept` header advertises the declared success-response media (previously a fixed `application/json, text/event-stream`).
- **Servers (OAPI-P-05).** The OAS effective server list (operation → path item → document → implied `/`) with variable substitution; `context.configuration.server` is the named configuration point (entry selection by url/index, variable values, outright `baseUrl`); relative server URLs resolve against the source's location per RFC 3986. `metadata.baseURL` still works, below the configuration point.
- **Ref (OAPI-D-03).** `#/paths/<escaped-path>/<method>` is enforced exactly: lowercase method (an uppercase method is refused, never case-folded), `#/paths/` prefix required, single escaped path token.
- **Interaction shape (OAPI-P-06).** Streaming capability is static — declared `text/event-stream` on a success response — and the response's `Content-Type` framing selects among declared shapes; an undeclared event-stream response is an `ERR_PROTOCOL` failure (previously any 2xx SSE response silently streamed). SSE extraction is WHATWG-exact: empty-data/fields-only events emit nothing, incomplete final events are discarded, CR/CRLF line endings and the leading BOM are handled, `id` follows lastEventId semantics.
- **Decode (OAPI-P-07).** The text lane honors the `charset` parameter (UTF-8 default) and refuses invalid sequences loudly.
- **Channel assembly (OAPI-P-10).** Declared cookie parameters and cookie-riding credentials merge into one `Cookie` header (parameters in declaration order, credentials appended); credential/parameter name collisions on a channel refuse pre-dispatch.
- **Loading (OAPI-P-01, §3–§6).** The artifact's `openapi` field discriminates the accepted 3.0.x/3.1.x lines (Swagger 2.0 and other versions refuse loudly); duplicate YAML mapping keys refuse; embedded content without a location must be self-contained (relative external `$ref`s fail with a readable error instead of resolving against the process working directory).

This release tracks the spec 0.2.0 alignment of `openbindings-go`. The public API breaks from the root SDK (executor → invoker rename, `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`, `InvocationOptions` folded into `BindingContext`, `InterfaceClient` removed in favor of codegen-emitted `<Name>Invoker` types, `.Data` event field renamed to `.Output`) propagate to this module's exported surface. See the root `openbindings-go` CHANGELOG for the full table. No format-specific behavior changed.

**Invocation handle migration.** `InvokeBinding` now returns the cardinality-agnostic `Invocation[I, O]` handle (input via `Write`, outputs via `Outputs().Read` to `io.EOF`/terminal) instead of `(<-chan InvocationOutput, error)`. Auth moved off the removed `SecurityMethod`/`ContextStore` path to context (credentials in the binding context; `CONTEXT_REQUIRED` where the source declares requirements). Error codes use the new SCREAMING wire values. See the root CHANGELOG for the full shape.

## 0.1.0 — 2026-03-31

Initial public release.

- OpenAPI 3.x binding executor (`openapi@^3.0.0`)
- HTTP request construction from OpenAPI specs (path, query, header, body parameter routing)
- Security scheme-driven credential application (bearer, basic, apiKey with spec-declared placement)
- Credential fallback when no security schemes defined
- Document caching with thread-safe read/write locking
- Interface creation from OpenAPI documents (operations, bindings, schemas, refs)
- JSON Pointer ref generation and parsing (RFC 6901)
- Base URL resolution from spec servers with relative URL support
