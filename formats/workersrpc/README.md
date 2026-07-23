# formats/workersrpc

Go-side stub registration for the `workers-rpc@^1.0.0` OpenBindings binding format.

> **Legacy experimental adapter.** This package recognizes the historical
> `workers-rpc@^1.0.0` token. It is not an implementation of the unpromoted
> `openbindings.workers-rpc@1` candidate, is not one of the six published 0.2
> binding specifications, and does not participate in the release's portable
> synthesis-coverage guarantee.

**This package cannot dispatch Workers RPC calls.** Cloudflare Workers RPC is a JavaScript runtime feature: a sibling Worker exposes a `WorkerEntrypoint` class via a service binding declared in `wrangler.toml`, and the calling Worker invokes methods on it through `env[bindingName][methodName](args)`. The Cloudflare runtime handles structured-clone serialization across the binding boundary. There is no HTTP, no JSON, no URL, and no way for a Go program to participate.

For the actual runtime invoker, use [`@openbindings/workers-rpc`](https://github.com/openbindings/openbindings-ts/tree/main/packages/workers-rpc) from inside your Cloudflare Worker.

## Why this Go package exists

Even though Go cannot dispatch Workers RPC, the Go side of the OpenBindings ecosystem still needs to know the format token exists. Without this stub:

1. **`ob synthesize`, `ob diff`, `ob validate`** would reject any OBI that declares a `workers-rpc@^1.0.0` source as "unknown format". Hand-authored Workers RPC OBIs would be unusable from any Go-based tool.
2. **`ob codegen --lang typescript`** would refuse to generate typed operation signatures for Workers RPC bindings, even though the generated code runs in TypeScript. Generated code is protocol-agnostic (an `OperationSignatures` namespace passed to a generic `invoke`); consumers wire the real `WorkersRpcInvoker` into their `OperationInvoker` at runtime, inside the Worker. Codegen needs to recognize the format token to walk the OBI's bindings; it does not need to dispatch.
3. **Validation tooling** that checks whether an OBI's declared formats are recognized would flag every Workers RPC OBI as having an unknown format.

This package solves all three by registering the format token and providing stub implementations of `BindingInvoker` (via `NewInvoker`) and `InterfaceSynthesizer` (via `NewSynthesizer`) that fail with clear errors if anything actually tries to dispatch or synthesize.

## Usage

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    workersrpc "github.com/openbindings/openbindings-go/formats/workersrpc"
)

opInv := openbindings.NewOperationInvoker(
    workersrpc.NewInvoker(), // registers workers-rpc@^1.0.0 as a known format
    // ...other invokers...
)
```

The stub invoker's `Formats()` returns `[{Token: "workers-rpc@^1.0.0", Description: "Cloudflare Workers RPC bindings (Go-side stub; dispatch requires the Workers runtime)"}]`. `InvokeBinding` always returns an already-errored invocation handle (terminal `ERR_SOURCE_CONFIG_ERROR`) whose message points at `@openbindings/workers-rpc`. `SynthesizeInterface` returns an error explaining that Workers RPC OBIs are hand-authored.

## Authoring Workers RPC OBIs

Workers RPC OBIs are **hand-authored**. The contract is the TypeScript class on the target Worker, not a machine-readable spec file, so there is no source artifact for `ob synthesize` to derive operations from. Author the `operations` and `bindings` sections directly:

```json
{
  "openbindings": "0.2.0",
  "operations": {
    "mintToken": {
      "input": { "type": "object", "properties": { "user": { "type": "string" } } },
      "output": { "type": "object", "properties": { "access_token": { "type": "string" } } }
    }
  },
  "sources": {
    "auth": {
      "format": "workers-rpc@^1.0.0",
      "location": "workers-rpc://auth-service"
    }
  },
  "bindings": {
    "mintToken.auth": {
      "operation": "mintToken",
      "source": "auth",
      "ref": "mintToken"
    }
  }
}
```

The binding's `ref` field is the literal method name on the target `WorkerEntrypoint` class. The source's `location` is symbolic: `workers-rpc://` is a convention indicating a non-HTTP source that is never fetched from `/.well-known/openbindings`. The OBI document ships with the consumer instead (checked into the calling Worker's repo, typically alongside its generated `OperationSignatures`), and is passed directly to `invoke`.

## License

Apache-2.0
