# SDK runtime architecture

Package `sdk` is the optional protocol-neutral composition root above the
published Core, invocation, synthesis, and inspection contracts. A `Runtime`
owns an explicit, instance-scoped set of binding providers. It installs no
global registry and imports no binding implementation.

For OpenAPI, one `openapi.Adapter` implements `invoke.BindingInvoker`,
`synthesize.CoverageSynthesizer`, and `synthesize.SourceInspector`. The adapter
delegates OpenAPI loading, declaration analysis, request construction, HTTP,
response handling, and streams to `openapi-client/go`; it translates only
OpenBindings contracts and lifecycle.

The dependency rule is strict:

```text
application / OB CLI
  -> sdk.Runtime
     -> protocol-neutral contracts
        -> openapi.Adapter
           -> standalone OpenAPI client and provider projection
```

The runtime may resolve or synthesize an interface, inspect a source, prepare
an operation, and invoke it dynamically. Typed applications use
`runtime.OperationInvoker()` with a generated `invoke.OperationSignature`.
Duplicate exact identifiers listed by registered providers are rejected at
construction; registration order never silently chooses between competing
listed implementations. As in the underlying contracts, `BindingSpecs` is
discovery metadata while `CheckBindingSpecs` remains authoritative for dynamic
support.

Retrieval and transport policy remain explicit at their owning boundaries.
`sdk.RuntimeOptions.HTTPClient` retrieves OBIs during resolution;
`openapi.AdapterOptions` separately configures authoring reads and live API
dispatch. The clients may be the same, but they are not silently conflated
because those paths can require different allowlists, redirects, and TLS
policies.

The lower-level packages remain first-class. A library may import only the
root, `invoke`, or `synthesize` package, and a binding implementation remains
independently usable without the facade. OB CLI owns the concrete list of
installed binding packages and is migrated only after this SDK boundary
passes independently.
