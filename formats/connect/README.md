# formats/connect

Connect (Buf) binding invoker and interface synthesizer for the [OpenBindings](https://openbindings.com) Go SDK.

This package implements the published, exact binding-specification identifier
`openbindings.connect@1`. It uses the Connect JSON protocol and incorporates
the protobuf schema carriage, ref grammar, and canonical ProtoJSON
correspondence defined by `openbindings.grpc@1` where the Connect binding
specification says to do so.

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
        BindingSpec: connectbinding.BindingSpec,
        Location:    "https://api.example.com",
    },
    Ref:     "mypackage.MyService/GetItem",
    Context: map[string]any{"headers": map[string]string{"authorization": "Bearer tok_123"}},
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

### Synthesize an interface from embedded protobuf content

```go
synth := connectbinding.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        BindingSpec: connectbinding.BindingSpec,
        Location:    "https://api.example.com",
        Content:     openbindings.TextContent(protoSource),
    }},
})
```

## Conventions

These are non-normative implementation notes. The binding specification is
normative for artifact, target, and operation-boundary behavior.

### Binding specification identifier

`openbindings.connect@1` (exact and opaque).

### Ref format

Same as gRPC: `{package.Service}/{Method}`

- `mypackage.UserService/GetUser`
- `blend.CoffeeShop/GetMenu`

Both Connect and gRPC use protobuf service definitions, so the ref convention is identical.

### Source expectations

- **`location`**: The Connect server base URL (e.g., `https://api.example.com`). The invoker constructs the full URL as `{location}/{service}/{method}`.
- **`content`**: Inline protobuf definition (string). Used for proto-aware input marshaling. When provided alongside a location, the location is used for invocation and the content for schema resolution.

### Credential application

The schema and protocol declare no application authentication convention, so
the package invents none. Explicitly named context `headers` and `cookies`
ride under Connect/HTTP metadata rules. A generic bearer, basic, or API-key
credential without a named carriage raises `CONTEXT_REQUIRED` before
dispatch; it is never silently mapped to `Authorization`.

### Connect protocol details

The invoker sends unary requests as plain `application/json` and streaming
requests as enveloped `application/connect+json`. The Connect protocol permits
`Connect-Protocol-Version: 1` to be sent or omitted; either artifact-permitted
choice is valid.

Responses are parsed as JSON. Connect error responses complete unsuccessfully with the structural `ERR_EXECUTION_FAILED` code. Their native Connect error object and any HTTP response capture are optional diagnostics; the ordinary operation boundary does not derive portable auth, retry, or side-effect policy from Connect codes.

Mapping does not replace native evidence. `FailureEvidenceFrom` recovers the
complete parsed Connect error and either the exact non-200 HTTP response body
or exact END_STREAM payload from the terminal error, including after an
invoker-frame JSON round trip. A diagnostics-bound HTTP body is explicitly
marked truncated rather than presented as complete. Data envelopes emitted
before a later END_STREAM error remain outputs.

Leading and trailing Connect/HTTP metadata is available only through the handle's explicit `Diagnostics()` view. It is not an operation value and correct ordinary invocation behavior must not depend on it.

### Streaming behavior

With embedded schema content, the invoker preserves all four protobuf-declared
cardinalities: unary, server-streaming, client-streaming, and bidirectional.
Bidirectional output may arrive while input remains open. A selected HTTP
transport that cannot provide full duplex refuses client-streaming and
bidirectional methods before dispatch as an implementation limitation; it does
not reinterpret them. Without content, descriptorless mode is unary by
definition and requires exactly one caller value.

Server-streaming uses the Connect envelope-framed wire format with `Content-Type: application/connect+json` per the [Connect protocol specification](https://connectrpc.com/docs/protocol#streaming-rpcs). Server-streaming dispatch requires inline proto `content` on the source so the invoker can detect that the method is streaming; without proto content the invoker falls back to unary invocation.

The interface synthesizer and `SourceInspector` include all four declared
method kinds. Whether a particular invocation host can dispatch a
request-streaming method is a runtime transport capability; it does not shrink
the artifact-derived interface.

### Relationship to gRPC

Connect and gRPC are separate wire protocols that share protobuf service
definitions. The same schema can govern sources with
`bindingSpec: "openbindings.grpc@1"` and
`bindingSpec: "openbindings.connect@1"`; a service speaking both has distinct
sources and bindings for the two access paths.

### Consumer hooks

Like gRPC, protobuf framing plus the Connect envelope status fully determine
decode and success. This binding implementation **does not consult the
consumer hooks seam** (`InvokeHooks`); a `DecodeOutput`, `Classify`, or `Route`
hook has no effect.

## Resource bounds

Delivery units — the unary response body and each streaming envelope
payload — are consumer-bounded: set `MaxDeliveryUnitBytes` on the
`OperationInvoker` (or per invocation on `BindingInvocationArgs`); zero
selects `openbindings.DefaultMaxDeliveryUnitBytes` (10 MiB). The streaming
dispatch path's HTTP error-body capture is deliberately fixed — a
diagnostics capture, not a delivery unit. Size caps are consumer and
implementation policy under openbindings.connect@1 §2, never spec rules.

## License

Apache-2.0
