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

The invoker declares `openbindings.grpc@1`. Embedded protobuf content pins the
schema; without content, the schema is discovered from the addressed server
through gRPC reflection.

### Invoke a binding

Typically you don't call the invoker directly -- the `OperationInvoker` routes operations to it based on the OBI source's binding specification. But direct use is straightforward:

```go
invoker := grpcbinding.NewInvoker()
defer invoker.Close()

inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        BindingSpec: grpcbinding.BindingSpec, // "openbindings.grpc@1"
        Location:    "grpcs://api.example.com:443",
    },
    Ref:     "mypackage.MyService/GetItem",
    Context: map[string]any{"headers": map[string]string{"authorization": "Bearer tok_123"}},
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
    Source: openbindings.InvocationSource{BindingSpec: grpcbinding.BindingSpec, Location: "grpcs://api.example.com:443"},
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

gRPC leading/trailing metadata is available only through the handle's explicit diagnostic view: `inv.Diagnostics().Header(ctx)` and `inv.Diagnostics().Trailer()`. Every value and its order are retained there; binary `-bin` metadata values use base64 strings. None of it is part of the ordinary operation value stream.

If iteration terminates with a gRPC status, `FailureEvidenceFrom` recovers the
native numeric code, message, and exact `google.protobuf.Any` payload bytes
from the terminal error, including after an invoker-frame JSON round trip:

```go
if evidence, ok := grpcbinding.FailureEvidenceFrom(err); ok {
    log.Printf("gRPC status %s: %s", evidence.Code, evidence.Message)
}
```

Application result messages remain ordinary outputs even when their fields
look error-shaped. A non-OK final gRPC status is a failure completion, and a
local SDK or ProtoJSON validation error carries no invented gRPC evidence.

### Synthesize an interface from a gRPC server

```go
synth := grpcbinding.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        BindingSpec: grpcbinding.BindingSpec, // "openbindings.grpc@1"
        Location:    "grpcs://api.example.com:443",
    }},
})
// iface is a fully-formed OBInterface with operations, bindings, and sources
```

## Conventions

This package implements the published [`openbindings.grpc@1`](https://github.com/openbindings/spec/blob/main/binding-specs/grpc/openbindings.grpc.md) binding specification; that document is normative for artifact forms, dial addresses, input construction, interaction shapes, and channel assembly. The conventions below are this package's own.

### Binding specification identifier

`openbindings.grpc@1` (exact, opaque). Handles gRPC servers via embedded
protobuf schemas or live reflection.

### Ref format

`{package.Service}/{Method}` - the fully qualified service name followed by the method name:

- `mypackage.UserService/GetUser`
- `blend.CoffeeShop/GetMenu`

The service name is the protobuf fully qualified name. The method name is the unqualified RPC name.

### Source expectations

- **`location`**: The gRPC server dial address in one of the specification's three port-explicit forms (`openbindings.grpc@1` §4, GRPC-D-02): bare `host:port`, `grpc://host:port` (plaintext), or `grpcs://host:port` (TLS). A bare target makes no transport claim and requires consumer election before dispatch; port numbers never imply TLS. No port is defaulted, and no location is interpreted as a schema-file path.
- **`content`**: The embedded schema — single-file `.proto` source text (string; imports limited to `google/protobuf/*`) or a `google.protobuf.FileDescriptorSet` in canonical JSON (object). When provided, method descriptors are built from it directly, never from reflection.

### Transport configuration

The invoker and synthesizer take functional options speaking grpc-go's own vocabulary (the invoker is openly grpc-go; inventing an abstraction over it would serve nobody):

```go
grpc.NewInvoker()                                          // schemes elect transport; bare host:port requires explicit election
grpc.NewInvoker(grpcfmt.WithTransportCredentials(creds))   // mTLS / custom CA / forced plaintext — replaces auto-detection
grpc.NewInvoker(grpcfmt.WithDialOptions(opts...))          // interceptors, keepalive, user-agent, ... (appended after defaults)
grpc.NewSynthesizer(grpcfmt.WithSynthesizerTransportCredentials(creds)) // reflection discovery dials live, so it needs the same setup
```

