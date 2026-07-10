# formats/grpc

gRPC binding invoker and interface synthesizer for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to invoke operations against gRPC servers and synthesize OBI documents from them. It discovers services via gRPC server reflection, constructs dynamic protobuf requests, applies credentials as gRPC metadata, and yields results through the SDK's cardinality-agnostic `Invocation` handle.

See the [spec](https://github.com/openbindings/spec) and the [invocation pattern](https://openbindings.com/spec/invocation-pattern) for how binding invokers and interface synthesizers fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/grpc
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    grpcbinding "github.com/openbindings/openbindings-go/formats/grpc"
)

opInv := openbindings.NewOperationInvoker(grpcbinding.NewInvoker())
```

The invoker declares `grpc` -- it handles gRPC servers via reflection and `.proto` files.

### Invoke a binding

Typically you don't call the invoker directly -- the `OperationInvoker` routes operations to it based on the OBI's source format. But direct use is straightforward:

```go
invoker := grpcbinding.NewInvoker()
defer invoker.Close()

inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        Format:   "grpc",
        Location: "api.example.com:443",
    },
    Ref:     "mypackage.MyService/GetItem",
    Context: map[string]any{"bearerToken": "tok_123"},
})

// Input flows through the handle, not the args.
if err := inv.Write(ctx, map[string]any{"id": "123"}); err != nil {
    log.Fatal(err)
}

