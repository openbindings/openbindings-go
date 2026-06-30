# openbindings-go

Go monorepo for the [OpenBindings](https://openbindings.com) Go ecosystem. Parse, validate, resolve, and invoke OpenBindings interfaces from Go, plus protocol-specific binding invokers and the `ob` CLI.

OpenBindings is an open standard: one interface, limitless bindings. An OBI (OpenBindings Interface) document describes what operations a service offers and how to reach them, independent of protocol. See the [spec](https://github.com/openbindings/spec) and [guides](https://github.com/openbindings/spec/tree/main/guides) for details.

**Spec version:** implements OpenBindings 0.2. Exact range is exported as `openbindings.MinSupportedVersion` / `openbindings.MaxTestedVersion`; check programmatically via `openbindings.IsSupportedVersion(version)`.

**Conformance:** `ParseDocument(data)` rejects malformed JSON and duplicate object keys (OBI-D-01), then `Interface.Validate()` enforces OBI-D-02 through OBI-D-13 and OBI-T-04. OBI-D-02 (document validates against `openbindings.schema.json`) and OBI-D-12 (examples validate against their operation's input/output schemas) are enforced via [`santhosh-tekuri/jsonschema/v6`](https://github.com/santhosh-tekuri/jsonschema). The schema is embedded at build time (synced via `scripts/sync-schema.sh`). In this monorepo, run `go test ./...` from the root module with the spec repo checked out at `./spec` or `../spec` to exercise the core conformance corpus.

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
- **`FetchInterface`** for resolving OBIs from URLs (well-known discovery, then synthesis from raw OpenAPI / AsyncAPI / etc. via supplied creators)
- **`OperationInvoker`** that dispatches operations to per-format binding invokers and applies transforms
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

// Wire up an invoker with the format(s) you need, plus a context resolver for
// any credentials the bindings negotiate at call time.
opInv := openbindings.NewOperationInvoker(openapi.NewInvoker()).
    WithRuntime(openbindings.StoreContextResolver(openbindings.NewMemoryStore()))

// Resolve an OBI from a URL (well-known discovery + creator synthesis).
fetched, err := openbindings.FetchInterface(ctx, "https://api.example.com",
    openbindings.WithCreators(openapi.NewCreator()))
if err != nil {
    log.Fatal(err)
}
iface := &fetched.Interface

// Invoke. One cardinality-agnostic handle serves every operation; a unary call
// writes one input and reads one output. Options are rarely needed — the common
// call passes none.
call := openbindings.Invoke(ctx, opInv, iface,
    openbindings.NewOperationSignature[any, any]("listItems"))
if err := call.Write(ctx, map[string]any{"limit": 10}); err != nil {
    log.Fatal(err)
}
out, err := openbindings.Single(ctx, call.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

For compile-time-typed operations, run `ob codegen <obi> --lang go` to generate an `OperationSignatures` namespace — one typed `OperationSignature[In, Out]` per operation — that you pass to this same `Invoke` for fully-typed input and output.

### Check compatibility

```go
issues := openbindings.CheckInterfaceCompatibility(required, provided)
for _, issue := range issues {
    fmt.Printf("%s: %s — %s\n", issue.Operation, issue.Kind, issue.Detail)
}
```

## Invocation model

Every operation returns a cardinality-agnostic `Invocation[I, O]` handle: the
caller writes input messages until done; the invocation yields output messages
until done. One shape serves unary, server-streaming, client-streaming, and
bidirectional bindings — cardinality is a property of the selected binding,
never of the call signature:

```go
sig := openbindings.NewOperationSignature[any, any]("listItems")
call := openbindings.Invoke(ctx, opInv, iface, sig)
if err := call.Write(ctx, input); err != nil { /* handle */ } // unary: binding closes input after one read
out := call.Outputs()
for {
    item, err := out.Read(ctx)
    if err == io.EOF { break }
    if err != nil { /* terminal *InvocationError */ }
    fmt.Println(item) // bare output value
}
```

For an operation you are confident yields exactly one output, the one blessed
terminal is `openbindings.Single`:

```go
call := openbindings.Invoke(ctx, opInv, iface, openbindings.NewOperationSignature[any, any]("getItem"))
_ = call.Write(ctx, map[string]any{"id": "item_1"})
item, err := openbindings.Single(ctx, call.Outputs())
```

Client-streaming and bidirectional callers own `Close()` (and drive input and
output from separate goroutines); `Header`/`Trailer` carry leading/trailing
metadata and `Cancel()` tears the invocation down. Missing runtime context
surfaces as a `CONTEXT_REQUIRED` terminal error raised before any side effect,
resolved by the operation invoker's `ContextResolver` when one is configured.

## Binding invokers

The SDK routes operations to binding invokers by format token. Invokers declare what formats they handle (including semver ranges like `openapi@^3.0.0`) and the SDK matches OBI source formats against those declarations:

```go
exec := openbindings.NewOperationInvoker(
    openapi.NewInvoker(),   // handles openapi@^3.0.0
    asyncapi.NewInvoker(),  // handles asyncapi@^3.0.0
    grpc.NewInvoker(),      // handles grpc (versionless)
)
```

Invokers implement `BindingInvoker`. Interface creators (which synthesize OBIs from raw specs) implement `InterfaceCreator`. Source inspectors (which enumerate refs in a source) implement `SourceInspector`. A single type may implement any combination. See [Implementing a Binding Format](https://github.com/openbindings/spec/blob/main/guides/implementing-a-binding-format.md) for a step-by-step walkthrough.

## Context and authentication

Credentials are never part of an OBI document — they are context, supplied per
call or resolved at invocation time. The context key is `host[:port]` —
scheme-agnostic, so `http://`, `https://`, and `ws://` for the same host share
context:

```go
store := openbindings.NewMemoryStore()
key := openbindings.NormalizeContextKey("https://api.example.com/v1/users")
// key = "api.example.com"
store.Set(ctx, key, map[string]any{"bearerToken": "tok_123"})
```

A binding that needs context it wasn't given raises a `CONTEXT_REQUIRED`
challenge before any side effect; the operation invoker resolves challenges
through its configured `ContextResolver` and re-drives the binding.
`openbindings.StoreContextResolver(store)` is the store-backed resolver — the
composition of the `binding-invoker` and `context-store` roles — and is wired
in via `OperationInvoker.WithRuntime(resolver)`. Apps that resolve
interactively (prompts, browser redirects, keychains) supply their own
resolver instead. Format invokers that can derive requirements from their
source (e.g. OpenAPI `securitySchemes`) also implement the side-effect-free
`BindingPreparer` preflight.

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