A caller who states the transport identity owns it:
`WithTransportCredentials` replaces the address form's determination entirely.
These are process-level defaults applied to every connection the invoker
dials.

### Dial address resolution (invocation)

The address the invoker dials is the **target configuration point** (`openbindings.grpc@1` §9.3): the default is the source's `location`; a `configuration.target` entry in the invocation context replaces it entirely, in the same §4 address forms. A source that carries its schema as embedded `content` but supplies no address through either lane refuses with a message naming both remedies.

gRPC is a **service-addressed family** (`openbindings.grpc@1` §4): its `location` names the live service, not a document. A source may therefore carry both — embedded `content` pins the contract (descriptors build from it, never from reflection) while `location` remains the dial address:

```json
"blend": {
  "bindingSpec": "openbindings.grpc@1",
  "content": "syntax = \"proto3\"; ...",
  "location": "api.example.com:443"
}
```

This is the publishing form for servers with reflection disabled: the document is self-contained for schema purposes and complete for invocation. (`ob source add x.obi.json grpc:./svc.proto --resolve content --uri host:port` authors it.)

### Credential application

Protobuf and gRPC declare no per-method application-authentication convention,
so this binding invents none. Explicitly named context `headers` ride as gRPC
metadata under the protocol grammar. A generic bearer, basic, or API-key
credential without a named metadata carriage raises `CONTEXT_REQUIRED` before
reflection or method dispatch; it is never silently mapped to
`authorization`.

### Connect (Buf) compatibility

