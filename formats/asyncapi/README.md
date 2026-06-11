# asyncapi-go

AsyncAPI 3.x binding invoker and interface creator for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to invoke operations against AsyncAPI specs and synthesize OBI documents from them. It supports HTTP/SSE for event streaming, HTTP POST for sending messages, and WebSocket for bidirectional communication. Credentials are applied via the spec's security schemes.

See the [spec](https://github.com/openbindings/spec) and [pattern documentation](https://github.com/openbindings/spec/tree/main/patterns) for how invokers and creators fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/asyncapi
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    asyncapi "github.com/openbindings/openbindings-go/formats/asyncapi"
)

exec := openbindings.NewOperationInvoker(asyncapi.NewInvoker())
```

The invoker declares `asyncapi@^3.0.0` — it handles any AsyncAPI 3.x spec.

### Invoke a binding

The `OperationInvoker` routes operations to this invoker based on the OBI's source format. Direct use:

```go
invoker := asyncapi.NewInvoker()
defer invoker.Close()

call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        Format:   "asyncapi@3.0",
        Location: "https://api.example.com/asyncapi.json",
    },
    Ref:     "#/operations/sendMessage",
    Context: map[string]any{"bearerToken": "tok_123"},
})

// Inputs flow through the handle; a send operation takes one message.
if err := call.Write(ctx, map[string]any{"text": "hello"}); err != nil {
    log.Fatal(err)
}

// Unary send: assert exactly one output.
out, err := openbindings.Single(ctx, call.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

For a streaming receive, read the output sequence to EOF instead:

```go
call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source:  source,
    Ref:     "#/operations/orderUpdates",
    Context: map[string]any{"bearerToken": "tok_123"},
})
outs := call.Outputs()
for {
    ev, err := outs.Read(ctx)
    if err == io.EOF {
        break // clean close
    }
    if err != nil {
        log.Fatal(err) // terminal *openbindings.InvocationError
    }
    fmt.Println(ev)
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

### Context negotiation

The doc's security declarations (operation-level, falling back to server-level) are mapped to requirement families (`auth.bearer`, `auth.basic`, `auth.apiKey`, `auth.oauth2`). When the declared requirements aren't satisfied by the provided context, the invocation terminates with `CONTEXT_REQUIRED` **before any network I/O**; the challenge `key` is the normalized server origin. The invoker also implements `PrepareBinding` (side-effect-free preflight) using inline source content or the warm doc cache — it never fetches.

### Credential application

Credentials are applied based on the AsyncAPI spec's `securitySchemes`:

- `http` + `bearer` / `httpBearer`: `Authorization: Bearer <token>` from `bearerToken`
- `http` + `basic` / `userPassword`: `Authorization: Basic <encoded>` from `basic`
- `apiKey` / `httpApiKey`: Placed in header, query, or cookie as declared, from `apiKey`
- `oauth2`: `Authorization: Bearer <token>` from `bearerToken` or `accessToken`

For WebSocket receive subscriptions, bearer tokens are sent in the first message body (browsers cannot set headers on WebSocket upgrades). Query-param apiKeys are appended to the WebSocket URL.

Falls back to bearer, then basic, then apiKey when no security schemes are defined.

### Interface creation

- Operations iterated alphabetically for deterministic output
- Input schemas from send operation payloads
- Output schemas from receive operation payloads and reply payloads
- Refs generated as `#/operations/<id>`

### Protocol dispatch

The invoker determines the transport from the AsyncAPI spec's server protocol and the operation's action:

| Protocol | Receive (subscribe) | Send (publish) |
|----------|-------------------|----------------|
| HTTP/HTTPS | SSE streaming | POST (unary) |
| WS/WSS | WebSocket subscribe (bidi-capable) | WebSocket publish (client-streaming) |

## How it works

### Invocation flow

1. Loads and caches the AsyncAPI document (JSON or YAML, local or remote)
2. Parses the ref to extract the operation ID (`#/operations/sendMessage` -> `sendMessage`)
3. Resolves the server URL and protocol from the spec
4. Challenges `CONTEXT_REQUIRED` when declared security isn't satisfied (before any I/O)
5. Dispatches based on action and protocol:
   - **receive + http/https**: SSE subscribe — each server event is one output
   - **receive + ws/wss**: WebSocket subscribe — socket frames become outputs; caller inputs are forwarded as control frames (closing input does not end the subscription)
   - **send + http/https**: unary HTTP POST — the first input is the message body; a 202/204 acknowledgment yields zero outputs
   - **send + ws/wss**: client-streaming publish — every input is one socket frame; closing input completes the call

### WebSocket connection pooling

Operations on the same channel (same server + address) share one pooled WebSocket connection — load-bearing for the AsyncAPI two-operation pattern where a `receive` subscription and `send` publishes need to land on the same socket so server-side per-connection state is shared. Connections are reference-counted and evicted after a 30s idle timeout; `Invoker.Close()` closes the pool.

### Interface creation

Converts an AsyncAPI 3.x document into an OBI by:
- Iterating operations sorted alphabetically for deterministic output
- Extracting input schemas from send operation payloads
- Extracting output schemas from receive operation payloads and reply payloads
- Generating `#/operations/<id>` refs for each binding
- Deriving operation keys from operation IDs

## License

Apache-2.0
