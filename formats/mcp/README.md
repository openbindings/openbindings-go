# formats/mcp

MCP (Model Context Protocol) binding invoker and interface synthesizer for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to invoke operations against MCP servers and synthesize OBI documents from them. It connects to MCP servers via the Streamable HTTP transport, discovers tools, resources, and prompts, and executes them through the MCP JSON-RPC protocol.

See the [spec](https://github.com/openbindings/spec) and the [invocation pattern](https://openbindings.com/spec/invocation-pattern) for how binding invokers and interface synthesizers fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/mcp
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    mcpbinding "github.com/openbindings/openbindings-go/formats/mcp"
)

opInv := openbindings.NewOperationInvoker(mcpbinding.NewInvoker())
```

The invoker declares `openbindings.mcp@1` (the [published binding specification](https://github.com/openbindings/spec), defined against MCP revision 2025-11-25).

### Invoke a binding

```go
invoker := mcpbinding.NewInvoker()
defer invoker.Close()

call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        BindingSpec: "openbindings.mcp@1",
        Location:    "https://mcp.example.com/mcp",
    },
    Ref:     "tools/get_weather",
    Context: map[string]any{"bearerToken": "tok_123"},
})

// Tool and prompt arguments are the operation's single input message.
if err := call.Write(ctx, map[string]any{"city": "Seattle"}); err != nil {
    log.Fatal(err)
}