// Unary: assert exactly one output.
out, err := openbindings.Single(ctx, inv.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

For server-streaming methods, read the output stream to `io.EOF` instead:

```go
inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{Format: "grpc", Location: "api.example.com:443"},
    Ref:    "mypackage.MyService/WatchItems",
})
_ = inv.Write(ctx, map[string]any{"topic": "orders"})

stream := inv.Outputs()
for {
    out, err := stream.Read(ctx)
    if err == io.EOF {
        break // clean end of stream
    }
    if err != nil {
        log.Fatal(err) // terminal *openbindings.InvocationError
    }
    fmt.Println(out)
}
```

gRPC leading/trailing metadata maps onto the handle: `inv.Header(ctx)` returns the server's header metadata, and `inv.Trailer()` (valid after the invocation terminates) returns trailing metadata.

### Synthesize an interface from a gRPC server

```go
synth := grpcbinding.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
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

- **`location`**: The gRPC server address (`host:port`) or a path to a `.proto` file. TLS is auto-detected for port 443, or forced off any port with an `https://` or `grpcs://` prefix (the prefix is the invoker's affordance, stripped before dialing). When the location ends in `.proto`, the file is parsed directly instead of using server reflection.
- **`content`**: Inline protobuf definition (string, self-contained — no imports). When provided, method descriptors are built from it directly, never from reflection.

### Transport configuration

The invoker and synthesizer take functional options speaking grpc-go's own vocabulary (the invoker is openly grpc-go; inventing an abstraction over it would serve nobody):

```go
grpc.NewInvoker()                                          // zero-config: TLS auto-detected (:443, https://, grpcs://)
grpc.NewInvoker(grpcfmt.WithTransportCredentials(creds))   // mTLS / custom CA / forced plaintext — replaces auto-detection
grpc.NewInvoker(grpcfmt.WithDialOptions(opts...))          // interceptors, keepalive, user-agent, ... (appended after defaults)
grpc.NewSynthesizer(grpcfmt.WithSynthesizerTransportCredentials(creds)) // reflection discovery dials live, so it needs the same setup
```

A caller who states the transport identity owns it: `WithTransportCredentials` disables the automatic TLS detection entirely. These are process-level defaults applied to every connection the invoker dials; a per-target credential lane (an `auth.mtls` context family) is designed follow-up work.

### Dial address resolution (invocation)

The address the invoker dials resolves in order: the source's `location`, then the `baseURL` field of the invocation context's `metadata` (the same override key the openapi invoker honors for targeting the same OBI at a different host). A source that carries its schema as embedded `content` but supplies no address through either lane refuses with a message naming both remedies.

gRPC is a **service-addressed format** (core spec §6.4): its `location` names the live service, not a document. A source may therefore carry both — embedded `content` pins the contract (descriptors build from it, never from reflection) while `location` remains the dial address:

```json
"blend": {
  "format": "grpc",
  "content": "syntax = \"proto3\"; ...",
  "location": "api.example.com:443"
}
```

This is the publishing form for servers with reflection disabled: the document is self-contained for schema purposes and complete for invocation. (`ob source add x.obi.json grpc:./svc.proto --resolve content --uri host:port` authors it.)

### Credential application

Credentials are applied as gRPC metadata (equivalent to HTTP/2 headers):

- `bearerToken`: `authorization: Bearer <token>`
- `apiKey`: `authorization: ApiKey <key>`
- `basic`: `authorization: Basic <encoded>`

The context's `headers` map is forwarded as additional metadata.

### Connect (Buf) compatibility

This invoker can discover and execute against [Connect](https://connectrpc.com) servers that serve the gRPC protocol (the default). Connect handlers expose gRPC alongside the Connect protocol, and Connect's `grpcreflect` package is wire-compatible with Google's gRPC reflection API.

The resulting OBI will have `format: "grpc"`, reflecting the gRPC access path. It does not capture the Connect protocol as a separate binding. If you need Connect-native access (HTTP/1.1, JSON payloads), that would require a dedicated `connect` format.

### Interface synthesis

- Services discovered via gRPC server reflection or `.proto` file parsing
- Infrastructure services filtered out (`grpc.reflection.*`, `grpc.health.*`)
- Client-streaming RPCs skipped (unary and server-streaming supported)
- Protobuf message types converted to JSON Schema (int64 mapped to string for JS safety)
- Methods sorted alphabetically for deterministic output
- No security metadata in protobuf; an unauthenticated call surfaces as a terminal `ERR_AUTH_REQUIRED`, and credential resolution happens above the binding (operation-layer context negotiation)

### Consumer hooks

protobuf framing is unambiguous: the response message type and the gRPC status code fully determine decode and success. This format **does not consult the consumer hooks seam** (`InvokeHooks`) — a `DecodeOutput`, `Classify`, or `Route` hook has no effect here, and `ob plan` reports it as `not-consulted`. Hooks matter for formats with open wire questions (openapi, asyncapi).

## How it works

### Invocation flow

`InvokeBinding` returns the `Invocation` handle synchronously; all work runs on the binding's goroutine:

1. Parses the ref as `package.Service/Method` (bad ref → `ERR_INVALID_REF`, before any I/O)
2. Resolves service and method descriptors via inline content or server reflection (load failure → `ERR_SOURCE_LOAD_FAILED`; unresolved symbol → `ERR_REF_NOT_FOUND`)
3. Resolves or reuses a cached gRPC client connection (TLS auto-detected for port 443 or `https://` prefixes)
4. Reads the single request message from the handle (`Write` one input; methods with empty request messages dispatch without one) and builds a dynamic protobuf request from it
5. Applies credentials from the context as gRPC metadata (bearer, basic, apiKey)
6. Invokes the RPC: unary methods emit one output then close; server-streaming methods emit per received message, with backpressure flow-controlling the stream

gRPC status errors terminate the invocation with a mapped code (`Unauthenticated` → `ERR_AUTH_REQUIRED`, `PermissionDenied` → `ERR_PERMISSION_DENIED`, `Unavailable` → `ERR_CONNECT_FAILED`, `DeadlineExceeded` → `ERR_TIMEOUT`); the gRPC code and any status details ride in the error's `Details`. Leading/trailing gRPC metadata maps onto `Header(ctx)`/`Trailer()`. Cancelling the handle (or the invocation context) tears down the underlying stream.

Client-streaming and bidi methods are not supported and terminate pre-dispatch with `ERR_EXECUTION_FAILED`.

### Credential application

Credentials are applied as gRPC metadata in priority order:

- **bearer**: Sets `authorization: Bearer <token>` from `bearerToken` context field
- **apiKey**: Sets `authorization: ApiKey <key>` from `apiKey` context field
- **basic**: Sets `authorization: Basic <encoded>` from `basic.username`/`basic.password` context fields

The context's `headers` map is also forwarded as gRPC metadata.

### Interface synthesis

Converts a live gRPC server into an OBI by:

- Discovering services via gRPC reflection or `.proto` file parsing
- Filtering out infrastructure services (`grpc.reflection.*`, `grpc.health.*`)
- Skipping client-streaming RPCs
- Converting protobuf message types to JSON Schema (input and output)
- Generating `package.Service/Method` refs for each binding
- Sorting services and methods alphabetically for deterministic output

## License

Apache-2.0
