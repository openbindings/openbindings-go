# openbindings-go

Go monorepo for the [OpenBindings](https://openbindings.com) Go ecosystem. Parse, validate, resolve, and invoke OpenBindings interfaces from Go, plus protocol-specific binding invokers and the `ob` CLI.

OpenBindings is an open standard: one interface, limitless bindings. An OBI (OpenBindings Interface) document describes what operations a service offers and how to reach them, independent of protocol. See the [spec](https://github.com/openbindings/spec) and [guides](https://github.com/openbindings/spec/tree/main/guides) for details.

**Spec version:** implements OpenBindings 0.2. Exact range is exported as `openbindings.MinSupportedVersion` / `openbindings.MaxTestedVersion`; check programmatically via `openbindings.IsSupportedVersion(version)`.

**Conformance:** `ParseDocument(data)` rejects malformed JSON and duplicate object keys (OBI-D-01), then `Interface.Validate()` enforces OBI-D-02 through OBI-D-17 and OBI-T-04. OBI-D-02 (document validates against `openbindings.schema.json`) and OBI-D-15 (examples validate against their operation's input/output schemas) are enforced via [`santhosh-tekuri/jsonschema/v6`](https://github.com/santhosh-tekuri/jsonschema). The schema is embedded at build time (synced via `scripts/sync-schema.sh`). In this monorepo, run `go test ./...` from the root module with the spec repo checked out at `./spec` or `../spec` to exercise the core conformance corpus.

## Layout

This is a multi-module Go monorepo. Each subdirectory below is its own module:

```
.                          ← github.com/openbindings/openbindings-go (the core SDK)
formats/
  openapi/                 ← .../formats/openapi
  asyncapi/                ← .../formats/asyncapi
  graphql/                 ← .../formats/graphql
  grpc/                    ← .../formats/grpc
  connect/                 ← .../formats/connect
  mcp/                     ← .../formats/mcp
  usage/                   ← .../formats/usage
  operationgraph/          ← .../formats/operationgraph
  workersrpc/              ← .../formats/workersrpc
cmd/
  ob/                      ← .../cmd/ob (the CLI binary)
```

The format libraries previously lived in separate repos (`openbindings/openapi-go`, `openbindings/asyncapi-go`, etc.). They were consolidated into this monorepo because they all implement the same `BindingInvoker`/`InterfaceCreator` interfaces from the core SDK and need to evolve in lockstep with it. This pattern matches the modern convention for first-party SDK families in Go (`aws-sdk-go-v2`, `googleapis/google-cloud-go`, `Azure/azure-sdk-for-go`, `open-telemetry/opentelemetry-go`, `kubernetes/kubernetes`).

## Install

Just the core SDK:

```
go get github.com/openbindings/openbindings-go
```

A specific binding invoker (you only pull the deps you need):

```
go get github.com/openbindings/openbindings-go/formats/openapi
go get github.com/openbindings/openbindings-go/formats/asyncapi
# ...
```

The CLI:

```
go install github.com/openbindings/openbindings-go/cmd/ob@latest
```

## What this SDK does

- **Core types** for the OpenBindings interface document: operations, bindings, sources, transforms, schemas, roles
- **Lossless JSON** round-tripping that preserves unknown fields and `x-*` extensions for forward compatibility
- **Validation** with shape-level checks, strict mode for unknown fields, and format token validation
- **Schema compatibility** checking under the OpenBindings Profile v0.1 (covariant outputs, contravariant inputs) with diagnostic reasons
- **InterfaceClient** for resolving OBIs from URLs, well-known discovery, or synthesis from raw specs
- **OperationInvoker** for routing operations to binding invokers by format, with transform support
- **Context store** for per-host credential persistence with scheme-agnostic key normalization

The SDK is the foundation layer. It defines the contracts that binding invokers (OpenAPI, AsyncAPI, gRPC, etc.) implement but does not contain any format-specific logic itself.

## Quick start

### Parse and validate an OBI

```go
import (
    "encoding/json"
    openbindings "github.com/openbindings/openbindings-go"
)

var iface openbindings.Interface
if err := json.Unmarshal(data, &iface); err != nil {
    log.Fatal(err)
}
if err := iface.Validate(); err != nil {
    log.Fatal(err)
}

fmt.Println(iface.Name, iface.Version)
for name, op := range iface.Operations {
    fmt.Println(name, op.Description)
}
```

### Resolve and invoke operations

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    openapi "github.com/openbindings/openbindings-go/formats/openapi"
)

// Create an invoker with format support
exec := openbindings.NewOperationInvoker(openapi.NewInvoker())

// Create a client and resolve an OBI from a URL
client := openbindings.NewInterfaceClient(nil, exec,
    openbindings.WithContextStore(openbindings.NewMemoryStore()),
)
if err := client.Resolve(ctx, "https://api.example.com"); err != nil {
    log.Fatal(err)
}

// Invoke an operation — everything is a stream
ch, err := client.Invoke(ctx, "listItems", map[string]any{"limit": 10})
if err != nil {
    log.Fatal(err)
}
for ev := range ch {
    if ev.Error != nil {
        log.Fatal(ev.Error.Message)
    }
    fmt.Println(ev.Data)
}
```

### Check compatibility

```go
issues := openbindings.CheckInterfaceCompatibility(required, provided)
for _, issue := range issues {
    fmt.Printf("%s: %s — %s\n", issue.Operation, issue.Kind, issue.Detail)
}
```

## Invocation model

Every operation returns a stream of events (`<-chan StreamEvent`). A unary operation produces one event and closes the channel. A streaming operation produces many. The consumer code is the same for both:

```go
ch, err := invoker.Invoke(ctx, input)
for ev := range ch {
    if ev.Error != nil { /* handle */ }
    fmt.Println(ev.Data)
}
```

## Binding invokers

The SDK routes operations to binding invokers by format token. Invokers declare what formats they handle (including semver ranges like `openapi@^3.0.0`) and the SDK matches OBI source formats against those declarations:

```go
exec := openbindings.NewOperationInvoker(
    openapi.NewInvoker(),   // handles openapi@^3.0.0
    asyncapi.NewInvoker(),  // handles asyncapi@^3.0.0
    grpc.NewInvoker(),      // handles grpc (versionless)
)
```

Invokers implement `BindingInvoker`. Interface creators (which synthesize OBIs from raw specs) implement `InterfaceCreator`. A single type may implement both.

## Context store

Credentials are stored per host, not per request. The context key is `host[:port]` — scheme-agnostic, so `http://`, `https://`, and `ws://` for the same host share credentials:

```go
store := openbindings.NewMemoryStore()
key := openbindings.NormalizeContextKey("https://api.example.com/v1/users")
// key = "api.example.com"
store.Set(ctx, key, map[string]any{"bearerToken": "tok_123"})
```

Invokers read from the context store automatically when it's configured on the `OperationInvoker` or `InterfaceClient`.

## Security

OBI documents can declare security methods on bindings via the `security` section. The SDK provides:

- `SecurityMethod` -- describes an authentication method (bearer, oauth2, basic, apiKey)
- `Interface.Security` -- named security entries referenced by bindings
- `BindingEntry.Security` -- references a key in the security section
- `ResolveSecurity` -- walks security methods in preference order, uses `PlatformCallbacks` to acquire credentials interactively

The `OperationInvoker` resolves security methods from the OBI and passes them to binding invokers via `BindingInvocationInput.Security`.

## Schema compatibility profile

The `schemaprofile` subpackage implements the OpenBindings Schema Compatibility Profile v0.1 for deterministic schema comparison:

```go
import "github.com/openbindings/openbindings-go/schemaprofile"

norm := &schemaprofile.Normalizer{}
ok, reason, err := norm.OutputCompatible(targetSchema, candidateSchema)
if err != nil {
    log.Fatal(err)
}
if !ok {
    fmt.Println("Incompatible:", reason)
    // e.g. "type: candidate allows \"array\" but target does not"
}
```

The profile handles: type sets, const/enum, object properties and required fields, additionalProperties, array items, numeric bounds, string/array length bounds, oneOf/anyOf unions, and allOf flattening.

## Subpackages

| Package | Purpose |
|---------|---------|
| `canonicaljson` | RFC 8785 (JCS) deterministic JSON serialization |
| `formattoken` | Parse and match `name@version` format tokens with semver range support |
| `schemaprofile` | Schema Compatibility Profile v0.1 — normalization and directional comparison |

## License

Apache-2.0
