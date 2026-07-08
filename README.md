# openbindings-go

Go monorepo for the [OpenBindings](https://openbindings.com) Go ecosystem: the core SDK plus protocol-specific binding invokers, each as its own Go module. Parse, validate, resolve, and invoke OpenBindings interfaces from Go.

OpenBindings is an open standard: one interface, limitless bindings. An OBI (OpenBindings Interface) document describes what operations a service offers and how to reach them, independent of protocol. See the [spec](https://github.com/openbindings/spec) and [openbindings.com](https://openbindings.com) for details.

**Spec version:** implements OpenBindings 0.2. The exact range is exported as `openbindings.MinSupportedVersion` / `openbindings.MaxTestedVersion`; check programmatically via `openbindings.IsSupportedVersion(version)`.

**Conformance:** `ParseDocument(data)` rejects malformed JSON and duplicate object keys (OBI-D-01), then `Interface.Validate()` enforces OBI-D-02 through OBI-D-13 and OBI-T-04. OBI-D-02 (document validates against `openbindings.schema.json`) and OBI-D-12 (examples validate against their operation's input/output schemas) are enforced via [`santhosh-tekuri/jsonschema/v6`](https://github.com/santhosh-tekuri/jsonschema). The schema is embedded at build time (synced via `scripts/sync-schema.sh`). To exercise the core conformance corpus, check out the spec repo alongside this one (at `../spec`, or `./spec` inside the repo) and run `go test ./...` from the root module.

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
```

The format libraries previously lived in separate repos (`openbindings/openapi-go`, `openbindings/asyncapi-go`, etc.). They were consolidated into this monorepo because they all implement the same `BindingInvoker`/`InterfaceSynthesizer` interfaces from the core SDK and need to evolve in lockstep with it. This pattern matches the modern convention for first-party SDK families in Go (`aws-sdk-go-v2`, `googleapis/google-cloud-go`, `Azure/azure-sdk-for-go`, `open-telemetry/opentelemetry-go`, `kubernetes/kubernetes`).

The [`ob` CLI](https://github.com/openbindings/ob) is built on this SDK but lives in its own repo, with its own versioning and release cadence.

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

The `ob` CLI (separate repo):

```
brew install --cask openbindings/tap/ob
# or
go install github.com/openbindings/ob/cmd/ob@latest
```

## What this SDK does

- **Core types** for the OpenBindings interface document: operations, bindings, sources, transforms, schemas
- **Lossless JSON** round-tripping that preserves unknown fields and `x-*` extensions for forward compatibility
- **Validation** with shape-level checks, strict mode for unknown fields, and format token validation
- **Schema compatibility** checking under the OpenBindings Schema Compatibility Profile v0.1 (covariant outputs, contravariant inputs) with diagnostic reasons
- **`FetchInterface`** for resolving OBIs from URLs: well-known discovery, then synthesis from raw OpenAPI / AsyncAPI / etc. via supplied synthesizers
- **`OperationInvoker`** that dispatches operations to per-format binding invokers and applies transforms
- **Context contracts** for per-origin invocation context (credentials and non-secret configuration), resolved at call time with least-privilege scoping

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

// Wire up an operation invoker with the format(s) you need.
opInv := openbindings.NewOperationInvoker(openapi.NewInvoker())

// Resolve an OBI from a URL (well-known discovery, with synthesis as the
// fallback when the target only exposes a raw spec such as an OpenAPI doc).
fetched, err := openbindings.FetchInterface(ctx, "https://api.example.com",
    openbindings.WithSynthesizers(openapi.NewSynthesizer()))
if err != nil {
    log.Fatal(err)
}
iface := fetched.Interface

// Invoke. One cardinality-agnostic handle serves every operation; a unary call
// writes one input and reads one output. Options are rarely needed; the common
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

For compile-time-typed operations, run `ob codegen <obi> --lang go` to generate an `OperationSignatures` namespace, one typed `OperationSignature[In, Out]` per operation, that you pass to this same `Invoke` for fully-typed input and output.

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
bidirectional bindings. Cardinality is a property of the selected binding,
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
opInv := openbindings.NewOperationInvoker(
    openapi.NewInvoker(),   // handles openapi@^3.0.0
    asyncapi.NewInvoker(),  // handles asyncapi@^3.0.0
    grpc.NewInvoker(),      // handles grpc (versionless)
)
```

| Module | Format token(s) | Synthesizes OBIs? |
|--------|-----------------|-------------------|
| `formats/openapi` | `openapi@^3.0.0` | yes |
| `formats/asyncapi` | `asyncapi@^3.0.0` | yes |
| `formats/graphql` | `graphql` | yes |
| `formats/grpc` | `grpc` | yes |
| `formats/connect` | `connect` | yes |
| `formats/mcp` | `mcp@2025-11-25` | yes |
| `formats/usage` | `usage@^2.0.0`, `usage@^3.0.0` | yes |
| `formats/operationgraph` | `openbindings.operation-graph@0.2.0` | no (graphs are authored, then composed at invoke time) |
| `formats/workersrpc` | `workers-rpc@^1.0.0` | no (Go-side stub; dispatch requires the Workers runtime) |

Invokers implement `BindingInvoker`. Interface synthesizers (which synthesize OBIs from raw specs) implement `InterfaceSynthesizer`. Source inspectors (which enumerate refs in a source) implement `SourceInspector`. A single type may implement any combination.

## Context and authentication

Context is never part of an OBI document. Credentials and other runtime
configuration are supplied per call or resolved at invocation time, keyed by
normalized origin. The context key is `host[:port]` and is scheme-agnostic, so
`http://`, `https://`, and `ws://` for the same origin share context:

```go
key := openbindings.NormalizeContextKey("https://api.example.com/v1/users")
// key = "api.example.com"
```

The SDK defines the `ContextStore` contract (`Get`/`Set`/`Delete` keyed by
origin; values are opaque maps the SDK never inspects) and leaves storage to
the caller: a file, a keychain, or an in-memory map in tests.

A binding that needs context it wasn't given raises a `CONTEXT_REQUIRED`
challenge before any side effect; the operation invoker resolves challenges
through its configured `ContextResolver` and re-drives the binding.
`openbindings.StoreContextResolver(store)` is the store-backed resolver, the
composition of the published binding-invoker and context-store interfaces. It
treats a challenge as a scope, not a hint: via `ScopeContext` it returns only
the credential fields the satisfied requirement-alternative needs, plus
non-secret configuration, never other stored credentials.

```go
opInv := openbindings.NewOperationInvoker(openapi.NewInvoker()).
    WithRuntime(openbindings.StoreContextResolver(store)) // store implements ContextStore
```

Apps that resolve interactively (prompts, browser redirects, keychains) supply
their own resolver instead. Format invokers that can derive requirements from
their source (e.g. OpenAPI `securitySchemes`) also implement the
side-effect-free `BindingPreparer` preflight.

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
