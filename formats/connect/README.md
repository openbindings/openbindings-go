# connect-go

Connect (Buf) binding executor and interface creator for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to execute operations against Connect services and synthesize OBI documents from protobuf definitions. It uses the Connect wire protocol (HTTP POST with JSON) and shares the same protobuf service definitions and ref convention as the gRPC executor.

See the [spec](https://github.com/openbindings/spec) and [pattern documentation](https://github.com/openbindings/spec/tree/main/guides) for how binding executors and interface creators fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/connect
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationExecutor

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    connectbinding "github.com/openbindings/openbindings-go/formats/connect"
)

exec := openbindings.NewOperationExecutor(connectbinding.NewExecutor())
```

### Execute a binding

```go
executor := connectbinding.NewExecutor()

ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
    Source: openbindings.ExecuteSource{
        Format:   "connect",
        Location: "https://api.example.com",
    },
    Ref:     "mypackage.MyService/GetItem",
    Input:   map[string]any{"id": "123"},
    Context: map[string]any{"bearerToken": "tok_123"},
})
for ev := range ch {
    if ev.Error != nil {
        log.Fatal(ev.Error.Message)
    }
    fmt.Println(ev.Data)
}
```

### Create an interface from a .proto file

```go
creator := connectbinding.NewCreator()
iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
    Sources: []openbindings.CreateSource{{
        Format:   "connect",
        Location: "./service.proto",
    }},
})
```

## Conventions

These are non-normative conventions specific to the `connect` binding format.

### Format token

`connect` (versionless). Handles Connect (Buf) services via HTTP.

### Ref format

Same as gRPC: `{package.Service}/{Method}`

- `mypackage.UserService/GetUser`
- `blend.CoffeeShop/GetMenu`

Both Connect and gRPC use protobuf service definitions, so the ref convention is identical.

### Source expectations

- **`location`**: The Connect server base URL (e.g., `https://api.example.com`). The executor constructs the full URL as `{location}/{service}/{method}`.
- **`content`**: Inline protobuf definition (string). Used for proto-aware input marshaling. When provided alongside a location, the location is used for execution and the content for schema resolution.

### Credential application

Credentials are applied as HTTP headers:

- `bearerToken`: `Authorization: Bearer <token>`
- `apiKey`: `Authorization: ApiKey <key>`
- `basic`: `Authorization: Basic <encoded>`

Execution options headers and cookies are also forwarded.

### Connect protocol details

The executor sends requests as HTTP POST with:
- `Content-Type: application/json`
- `Connect-Protocol-Version: 1`

Responses are parsed as JSON. Connect error responses (with `code` and `message` fields) are mapped to standard error codes.

### Streaming behavior

The Connect executor supports two streaming patterns:

- **Unary RPCs** — single request, single response. Each call produces one stream event.
- **Server-streaming RPCs** — single request, stream of responses. Each server-streamed message is emitted as a separate stream event; the channel closes when the server's end-stream envelope is received or the caller cancels the context.

Server-streaming uses the Connect envelope-framed wire format with `Content-Type: application/connect+json` per the [Connect protocol specification](https://connectrpc.com/docs/protocol#streaming-rpcs). Server-streaming dispatch requires inline proto `content` on the source so the executor can detect that the method is streaming; without proto content the executor falls back to unary execution.

The interface creator skips **client-streaming** methods during interface creation (these are structurally inexpressible in the v0.1 OBI execution model, which accepts a single input value per operation invocation). Server-streaming methods are included.

Compression is not currently supported. Bidirectional streaming is out of scope.

### Relationship to gRPC

Connect and gRPC are separate wire protocols that share protobuf service definitions. The same `.proto` file can produce both `format: "grpc"` and `format: "connect"` bindings. A service that speaks both protocols would have two sources and two sets of bindings in its OBI.

## License

Apache-2.0
