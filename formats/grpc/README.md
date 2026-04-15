# grpc-go

gRPC binding executor and interface creator for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to execute operations against gRPC servers and synthesize OBI documents from them. It discovers services via gRPC server reflection, constructs dynamic protobuf requests, applies credentials as gRPC metadata, and returns results as a stream of events.

See the [spec](https://github.com/openbindings/spec) and [pattern documentation](https://github.com/openbindings/spec/tree/main/patterns) for how executors and creators fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/grpc
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationExecutor

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    grpcbinding "github.com/openbindings/openbindings-go/formats/grpc"
)

exec := openbindings.NewOperationExecutor(grpcbinding.NewExecutor())
```

The executor declares `grpc` -- it handles gRPC servers via reflection and `.proto` files.

### Execute a binding

Typically you don't call the executor directly -- the `OperationExecutor` routes operations to it based on the OBI's source format. But direct use is straightforward:

```go
executor := grpcbinding.NewExecutor()
defer executor.Close()

ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
    Source: openbindings.ExecuteSource{
        Format:   "grpc",
        Location: "api.example.com:443",
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

### Create an interface from a gRPC server

```go
creator := grpcbinding.NewCreator()
iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
    Sources: []openbindings.CreateSource{{
        Format:   "grpc",
        Location: "api.example.com:443",
    }},
})
// iface is a fully-formed OBInterface with operations, bindings, and sources
```

## Conventions

These are non-normative conventions specific to the `grpc` binding format.

### Format token

`grpc` (versionless). Handles gRPC servers via reflection and `.proto` files.

### Ref format

`{package.Service}/{Method}` - the fully qualified service name followed by the method name:

- `mypackage.UserService/GetUser`
- `blend.CoffeeShop/GetMenu`

The service name is the protobuf fully qualified name. The method name is the unqualified RPC name.

### Source expectations

- **`location`**: The gRPC server address (`host:port`) or a path to a `.proto` file. TLS is auto-detected for port 443 or `https://` prefixes. When the location ends in `.proto`, the file is parsed directly instead of using server reflection.
- **`content`**: Inline protobuf definition (string). When provided, parsed directly without connecting to a server for reflection. The server address in `location` is still used for execution.

### Credential application

Credentials are applied as gRPC metadata (equivalent to HTTP/2 headers):

- `bearerToken`: `authorization: Bearer <token>`
- `apiKey`: `authorization: ApiKey <key>`
- `basic`: `authorization: Basic <encoded>`

Execution options headers are forwarded as additional metadata.

### Connect (Buf) compatibility

This executor can discover and execute against [Connect](https://connectrpc.com) servers that serve the gRPC protocol (the default). Connect handlers expose gRPC alongside the Connect protocol, and Connect's `grpcreflect` package is wire-compatible with Google's gRPC reflection API.

The resulting OBI will have `format: "grpc"`, reflecting the gRPC access path. It does not capture the Connect protocol as a separate binding. If you need Connect-native access (HTTP/1.1, JSON payloads), that would require a dedicated `connect` format.

### Interface creation

- Services discovered via gRPC server reflection or `.proto` file parsing
- Infrastructure services filtered out (`grpc.reflection.*`, `grpc.health.*`)
- Client-streaming RPCs skipped (unary and server-streaming supported)
- Protobuf message types converted to JSON Schema (int64 mapped to string for JS safety)
- Methods sorted alphabetically for deterministic output
- No security metadata in protobuf; auth retry handles 401 at runtime

## How it works

### Execution flow

1. Resolves or reuses a cached gRPC client connection (TLS auto-detected for port 443 or `https://` prefixes)
2. Parses the ref as `package.Service/Method`
3. Resolves service and method descriptors via server reflection, `.proto` file, or inline content
4. Builds a dynamic protobuf request from JSON input
5. Applies credentials from the context as gRPC metadata (bearer, basic, apiKey)
6. Invokes the RPC and returns the result as a stream event

For server-streaming RPCs, the executor returns a channel that yields events as they arrive. For unary RPCs, it returns a single-event channel.

### Credential application

Credentials are applied as gRPC metadata in priority order:

- **bearer**: Sets `authorization: Bearer <token>` from `bearerToken` context field
- **basic**: Sets `authorization: Basic <encoded>` from `basic.username`/`basic.password` context fields
- **apiKey**: Sets `authorization: ApiKey <key>` from `apiKey` context field

Execution options headers are also forwarded as gRPC metadata.

### Interface creation

Converts a live gRPC server into an OBI by:

- Discovering services via gRPC reflection or `.proto` file parsing
- Filtering out infrastructure services (`grpc.reflection.*`, `grpc.health.*`)
- Skipping client-streaming RPCs
- Converting protobuf message types to JSON Schema (input and output)
- Generating `package.Service/Method` refs for each binding
- Sorting services and methods alphabetically for deterministic output

## License

Apache-2.0
