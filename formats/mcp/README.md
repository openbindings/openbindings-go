# mcp-go

MCP (Model Context Protocol) binding executor and interface creator for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to execute operations against MCP servers and synthesize OBI documents from them. It connects to MCP servers via the Streamable HTTP transport, discovers tools, resources, and prompts, and executes them through the MCP JSON-RPC protocol.

See the [spec](https://github.com/openbindings/spec) and [pattern documentation](https://github.com/openbindings/spec/tree/main/patterns) for how executors and creators fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/mcp
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationExecutor

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    mcpbinding "github.com/openbindings/openbindings-go/formats/mcp"
)

exec := openbindings.NewOperationExecutor(mcpbinding.NewExecutor())
```

The executor declares `mcp@2025-11-25` -- it handles MCP servers implementing the 2025-11-25 spec revision.

### Execute a binding

```go
executor := mcpbinding.NewExecutor()

ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
    Source: openbindings.ExecuteSource{
        Format:   "mcp@2025-11-25",
        Location: "https://mcp.example.com/sse",
    },
    Ref:     "tools/get_weather",
    Input:   map[string]any{"city": "Seattle"},
    Context: map[string]any{"bearerToken": "tok_123"},
})
for ev := range ch {
    if ev.Error != nil {
        log.Fatal(ev.Error.Message)
    }
    fmt.Println(ev.Data)
}
```

### Create an interface from an MCP server

```go
creator := mcpbinding.NewCreator()
iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
    Sources: []openbindings.CreateSource{{
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

Credentials are applied as HTTP headers:

- `bearerToken`: `Authorization: Bearer <token>`
- `apiKey`: `Authorization: ApiKey <key>`

Execution options headers and cookies are forwarded. No security metadata in MCP; auth retry handles 401 at runtime.

### Entity type mapping

| Entity | Ref format | Input | Output |
|--------|-----------|-------|--------|
| **Tool** | `tools/<name>` | Tool's `inputSchema` | Tool's `outputSchema` or content array |
| **Resource** | `resources/<uri>` | Fixed URI (const in schema) | Resource content |
| **Resource template** | `resources/<template>` | Fixed URI template (const) | Resource content |
| **Prompt** | `prompts/<name>` | Prompt arguments (string-typed) | `{messages: [...]}` |

### Interface creation

- Each MCP entity type discovered via capability negotiation
- Tools, resources, resource templates, and prompts sorted alphabetically
- Tool input/output schemas preserved as-is from the MCP server
- Resource URIs stored as const input properties
- No security metadata exposed

## How it works

### Execution flow

1. Parses the ref to determine entity type: `tools/`, `resources/`, or `prompts/`
2. Opens a new MCP session via Streamable HTTP transport (JSON-RPC over HTTP)
3. Applies credentials from the context as HTTP headers (bearer, apiKey)
4. Calls the appropriate MCP method (`tools/call`, `resources/read`, or `prompts/get`)
5. Converts the MCP response to an OpenBindings stream event
6. Closes the session

Each execution creates a fresh MCP session. MCP sessions are stateful -- they begin with capability negotiation, support bidirectional notifications, and have a formal shutdown lifecycle. Reusing sessions across independent `ExecuteBinding` calls would conflate these session semantics, so each call gets its own session. Go's default HTTP transport pools the underlying TCP connections.

### Credential application

Credentials are applied as HTTP headers in priority order:

- **bearer**: Sets `Authorization: Bearer <token>` from `bearerToken` context field
- **apiKey**: Sets `Authorization: ApiKey <key>` from `apiKey` context field

Execution options headers are also forwarded to the MCP transport.

### Interface creation

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
