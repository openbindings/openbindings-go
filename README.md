# openbindings-go

Go monorepo for the [OpenBindings](https://openbindings.com) Go ecosystem: the core SDK plus protocol-specific binding invokers, each as its own Go module. Parse, validate, resolve, and invoke OpenBindings interfaces from Go.

OpenBindings is an open standard. **One interface. Any binding.** Describe
operation contracts separately from how they are realized or consumed. An OBI
(OpenBindings Interface) document can declare operations it makes available
through bindings and named dependencies whose implementations are supplied by
its environment, independently of protocol. See the
[spec](https://github.com/openbindings/spec) and
[guides](https://github.com/openbindings/spec/tree/main/guides) for details.

**Spec version:** implements OpenBindings 0.2. To ask whether this SDK will accept a document of a given version, call `openbindings.IsSupportedVersion(version)` — the OBI-T-04 acceptance oracle: it returns true exactly when `Validate` / `ParseDocument` would process (not refuse) that version, so it is patch-lenient within a supported minor line (a 0.2.0 SDK accepts 0.2.1, 0.2.99, …) and refuses a different major, a pre-1.0 different minor, and unsupported prereleases. `openbindings.MinSupportedVersion` / `openbindings.MaxTestedVersion` / `openbindings.SupportedRange()` are a distinct, narrower notion — the maintainer-*tested* range — and a version can be accepted without falling inside it.

> **Draft status:** this branch implements the unreleased 0.2 working draft.
> The module manifests intentionally require `v0.2.0`, which does not exist
> until the coordinated release is cut. The install commands below describe
> the released package path; they do not install this branch today. Use the
> source-workspace instructions to evaluate 0.2 before release.

**Conformance:** `ParseDocument(data)` rejects malformed JSON and duplicate
object keys (OBI-D-01), then `Interface.Validate()` enforces OBI-D-02 through
OBI-D-12 and OBI-D-16 through OBI-D-19, plus the OBI-T-04 version-refusal
rule. OBI-D-13 and the binding-specification-defined address cases of OBI-D-05
require knowledge of the exact governing binding specification; a core-only
validator leaves those conclusions unverified rather than claiming conformity
or non-conformity, per
[§10.5](https://github.com/openbindings/spec/blob/main/openbindings.md#105-verification-conclusions).
OBI-D-14 and OBI-D-15 are retired identifiers. OBI-D-02, OBI-D-11, and
OBI-D-17 use [`santhosh-tekuri/jsonschema/v6`](https://github.com/santhosh-tekuri/jsonschema);
the core schema and locally required JSON Schema 2020-12 meta-schemas are
embedded at build time. To exercise the core conformance corpus, check out the
spec repo alongside this one (at `../spec`, or `./spec` inside the repo) and
run `go test ./...` from the root module.

The cross-SDK equivalence policy and corresponding public names are recorded
in [`IMPLEMENTATION_PARITY.md`](IMPLEMENTATION_PARITY.md).

**Value-carriage migration (development branch):** generic JSON values decoded
by this Go SDK now retain `json.Number`, rather than incidentally round-tripping
through `float64`. See [`jsonvalue`](jsonvalue/README.md) for caller migration and
the deliberately narrower guarantee. The assembled Go/TypeScript candidate is
qualified separately from shared release activation: JSONata-dependent edges
remain held pending evaluator qualification. Native caller values cannot recover
precision discarded before reaching the SDK.

## Layout

This is a multi-module Go monorepo. The root module carries the core package
plus its `invoke`, `synthesize`, and `compare` sub-packages; each `formats/*`
subdirectory is its own module:

```
.                          ← github.com/openbindings/openbindings-go (the core SDK)
  invoke/                  ← .../invoke (binding-invoker / operation-invoker runtime)
  synthesize/              ← .../synthesize (interface synthesis, source inspection, discovery)
  compare/                 ← .../compare (interface/operation compatibility checking)
  sdk/                     ← .../sdk (optional protocol-neutral composition facade)
formats/
  openapi/                 ← .../formats/openapi
  asyncapi/                ← .../formats/asyncapi
  graphql/                 ← .../formats/graphql
  grpc/                    ← .../formats/grpc
  connect/                 ← .../formats/connect
  mcp/                     ← .../formats/mcp
  usage/                   ← .../formats/usage
  operationgraph/          ← .../formats/operationgraph
```

The binding libraries previously lived in separate repos
(`openbindings/openapi-go`, `openbindings/asyncapi-go`, etc.). They were
consolidated into this monorepo because they all implement the same
`BindingInvoker`/`InterfaceSynthesizer` interfaces from the SDK's `invoke`
and `synthesize` sub-packages and need
to evolve in lockstep with it. Their historical `formats/*` import paths are
package locations, not a claim that every binding specification governs a
document format: a binding specification may incorporate an artifact or
protocol, define its own artifact, or govern an artifactless live surface.

The [`ob` CLI](https://github.com/openbindings/ob) is built on this SDK but lives in its own repo, with its own versioning and release cadence.

## Install

After 0.2 is released:

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

To evaluate the 0.2 draft, clone this repository and create the local
multi-module workspace described in
[`CONTRIBUTING.md`](CONTRIBUTING.md#working-on-this-repo). Clone `spec` and
`interfaces` alongside it to run the required conformance corpora. Do not add
draft-only `replace` directives to an application intended for release.

## What this SDK does

- **Core types** for the OpenBindings interface document: operations,
  dependencies, bindings, sources, transforms, and schemas
- **Lossless JSON** round-tripping that preserves unknown fields and `x-*` extensions for forward compatibility
- **Validation** with shape-level checks, strict mode for unknown fields, and exact binding-specification identifier validation
- **Schema compatibility** checking under the OpenBindings Schema Compatibility Profile v0.1 (covariant outputs, contravariant inputs) with diagnostic reasons
- **`FetchInterface`** for resolving OBIs from URLs: well-known discovery, then synthesis from raw OpenAPI / AsyncAPI / etc. via supplied synthesizers
- **Exhaustiveness-qualified synthesis accounting** through `CoverageSynthesizer`, pairing a creation-time-sound OBI with durable dispositions and an explicit claim about whether the upstream interaction inventory is complete
- **`OperationInvoker`** that dispatches operations to binding-spec implementations and applies transforms
- **`sdk.Runtime`** as an optional instance-scoped composition root over explicitly registered binding providers
- **Prepared provider composition** that resolves named dependencies through an explicit policy into retained, SDK-identified routes
- **Bounded process-local validation diagnostics** that identify contract
  locations without placing rejected values, credentials, transport evidence,
  or validator prose in portable invocation errors
- **Context contracts** for caller-supplied or resolved invocation context, with requirement-scoped provisioning and no assumption that non-credential fields are public

The SDK is the foundation layer. It defines the contracts that binding invokers (OpenAPI, AsyncAPI, gRPC, etc.) implement but does not contain any binding-spec-specific logic itself.

The exact binding specification named by a source is the semantic authority for
that binding. A binding package may implement a specification that incorporates
an upstream standard, deliberately diverges from one, or defines its own
domain. If the specification leaves behavior open, package code may complete
the gap locally, but that completion is implementation-defined and must not be
presented as portable meaning of the identifier. The project binding packages
instead treat such gaps in their unreleased `openbindings.*@1` candidates as
specification work to close before publication.

## Quick start

### Parse and validate an OBI

```go
import (
    "encoding/json"
    openbindings "github.com/openbindings/openbindings-go"
)

// ParseDocument is the conformant front door for untrusted or wire bytes:
// unlike a plain json.Unmarshal it also rejects duplicate object keys
// (OBI-D-01). FetchInterface uses it internally.
iface, err := openbindings.ParseDocument(data)
if err != nil {
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

Named dependencies resolve by exact dependency key and exact canonical local
operation key; dependency keys and local references do not use operation alias
resolution:

```go
dependency, ok := openbindings.LookupDependency(iface, "customerDelivery")
if !ok {
    log.Fatal(openbindings.ErrDependencyNotFound)
}
fmt.Println(dependency.OperationKey, dependency.Dependency.BindingSpecs)
```

### Resolve and invoke operations

```go
import (
    "github.com/openbindings/openbindings-go/invoke"
    obsdk "github.com/openbindings/openbindings-go/sdk"
    openapi "github.com/openbindings/openbindings-go/formats/openapi"
)

// One explicit adapter supplies invocation, synthesis, and source inspection.
runtime, err := obsdk.New(obsdk.RuntimeOptions{
    Providers: []obsdk.BindingProvider{openapi.NewAdapter()},
})
if err != nil {
    log.Fatal(err)
}

// Resolve an OBI from a URL (well-known discovery, with synthesis as the
// fallback when the target only exposes a raw spec such as an OpenAPI doc).
fetched, err := runtime.Resolve(ctx, "https://api.example.com")
if err != nil {
    log.Fatal(err)
}
iface := fetched.Interface

// Invoke. One cardinality-agnostic handle serves every operation; a unary call
// writes one input and reads one output. Options are rarely needed; the common
// call passes none.
call := runtime.Invoke(ctx, iface, "listItems")
if err := call.Write(ctx, map[string]any{"limit": 10}); err != nil {
    log.Fatal(err)
}
out, err := invoke.Single(ctx, call.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

Repeated provider use should prepare one immutable interface snapshot and let
the SDK index exact realization routes once. `sdk.Runtime.PrepareProvider`
prepares a document; `PrepareProviderSnapshot` accepts an already prepared
snapshot without reparsing it. Both use the runtime's cohesive provider
registry—binding identifiers remain exact opaque capability tokens.

**Value-identity architecture:** preparation owns one detached, exact JSON snapshot independently of optional
JCS export. Schema/realization caches belong to that immutable owner; any
cross-snapshot reuse or exact boundary match must verify exact material, not
merely equal lossy fingerprints. Authored boundary graphs preserve schema
structure, array order, presence and reachable resource content; they are not
the comparison profile's normalized schema identity.

`SnapshotID()` is local correlation, not content equality. `ExportJCS()` returns
an explicit canonical export and content revision, or an error if that export
would change a retained value; the working snapshot remains usable. Use
`CompareBoundaryContracts` for exact authored-boundary evidence. Successful
comparisons between immutable owners may be reused internally. These SDK
commitments are separate from Core/binding conformance and the
optional schema-comparison profile; they do not change invocation/context
patterns, mandate third-party fidelity, or qualify JSONata or persistent pins.

For an interactive host that needs to explain
`ERR_OPERATION_VALIDATION_FAILED`, create an `invoke.DiagnosticCollector` with
`invoke.NewDiagnosticCollector` and attach it with
`invoke.WithDiagnosticCollector`. Its bounded snapshot is process-local and
safe to render as phase plus JSON Pointer/keyword evidence. The abstract
`InvocationError` remains code-only; diagnostic evidence never rides its
portable `Data` field.

For compile-time-typed operations, run `ob codegen <obi> --lang go` to generate
an `OperationSignatures` namespace. Pass its typed signature to
`invoke.Invoke(ctx, runtime.OperationInvoker(), iface, signature)`; the dynamic
runtime convenience and typed lower-level call share the same registry.

### Check compatibility

```go
issues := compare.CheckInterfaceCompatibility(required, provided)
for _, issue := range issues {
    fmt.Printf("%s: %s — %s\n", issue.Operation, issue.Kind, issue.Detail)
}
```

### Satisfy a named interface dependency

The consumer OBI's `dependencies` map is the contract authority. Prepare the
consumer and providers once, compose them with explicit application-owned
preference, and resolve the generated dependency signature:

```go
consumer, err := openbindings.PrepareInterface(componentInterface)
if err != nil {
    log.Fatal(err)
}
providerInterface, err := openbindings.PrepareInterface(tasksAPI)
if err != nil {
    log.Fatal(err)
}
provider, err := invoke.PrepareProvider(invoke.PreparedProviderOptions{
    Key:       "tasks-api",
    Interface: providerInterface,
    Runtime:   invoke.NewOperationInvoker(openapi.NewInvoker()),
})
if err != nil {
    log.Fatal(err)
}
defer provider.Close()

session, err := invoke.NewCompositionSession(invoke.CompositionSessionOptions{
    Consumer:  consumer,
    Providers: []invoke.ProviderRegistration{{Provider: provider, Preference: 10}},
})
if err != nil {
    log.Fatal(err)
}
resolution, err := invoke.ResolveDependency(
    ctx, session, contracts.DependencySignatures.Creation,
)
if err != nil {
    log.Fatal(err)
}
if resolution.Status == invoke.DependencyAvailable {
    call := resolution.Route.Invoke(ctx)
    _ = call.Write(ctx, CreateTaskInput{Title: "Ship it"})
    task, err := invoke.Single(ctx, call.Outputs())
    // handle task / err
}
```

Code generation derives each dependency's I/O types from its referenced
operation. Dynamic lookup is explicitly `[any, any]`, while a separately named
unsafe constructor is the only manual typed assertion. The reference policy
distinguishes provider and realization ambiguity, preserves tri-state contract
evidence, and performs no live network or credential preflight during static
resolution. It inspects provider preference tiers from highest to lowest and
stops after the first eligible tier; `InspectDependency` remains the deliberate
exhaustive diagnostics path. Custom policies make that staging explicit with
`ProviderInspectionGroups`.

Native implementations use the same verified route and operation substrate,
addressed only by exact OBI binding key. Generic JSON-domain maps and slices
retain reference identity and are not serialized:

```go
local, err := invoke.PrepareLocalProvider(invoke.PrepareLocalProviderOptions{
    Key:       "local-tasks",
    Interface: providerInterface,
    Implementations: map[string]invoke.LocalBindingImplementation{
        "create.local": invoke.LocalUnary(
            func(ctx context.Context, input map[string]any) (map[string]any, error) {
                return repository.Create(ctx, input)
            },
        ),
    },
})
```

The older `OperationRequirement` family remains as a transitional compatibility
surface while downstream callers migrate; new 0.2 wiring should use prepared
composition.

The core module imports no format module. An OpenAPI-only application depends
only on the core module and `formats/openapi`; other binding implementations
are neither linked nor shipped.

## Invocation model

Every operation returns a cardinality-agnostic `Invocation[I, O]` handle: the
caller writes input messages until done; the invocation yields output messages
until done. One shape serves unary, server-streaming, client-streaming, and
bidirectional bindings. Cardinality is a property of the selected binding,
never of the call signature:

```go
sig := invoke.NewOperationSignature[any, any]("listItems")
call := invoke.Invoke(ctx, opInv, iface, sig)
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
terminal is `invoke.Single`:

```go
call := invoke.Invoke(ctx, opInv, iface, invoke.NewOperationSignature[any, any]("getItem"))
_ = call.Write(ctx, map[string]any{"id": "item_1"})
item, err := invoke.Single(ctx, call.Outputs())
```

`Write`'s error contract makes both styles above safe: every error it returns
is truthful — your ctx error, a flow signal, or, when a terminal has already
fired, the terminal error itself — and the output side always carries the
authoritative verdict. Checking a write error is optional fast-fail, never
required for correctness, and `Close()` never fails.

Two idioms worth knowing. For an operation with **no input**, call `Close()`
(or nothing at all — bindings that need no input dispatch without one). For
an operation that **requires input**, forgetting to `Write` parks the binding
until your ctx cancels. `Close()` without a `Write` fails fast with the
protocol-independent `ERR_MISSING_INPUT` code.

Client-streaming and bidirectional callers own `Close()` (and drive input and
output from separate goroutines); `Cancel()` tears the invocation down.
Protocol metadata remains inside artifact runtimes and binding-specific
interpretation. Missing runtime context
surfaces as a `CONTEXT_REQUIRED` terminal error raised before any side effect,
resolved by the operation invoker's `ContextResolver` when one is configured.

## Binding invokers

The SDK routes operations to binding invokers by exact, opaque `bindingSpec`
identifier. Invokers declare the identifiers they implement, and the SDK uses
exact equality—never semver range matching:

`BindingInvoker` is the intended adapter boundary. General OBI processing and
the protocol-independent invocation surface remain in the SDK; a binding
package adds only the translation and behavior owned by its binding
specification. Beneath that boundary, implementations should use separately
testable, ordinary domain-native machinery where an independent source domain
exists. Binding-defined domains such as Operation Graph may instead depend
directly on OpenBindings concepts. See the binding-specification guide's
[implementation-layering doctrine](https://github.com/openbindings/spec/blob/main/binding-specs/README.md#implementation-layering).

```go
opInv := invoke.NewOperationInvoker(
    openapi.NewInvoker(),   // four exact OpenAPI sibling candidates
    asyncapi.NewInvoker(),  // openbindings.asyncapi@1
    grpc.NewInvoker(),      // openbindings.grpc@1
)
```

| Module | Binding specification | Synthesizes OBIs? |
|--------|-----------------|-------------------|
| `formats/openapi` | `openbindings.openapi-2.0@1` | yes |
| `formats/openapi` | `openbindings.openapi-3.0@1` | yes |
| `formats/openapi` | `openbindings.openapi-3.1@1` | yes |
| `formats/openapi` | `openbindings.openapi-3.2@1` | yes |
| `formats/asyncapi` | `openbindings.asyncapi@1` | yes |
| `formats/graphql` | `openbindings.graphql@1` candidate | yes |
| `formats/grpc` | `openbindings.grpc@1` | yes |
| `formats/connect` | `openbindings.connect@1` | yes |
| `formats/mcp` | `openbindings.mcp@1` candidate | yes |
| `formats/usage` | `openbindings.usage@1` | yes |
| `formats/operationgraph` | `openbindings.operation-graph@1` | no (graphs are authored, then composed at invoke time) |

Every listed binding specification is an unreleased first `@1` candidate.
None has an older published meaning or compatibility revision. Exact opaque
identifier routing is exercised during development so each candidate and its
conformance evidence can be qualified before publication.

The four OpenAPI siblings govern Swagger 2.0, OpenAPI 3.0.0–3.0.4,
3.1.0–3.1.2, and 3.2.0 respectively; one source names exactly one sibling.
`formats/openapi` adapts the standalone
[`openapi-client/go`](https://github.com/openbindings/openapi-client/tree/main/go)
engine to OpenBindings. Synthesis emits ordinary Core JSONata
`inputTransform` expressions that map operation input to the public
`{parameters?, body?}` caller envelope.

Invokers implement `BindingInvoker`. Interface synthesizers implement
`InterfaceSynthesizer`; synthesizers that can return durable, explicitly
exhaustiveness-qualified source accounting implement `CoverageSynthesizer`.
Source inspectors implement `SourceInspector`.
A single type may implement any combination.

Most HTTP-speaking invokers (openapi, asyncapi, graphql, connect) accept an injected `*http.Client` via `NewInvokerWithClient` (it also rides WebSocket upgrade handshakes); mcp takes one through the `WithHTTPClient` option; grpc, which is not `*http.Client`-based, injects transport via `WithDialOptions`/`WithTransportCredentials`. Invokers that pool resources — grpc connections, asyncapi WebSockets, mcp sessions — implement `io.Closer`.

## Context and authentication

Context is never part of an OBI document. Credentials and other runtime
configuration are supplied per call or resolved at invocation time. The
contract does not prescribe storage or keying. For applications that choose
origin-scoped reuse, the SDK provides a scheme-agnostic normalization helper:

```go
key := invoke.NormalizeContextKey("https://api.example.com/v1/users")
// key = "api.example.com"
```

The SDK defines an optional `ContextStore` seam (`Get`/`Set`/`Delete` over a
caller-chosen key) and leaves storage to the caller: a file, a keychain, or an
in-memory map in tests. It is not required by binding invocation.

Credential values use well-known field names, keyed by the requirement
family the challenge declares:

| Requirement | Context field |
|---|---|
| `auth.bearer` | `bearerToken` |
| `auth.apiKey` | `apiKey` |
| `auth.basic` | `basic` (a `{"username","password"}` object) |
| `auth.oauth2` | `accessToken` (plus `refreshToken`, `clientSecret`) |

so satisfying a bearer challenge is one complete call:

```go
_ = store.Set(ctx, invoke.NormalizeContextKey("https://api.example.com"),
    map[string]any{"bearerToken": token})
```

A binding that needs context it wasn't given raises a `CONTEXT_REQUIRED`
challenge before any side effect; the operation invoker resolves challenges
through its configured `ContextResolver` and re-drives the binding.
`invoke.StoreContextResolver(store)` is an optional store-backed
realization of the published binding-invoker challenge. It treats a challenge
as a scope, not a hint: via `ScopeContext` it returns only
the fields the satisfied requirement-alternative names. It does not forward
unrelated headers, cookies, environment values, metadata, configuration, or
credentials from the stored record; any of those can be sensitive. Context the
caller explicitly supplied for the invocation is preserved separately.
Because this resolver is backed by reusable storage, it declines every
alternative unless all of its requirements explicitly set durability to true;
an omitted durability flag means one-shot and cannot authorize stored-context
release or persistence.

```go
opInv := invoke.NewOperationInvoker(openapi.NewInvoker()).
    WithRuntime(invoke.StoreContextResolver(store)) // store implements ContextStore
```

Apps that resolve interactively (prompts, browser redirects, keychains) supply
their own resolver instead. Format invokers that can derive requirements from
their source (e.g. OpenAPI `securitySchemes`) also implement the
side-effect-free `BindingPreparer` preflight.

## Transforms (invoking tools only)

OpenBindings 0.2.0 uses the documented JSONata 2.1 language for binding
transforms. Document validation imports only the runtime family's syntax
package: it does not initialize an evaluator or apply arithmetic budgets.

Invocation remains explicitly dependency-injected. The official adapter uses
the independent JSONata runtime's closed JSON-text boundary:

```go
import jsonataevaluator "github.com/openbindings/openbindings-go/invoke/jsonata"

evaluator, err := jsonataevaluator.New(jsonataevaluator.Options{})
if err != nil {
    return err
}
invoker.TransformEvaluator = evaluator
```

The adapter translates SDK values and errors; the standalone runtime owns
compilation, work budgets and cooperative cancellation. Both the basic and
named-binding invocation interfaces are implemented. Allowed variable names
remain the responsibility of the invoking layer.

This local candidate depends on the provisionally named
`github.com/openbindings/jsonata/go` module. That module is not yet
published. Evaluate it in the coordinated source workspace or from the
qualification artifacts; do not release an application with an inaccessible
private dependency.

Other evaluators can implement the same interfaces. Their own numerical
behavior is not automatically the official runtime's stronger fidelity policy.
The closed environment and JSON result boundary still apply. Graph's separately
pinned evaluator is unchanged; this example does not migrate graph expressions.

## Consumer configuration (hooks)

Where a binding specification exposes a consumer choice because its upstream
authority does not answer a wire question, the consumer configures that
choice — the SDK never guesses from payload bytes.
Three hook axes cover the three wire questions:

- **Decode** (`OutputDecoder`) — how raw bytes become an output value when
  the binding specification leaves configurable (e.g. which lane a CLI's
  stdout carries).
- **Classify** (`ResultClassifier`) — which outcomes are success when the
  binding specification leaves configurable (e.g. diff(1)-style exit codes).
- **Route** (`FieldRouter`) — which channel an input field rides (argv,
  stdin, a temp file) for exec-style formats.

A hook declines by returning `ErrUseDefault`, falling through the chain:
  per-invocation (`WithOutputDecoder`, `WithResultClassifier`,
  `WithFieldRouter`) → invoker-level (the `OperationInvoker` fields) → the
  governing binding specification's explicitly documented fallback, if one
  exists. Binding specifications whose upstream authorities answer their wire
  questions do not expose that choice. See the invocation-configuration guide on
[openbindings.com](https://openbindings.com/spec/invocation-configuration)
for the full model.

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
| `schemaprofile` | Schema Compatibility Profile v0.1 — normalization and directional comparison |

## License

Apache-2.0
