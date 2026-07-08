# formats/connect

Connect (Buf) binding invoker and interface synthesizer for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to invoke operations against Connect services and synthesize OBI documents from protobuf definitions. It uses the Connect wire protocol (HTTP POST with JSON) and shares the same protobuf service definitions and ref convention as the gRPC invoker.

See the [spec](https://github.com/openbindings/spec) and the [invocation pattern](https://openbindings.com/spec/invocation-pattern) for how binding invokers and interface synthesizers fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/connect
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    connectbinding "github.com/openbindings/openbindings-go/formats/connect"
)

opInv := openbindings.NewOperationInvoker(connectbinding.NewInvoker())
```

### Invoke a binding

```go
invoker := connectbinding.NewInvoker()

inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        Format:   "connect",
        Location: "https://api.example.com",
    },
    Ref:     "mypackage.MyService/GetItem",
    Context: map[string]any{"bearerToken": "tok_123"},
})

// The request message flows through the handle, not the args.
if err := inv.Write(ctx, map[string]any{"id": "123"}); err != nil {
    log.Fatal(err)
}
_ = inv.Close()

// Unary: assert exactly one output.
out, err := openbindings.Single[any](ctx, inv.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

For server-streaming methods, read the output stream to its end instead:

```go
outputs := inv.Outputs()
for {
    msg, err := outputs.Read(ctx)
    if err == io.EOF {
        break // clean end of stream
    }
    if err != nil {
        log.Fatal(err) // terminal *openbindings.InvocationError
    }
    fmt.Println(msg)
}
```

### Synthesize an interface from a .proto file

```go
synth := connectbinding.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
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

- **`location`**: The Connect server base URL (e.g., `https://api.example.com`). The invoker constructs the full URL as `{location}/{service}/{method}`.
- **`content`**: Inline protobuf definition (string). Used for proto-aware input marshaling. When provided alongside a location, the location is used for invocation and the content for schema resolution.

### Credential application

Credentials are applied as HTTP headers:

- `bearerToken`: `Authorization: Bearer <token>`
- `apiKey`: `Authorization: ApiKey <key>`
- `basic`: `Authorization: Basic <encoded>`

Context `headers` and `cookies` are also forwarded.

### Connect protocol details

The invoker sends requests as HTTP POST with:
- `Content-Type: application/json`
- `Connect-Protocol-Version: 1`

Responses are parsed as JSON. Connect error responses (with `code` and `message` fields) are mapped to standard error codes: `unauthenticated` → `ERR_AUTH_REQUIRED`, `permission_denied` → `ERR_PERMISSION_DENIED`, `unavailable` → `ERR_CONNECT_FAILED`, `deadline_exceeded` → `ERR_TIMEOUT`, anything else → `ERR_EXECUTION_FAILED`.

Leading metadata (HTTP response headers) is available via the handle's `Header`; trailing metadata (Connect unary `Trailer-`-prefixed headers, or the streaming end-stream envelope's `metadata` field) via `Trailer`.

### Streaming behavior

The Connect invoker supports two cardinalities, both taking exactly one request message through the handle's `Write` channel:

- **Unary RPCs** — single request, single response. The invocation yields one output.
- **Server-streaming RPCs** — single request, stream of responses. Each server-streamed message is emitted as a separate output; the output stream closes when the server's end-stream envelope is received or the caller cancels.

Server-streaming uses the Connect envelope-framed wire format with `Content-Type: application/connect+json` per the [Connect protocol specification](https://connectrpc.com/docs/protocol#streaming-rpcs). Server-streaming dispatch requires inline proto `content` on the source so the invoker can detect that the method is streaming; without proto content the invoker falls back to unary invocation.

The interface synthesizer skips **client-streaming** methods during synthesis (out of the module's cardinality scope: one request message in, one or more outputs out). Server-streaming methods are included.

Compression is not currently supported. Bidirectional streaming is out of scope.

### Relationship to gRPC

Connect and gRPC are separate wire protocols that share protobuf service definitions. The same `.proto` file can produce both `format: "grpc"` and `format: "connect"` bindings. A service that speaks both protocols would have two sources and two sets of bindings in its OBI.

## License

Apache-2.0
