# asyncapi-go

AsyncAPI 3.x binding executor and interface creator for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to execute operations against AsyncAPI specs and synthesize OBI documents from them. It supports HTTP/SSE for event streaming, HTTP POST for sending messages, and WebSocket for bidirectional communication. Credentials are applied via the spec's security schemes.

See the [spec](https://github.com/openbindings/spec) and [pattern documentation](https://github.com/openbindings/spec/tree/main/patterns) for how executors and creators fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/asyncapi
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationExecutor

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    asyncapi "github.com/openbindings/openbindings-go/formats/asyncapi"
)

exec := openbindings.NewOperationExecutor(asyncapi.NewExecutor())
```

The executor declares `asyncapi@^3.0.0` — it handles any AsyncAPI 3.x spec.

### Execute a binding

The `OperationExecutor` routes operations to this executor based on the OBI's source format. Direct use:

```go
executor := asyncapi.NewExecutor()
ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
    Source: openbindings.ExecuteSource{
        Format:   "asyncapi@3.0",
        Location: "https://api.example.com/asyncapi.json",
    },
    Ref:     "#/operations/sendMessage",
    Input:   map[string]any{"text": "hello"},
    Context: map[string]any{"bearerToken": "tok_123"},
})
for ev := range ch {
    if ev.Error != nil {
        log.Fatal(ev.Error.Message)
    }
    fmt.Println(ev.Data)
}
```

### Create an interface from an AsyncAPI spec

```go
creator := asyncapi.NewCreator()
iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
    Sources: []openbindings.CreateSource{{
        Format:   "asyncapi@3.0",
        Location: "https://api.example.com/asyncapi.json",
    }},
})
```

## Conventions

These are non-normative conventions specific to the `asyncapi` binding format.

### Format token

`asyncapi@^3.0.0` (caret range). Matches any AsyncAPI 3.x document.

### Ref format

JSON Pointer to the operation within the AsyncAPI document: `#/operations/<operationId>`

- `#/operations/sendMessage`
- `#/operations/orderUpdates`

### Source expectations

- **`location`**: URL or file path to the AsyncAPI JSON/YAML document.
- **`content`**: Inline AsyncAPI document.

### Credential application

Credentials are applied based on the AsyncAPI spec's `securitySchemes`:

- `http` + `bearer` / `httpBearer`: `Authorization: Bearer <token>` from `bearerToken`
- `http` + `basic` / `userPassword`: `Authorization: Basic <encoded>` from `basic`
- `apiKey`: Placed in header, query, or cookie as declared, from `apiKey`

For WebSocket connections, bearer tokens are sent in the first message body (browsers cannot set headers on WebSocket upgrades). Query-param apiKeys are appended to the WebSocket URL.

Falls back to bearer, then basic, then apiKey when no security schemes are defined.

### Interface creation

- Operations iterated alphabetically for deterministic output
- Input schemas from send operation payloads
- Output schemas from receive operation payloads and reply payloads
- Refs generated as `#/operations/<id>`

### Protocol dispatch

The executor determines the transport from the AsyncAPI spec's server protocol and the operation's action:

| Protocol | Receive (subscribe) | Send (publish) |
|----------|-------------------|----------------|
| HTTP/HTTPS | SSE streaming | POST (unary) |
| WS/WSS | WebSocket streaming | WebSocket streaming |

## How it works

### Execution flow

1. Loads and caches the AsyncAPI document (JSON or YAML, local or remote)
2. Parses the ref to extract the operation ID (`#/operations/sendMessage` -> `sendMessage`)
3. Resolves the server URL and protocol from the spec
4. Dispatches based on action and protocol:
   - **receive + http/https**: SSE event stream
   - **receive + ws/wss**: WebSocket stream
   - **send + http/https**: HTTP POST (unary)
   - **send + ws/wss**: WebSocket stream (bidirectional)

### Credential application

Credentials are applied based on the AsyncAPI spec's security configuration:

- **`http` + `bearer`**: Sets `Authorization: Bearer <token>` from `bearerToken` context field
- **`http` + `basic`**: Sets `Authorization: Basic <encoded>` from `basic.username`/`basic.password` context fields
- **`apiKey`**: Places the `apiKey` context field in the header, query param, or cookie as the spec declares
- **`httpBearer`**: Same as http+bearer
- **`userPassword`**: Maps to basic auth

When no security schemes are defined, falls back to bearer -> basic -> apiKey in that order.

For WebSocket connections, the bearer token is sent in the first message body (browsers cannot set headers on WebSocket upgrades). Query-param apiKeys are appended to the WebSocket URL.

### Interface creation

Converts an AsyncAPI 3.x document into an OBI by:
- Iterating operations sorted alphabetically for deterministic output
- Extracting input schemas from send operation payloads
- Extracting output schemas from receive operation payloads and reply payloads
- Generating `#/operations/<id>` refs for each binding
- Deriving operation keys from operation IDs

## Supported protocols

| Protocol | Receive (subscribe) | Send (publish) |
|----------|-------------------|----------------|
| HTTP/HTTPS | SSE streaming | POST (unary) |
| WS/WSS | WebSocket streaming | WebSocket streaming |

## License

Apache-2.0