This invoker can discover and execute against [Connect](https://connectrpc.com) servers that serve the gRPC protocol (the default). Connect handlers expose gRPC alongside the Connect protocol, and Connect's `grpcreflect` package is wire-compatible with Google's gRPC reflection API.

The resulting OBI will have `bindingSpec: "openbindings.grpc@1"`, reflecting the gRPC access path. It does not capture the Connect protocol as a separate binding. For Connect-native access (HTTP/1.1, JSON payloads), use the dedicated [`formats/connect`](../connect) package, which implements `openbindings.connect@1`.

### Interface synthesis

Deterministic generation of OBI documents is a synthesis concern outside the binding specification (`openbindings.grpc@1` §10); these are this package's conventions:

- **Discovery**: services come from embedded protobuf content or gRPC server reflection. Infrastructure services (`grpc.reflection.*`, `grpc.health.*`) are excluded. All four declared method kinds are synthesized.
- **Determinism**: services iterate by fully-qualified name, methods by name; the same schema yields an identical OBI.
- **Operation keys** are the method's unqualified name, sanitized to the OBI key grammar; a cross-service collision is disambiguated with the service's name. Binding refs are the full `package.Service/Method` form.
- **Schema translation** mirrors protobuf's canonical JSON mapping. Field names use their `json_name` (camelCase) spellings; enums emit `{"type": "string", "enum": [...declared value names]}`; maps emit `additionalProperties`; repeated fields emit arrays; recursive message cycles degrade to a bare `{"type": "object"}`.
- **64-bit integers** (`int64`/`uint64`/`sint64`/`fixed64`/`sfixed64` and the 64-bit wrapper types) emit `{"type": "integer", "format": "int64"}`. Schemas describe semantic types, not wire carriage; downstream codegen reads `format: int64` to pick precision-preserving language types.
- **Well-known types** emit their canonical JSON-mapping schemas instead of their internal fields (descending into `Timestamp`'s `seconds`/`nanos` would produce a contract the protojson layer cannot accept):

  | Message | Schema |
  | --- | --- |
  | `google.protobuf.Timestamp` | `string` with `format: date-time` |
  | `google.protobuf.Duration` | `string` (seconds, `s`-suffixed) |
  | `google.protobuf.FieldMask` | `string` (comma-separated field paths) |
  | `google.protobuf.Struct` | `object` |
  | `google.protobuf.Value` | `{}` (any value) |
  | `google.protobuf.ListValue` | `array` |
  | `google.protobuf.Empty` | `object` |
  | `google.protobuf.BoolValue` | `boolean` |
  | `google.protobuf.StringValue`, `BytesValue` | `string` |
  | `google.protobuf.Int32Value`, `UInt32Value` | `integer` |
  | `google.protobuf.Int64Value`, `UInt64Value` | `integer` with `format: int64` |
  | `google.protobuf.FloatValue`, `DoubleValue` | `number` |
  | `google.protobuf.Any` | `object` with required `@type: string` plus `value` |

- **Oneof groups**: a message with exactly one user-declared oneof group emits a top-level `oneOf`, one single-property required variant per member, preserving exactly-one-of semantics. Proto3 `optional` fields (synthetic single-field oneofs) are ordinary optional properties, never variants. A message with multiple oneof groups flattens every member to an independent optional property — the current schema profile cannot express multi-group exclusivity — and surfaces the `grpc.multi_group_oneof` warning so callers know the emitted OBI does not enforce it.
- **No security metadata** exists in protobuf, so none is written to the OBI; a post-dispatch unauthenticated status completes unsuccessfully and remains identifiable only through optional diagnostics, while pre-dispatch credential resolution happens above the binding.

### Consumer hooks

protobuf framing is unambiguous: the response message type and the gRPC status
code fully determine decode and success. This binding implementation **does
not consult the consumer hooks seam** (`InvokeHooks`) — a `DecodeOutput`,
`Classify`, or `Route` hook has no effect here. Hooks matter for binding
specifications with exposed wire choices (OpenAPI and AsyncAPI).

## How it works

### Invocation flow

`InvokeBinding` returns the `Invocation` handle synchronously; all work runs on the binding's goroutine:

1. Parses the ref as `package.Service/Method` (bad ref → `ERR_INVALID_REF`, before any I/O)
2. Resolves service and method descriptors via inline content or server reflection (load failure → `ERR_SOURCE_LOAD_FAILED`; unresolved symbol → `ERR_REF_NOT_FOUND`)
3. Resolves or reuses a cached gRPC client connection (transport per the §4 address form — `grpc://` means plaintext and `grpcs://` means TLS; a bare `host:port` requires an explicit transport election — unless the transport configuration point overrides it)
4. Reads the single request message from the handle (`Write` one input; methods with empty request messages dispatch without one) and builds a dynamic protobuf request from it
5. Applies explicitly named context `headers` as gRPC metadata; generic credentials without an artifact-declared carriage are challenged before dispatch
6. Invokes the RPC using its declared unary, server-streaming, client-streaming, or bidirectional interaction; message flow and half-close behavior emerge from that declaration, with handle backpressure flow-controlling streams

Every non-OK gRPC final status terminates with structural `ERR_EXECUTION_FAILED`; the binding does not derive a portable failure taxonomy or retry policy from native status. Optional `Diagnostics.grpcStatus` preserves the numeric native code, message, and each rich-status detail as its type URL plus exact base64 payload; `FailureEvidenceFrom` is the typed expert accessor. Response messages emitted before a later non-OK final status remain outputs. Leading/trailing metadata is available through `Diagnostics()`. Cancelling the handle (or the invocation context) tears down the underlying stream.

### Credential application

Only the context's explicitly named `headers` map is forwarded as gRPC
metadata. Protobuf has no declaration saying that a generic bearer token,
API key, or username/password belongs in a particular metadata field, so the
binding does not invent one. The operation/context layer may satisfy a
challenge by supplying the service's documented metadata field explicitly.

### Interface synthesis

Converts a live gRPC server into an OBI by:

- Discovering services via gRPC reflection or `.proto` file parsing
- Filtering out infrastructure services (`grpc.reflection.*`, `grpc.health.*`)
- Including unary, server-streaming, client-streaming, and bidirectional RPCs exactly as declared
- Converting protobuf message types to JSON Schema (input and output)
- Generating `package.Service/Method` refs for each binding
- Sorting services and methods alphabetically for deterministic output

## Resource bounds

Named exclusion: this format wires no `MaxDeliveryUnitBytes` read site.
Message size rides grpc-go's native idiom — `MaxCallRecvMsgSize` (4 MB
default) via the existing `WithDialOptions` pass-through — and
openbindings.grpc@1 places size caps with the consumer and the
implementation, outside the binding document.

## License

Apache-2.0
