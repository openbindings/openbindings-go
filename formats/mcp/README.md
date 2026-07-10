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

The invoker declares `mcp@2025-11-25` -- it handles MCP servers implementing the 2025-11-25 spec revision.

### Invoke a binding

```go
invoker := mcpbinding.NewInvoker()
defer invoker.Close()

call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        Format:   "mcp@2025-11-25",
        Location: "https://mcp.example.com/sse",
    },
    Ref:     "tools/get_weather",
    Context: map[string]any{"bearerToken": "tok_123"},
})

// Tool and prompt arguments are the operation's single input message.
if err := call.Write(ctx, map[string]any{"city": "Seattle"}); err != nil {
    log.Fatal(err)
}

// Tools that emit progress notifications yield multiple outputs (progress
// events first, the result last); use Single when you expect exactly one.
out, err := openbindings.Single(ctx, call.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

Resource reads (`resources/<uri>`) take no input: skip the `Write` and read
outputs directly.

### Synthesize an interface from an MCP server

```go
synth := mcpbinding.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        Format:   "mcp@2025-11-25",
        Location: "https://mcp.example.com/sse",
    }},
})
// iface is a fully-formed OBInterface with operations, bindings, and sources
```

## Conventions

These are non-normative conventions specific to the `mcp` binding format.

### Format token

`mcp@2025-11-25` (exact, date-versioned). Matches MCP servers implementing the 2025-11-25 spec revision.

### Ref format

`{entityType}/{name}` - the MCP entity type followed by the entity identifier:

- `tools/get_weather` - a tool
- `resources/file:///data.csv` - a resource (URI is the identifier)
- `resources/users/{id}` - a resource template
- `prompts/summarize` - a prompt

### Source expectations

- **`location`**: The MCP server endpoint URL (HTTP/HTTPS). Used with the Streamable HTTP transport (JSON-RPC over HTTP POST).
- **`content`**: Not used. MCP servers are discovered via session initialization at runtime.

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

An MCP tool result is self-describing: `structuredContent`/`content` carries the output and `isError` signals failure, both protocol-native. This format **does not consult the consumer hooks seam** (`InvokeHooks`); a `DecodeOutput`, `Classify`, or `Route` hook has no effect, and `ob plan` reports it as `not-consulted`.

### Entity type mapping

| Entity | Ref format | Input | Output |
|--------|-----------|-------|--------|
| **Tool** | `tools/<name>` | Tool's `inputSchema` | Tool's `outputSchema` or content array |
| **Resource** | `resources/<uri>` | Fixed URI (const in schema) | Resource content |
| **Resource template** | `resources/<template>` | Fixed URI template (const) | Resource content |
| **Prompt** | `prompts/<name>` | Prompt arguments (string-typed) | `{messages: [...]}` |

### Interface synthesis

- Each MCP entity type discovered via capability negotiation
- Tools, resources, resource templates, and prompts sorted alphabetically
- Tool input/output schemas preserved as-is from the MCP server
- Resource URIs stored as const input properties
- No security metadata exposed

## How it works

### Invocation flow

`InvokeBinding` returns the `Invocation` handle synchronously; the MCP work runs on its own goroutine:

1. Parses the ref to determine entity type: `tools/`, `resources/`, or `prompts/` (a bad ref or missing/non-HTTP endpoint terminates the handle before any network I/O)
2. Reads the operation's single input message from the handle (tools and prompts); resource reads take no input and close the input side on entry
3. Acquires a pooled MCP session (Streamable HTTP transport, JSON-RPC over HTTP), applying credentials from the context as HTTP headers
4. Calls the appropriate MCP method (`tools/call`, `resources/read`, or `prompts/get`)
5. Emits any `notifications/progress` events as outputs as they arrive, then the final result, then closes the output side

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
| **Resource** | `resources/<uri>` | Fixed URI (const in schema) | Resource content (text, binary, structured) | Read-only data access; URI is predetermined by the binding |
| **Resource template** | `resources/<template>` | Fixed URI template (const in schema) | Resource content | Parameterized read; template is predetermined |
| **Prompt** | `prompts/<name>` | Prompt arguments (string-typed) | `{messages: [...], description: "..."}` | Returns LLM message sequences, not API results |

Tools map cleanly to OBI operations. Resources and prompts have different semantics -- resources are read-only data access points, and prompts return LLM-oriented message templates rather than traditional API results.

## License

Apache-2.0