// The output stream is the result value alone unless progress is solicited
// (the `solicit` configuration point, default off): opt in per invocation
// via context {"configuration": {"solicit": true}} or per invoker via
// WithSolicitProgress(true), and progress values then precede the result.
out, err := openbindings.Single(ctx, call.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

Static resource reads (`resources/<uri>`) take no input: skip the `Write`
and read outputs directly. A resource template (`resources/<template>`)
takes one input — an object of its RFC 6570 variables, every value a
string — and the invoker expands the template before `resources/read`.

### Synthesize an interface from an MCP server

```go
synth := mcpbinding.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        BindingSpec: "openbindings.mcp@1",
        Location:    "https://mcp.example.com/mcp",
    }},
})
// iface is a fully-formed OBInterface with operations, bindings, and sources
```

## The binding specification

This package implements **`openbindings.mcp@1`**, the openbindings project's published binding specification for MCP (`spec/binding-specs/mcp/openbindings.mcp.md`), defined against MCP revision 2025-11-25. The normative answers live there; the highlights as implemented here:

### Ref format (MCP-D-03)

`<entity>/<remainder>` — the entity family followed by the identity, matched byte-exactly against the (pagination-exhausted) listing before dispatch; unresolvable and ambiguous refs are refused (`ERR_REF_NOT_FOUND`):

- `tools/get_weather` - a tool's `name`
- `resources/file:///data.csv` - a resource's `uri`
- `resources/users/{id}` - a resource template, addressed by its template string
- `prompts/summarize` - a prompt's `name`

### Source expectations

- **`location`** (MCP-D-02): The MCP server's Streamable HTTP endpoint URL (HTTP/HTTPS), required.
- **`content`** (MCP-D-01): Optional **pinned listing** — a JSON object of pagination-exhausted entity arrays under `tools`/`resources`/`resourceTemplates`/`prompts` in the 2025-11-25 result shapes. A pin makes ref resolution offline-checkable and displaces the list requests entirely; stray members (`nextCursor`, `_meta`, anything else) invalidate it loudly. Without `content`, the listing is obtained live, each list request followed to pagination exhaustion.

### Credential application

Credentials from the invocation context are applied as HTTP headers:

- `bearerToken`: `Authorization: Bearer <token>`
- `apiKey`: `Authorization: ApiKey <key>`
- `basic`: `Authorization: Basic <base64(user:pass)>`

Context `headers` and `cookies` are forwarded. No security metadata in MCP; an HTTP 401 surfaces as a terminal `ERR_AUTH_REQUIRED` and context resolution happens above the binding.

### Custom HTTP client

The Streamable HTTP transport always installs an internal header injector (needed for per-call response capture). To run that injector over your own transport, pass a base `*http.Client`:

```go
invoker := mcp.NewInvoker(mcp.WithHTTPClient(myClient))       // invocation lane
synth := mcp.NewSynthesizer(mcp.WithSynthesizerHTTPClient(myClient)) // discovery lane
```

The client's `Transport` becomes the round-trip base beneath the injector; its `Timeout`, redirect policy, and cookie jar are preserved. Use it for a corporate proxy, mTLS client certificates, or a custom CA pool. Discovery connects live, so a setup the invocation lane needs is needed on the synthesizer too. This is the MCP counterpart to the openapi invoker's `NewInvokerWithClient`.

### Consumer hooks

This format consults the **decode axis** of the consumer hooks seam (`InvokeHooks`) on its text lanes; classification and routing stay protocol-native (`isError` decides success; MCP has no field routing), so `Classify` and `Route` hooks have no effect.

The decode lanes are content-independent, per the specification's `decode` configuration point (§9.3):

- **Tool results.** `structuredContent` is MCP's declared structured lane (2025-11-25: servers MUST conform it to `outputSchema`) and wins outright. Absent it, a single text content is a **string, verbatim** — MCP defines JSON-serialized-into-text as the backwards-compatibility *shadow* of `structuredContent`, so parsing it is a consumer choice made through a `DecodeOutput` hook, never a payload sniff. Other content shapes pass through as generic values.
- **Resources.** The output value is **always the array** of decoded contents items, in order (`contents: []` yields `[]`); a bare single value is an `outputTransform` concern. Each item decodes structurally first — a `blob` item passes as its Base64 string whatever `mimeType` it declares — and a `text` item decodes by its declared `mimeType`, exactly like the HTTP header rule: `application/json`/`+json` parses strictly (a parse failure is a loud error, never a silent fall-through); anything else is text.

Success provenance rides the `x-ob-decode` trailer stamp (`structuredContent`, `text`, `contents/declared`, or `hook`); classification stamps `protocol/isError`.

### Progress solicitation (the `solicit` configuration point)

Default **off**: no `progressToken` rides `tools/call` and the output stream is the result value alone. Opt in per invocation (`context.configuration.solicit: true`) or per invoker (`WithSolicitProgress(true)`); per-invocation wins. When solicited, each correlated `notifications/progress` emits one output value — the notification's params minus `progressToken`, presence-preserving (an explicit `total: 0` survives) — ahead of the result, which is always last; correlated notifications arriving after the result are discarded.

### Entity type mapping

| Entity | Ref format | Input | Output |
|--------|-----------|-------|--------|
| **Tool** | `tools/<name>` | Tool's `inputSchema` | Tool's `outputSchema` or content array |
| **Resource** | `resources/<uri>` | None (the URI is the ref) | Array of decoded contents items |
| **Resource template** | `resources/<template>` | RFC 6570 variables (string-typed) | Array of decoded contents items |
| **Prompt** | `prompts/<name>` | Prompt arguments (string-typed) | `{messages: [...]}` |

### Interface synthesis

- Each MCP entity type discovered via capability negotiation
- Tools, resources, resource templates, and prompts sorted alphabetically
- Tool input/output schemas preserved as-is from the MCP server
- Static resources declare no input; template inputs are the template's RFC 6570 variables as string properties
- No security metadata exposed

## How it works

### Invocation flow

`InvokeBinding` returns the `Invocation` handle synchronously; the MCP work runs on its own goroutine:

1. Parses the ref to determine entity type: `tools/`, `resources/`, or `prompts/` (a bad ref, a missing/non-HTTP endpoint, or an invalid pinned listing terminates the handle before any network I/O)
2. Resolves the ref against the listing before dispatch — offline against a pinned listing, otherwise against the live capability-gated, pagination-exhausted listing after the handshake; unresolvable or ambiguous refs are refused
3. Reads the operation's single input message from the handle (tools, prompts, and resource templates — whose input is validated as string variables and expanded per RFC 6570); static resource reads take no input. An absent input omits the `arguments` member entirely
4. Acquires a pooled MCP session (Streamable HTTP transport, JSON-RPC over HTTP), applying credentials from the context as HTTP headers
5. Calls the appropriate MCP method (`tools/call`, `resources/read`, or `prompts/get`)
6. Emits the result and closes the output side; when progress was solicited, correlated `notifications/progress` events emit as outputs ahead of the result

The call's HTTP response headers surface as the invocation's leading metadata (`Header`). Errors map to terminal invocation errors: JSON-RPC errors → `ERR_EXECUTION_FAILED` with the `{code, data}` in details, HTTP 401/403 → `ERR_AUTH_REQUIRED`/`ERR_PERMISSION_DENIED`, connection failures → `ERR_CONNECT_FAILED`.

### Session pooling

The invoker pools MCP sessions by server URL + auth headers. Multiple `InvokeBinding` calls against the same server share one session (one `initialize` handshake). A session stays alive while any invocation uses it, then idles for 30 seconds (configurable via `WithIdleTimeout`) before closing. Call `Invoker.Close` to shut down all pooled sessions.

### Credential application

Credentials are applied as HTTP headers in priority order:

- **bearer**: Sets `Authorization: Bearer <token>` from `bearerToken` context field
- **apiKey**: Sets `Authorization: ApiKey <key>` from `apiKey` context field
- **basic**: Sets `Authorization: Basic <credentials>` from the `basic` context field

Context `headers` and `cookies` are also forwarded to the MCP transport.

### Interface synthesis

Connects to an MCP server and discovers capabilities by:

- Reading server info (name, version, title) from the initialization handshake
- Listing tools (if the server declares tool capabilities)
- Listing resources and resource templates (if declared)
- Listing prompts (if declared)
- Sorting all entities alphabetically for deterministic output
- Generating `tools/name`, `resources/uri`, or `prompts/name` refs for each binding

### MCP entity type mapping

MCP exposes three entity types, each mapped to OBI operations differently:

| Entity | Ref format | Input | Output | Notes |
|--------|-----------|-------|--------|-------|
| **Tool** | `tools/<name>` | Tool's `inputSchema` (JSON Schema) | Tool's `outputSchema` or content array | Closest analog to a traditional API operation |
| **Resource** | `resources/<uri>` | None (the URI is the ref) | Array of decoded contents items | Read-only data access; URI is predetermined by the binding |
| **Resource template** | `resources/<template>` | The template's RFC 6570 variables (string-typed) | Array of decoded contents items | Parameterized read; the invoker expands the template |
| **Prompt** | `prompts/<name>` | Prompt arguments (string-typed) | `{messages: [...], description: "..."}` | Returns LLM message sequences, not API results |

Tools map cleanly to OBI operations. Resources and prompts have different semantics -- resources are read-only data access points, and prompts return LLM-oriented message templates rather than traditional API results.

## License

Apache-2.0
