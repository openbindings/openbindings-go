# workers-rpc-go

Go-side stub registration for the `workers-rpc@^1.0.0` OpenBindings binding format.

**This package cannot dispatch Workers RPC calls.** Cloudflare Workers RPC is a JavaScript runtime feature: a sibling Worker exposes a `WorkerEntrypoint` class via a service binding declared in `wrangler.toml`, and the calling Worker invokes methods on it through `env[bindingName][methodName](args)`. The Cloudflare runtime handles structured-clone serialization across the binding boundary. There is no HTTP, no JSON, no URL — and no way for a Go program to participate.

For the actual runtime executor, use [`@openbindings/workers-rpc`](https://github.com/openbindings/openbindings-ts/tree/main/packages/workers-rpc) from inside your Cloudflare Worker.

## Why this Go package exists

Even though Go cannot dispatch Workers RPC, the Go side of the OpenBindings ecosystem still needs to know the format token exists. Without this stub:

1. **`ob create`, `ob diff`, `ob sync`, `ob validate`** would reject any OBI that declares a `workers-rpc@^1.0.0` source as "unknown format". Hand-authored Workers RPC OBIs would be unusable from any Go-based tool.
2. **`ob codegen --lang typescript`** would refuse to generate clients for Workers RPC bindings, even though the generated client itself runs in TypeScript and uses the real `WorkersRpcExecutor`. Codegen needs to recognize the format token to walk the OBI's bindings; it does not need to dispatch.
3. **Validation tooling** that checks whether an OBI's declared formats are recognized would flag every Workers RPC OBI as having an unknown format.

This package solves all three by registering the format token and providing stub implementations of `BindingExecutor` and `InterfaceCreator` that return clear errors directing the caller to the TypeScript runtime if they actually try to dispatch.

## Usage

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    workersrpc "github.com/openbindings/openbindings-go/formats/workersrpc"
)

opExec := openbindings.NewOperationExecutor(
    workersrpc.NewExecutor(), // registers workers-rpc@^1.0.0 as a known format
    // ...other executors...
)
```

The stub's `Formats()` returns `[{Token: "workers-rpc@^1.0.0", Description: "Cloudflare Workers RPC bindings (Go-side stub; dispatch requires the Workers runtime)"}]`. Any attempt to call `ExecuteBinding` or `CreateInterface` returns an error with a message pointing at `@openbindings/workers-rpc`.

## Authoring Workers RPC OBIs

Workers RPC OBIs are **hand-authored**. The contract is the TypeScript class on the target Worker, not a machine-readable spec file, so there is no source artifact for `ob create` to derive operations from. Author the `operations` and `bindings` sections directly:

```json
{
  "openbindings": "0.1.0",
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

The binding's `ref` field is the literal method name on the target `WorkerEntrypoint` class. The source's `location` is symbolic — `workers-rpc://` is a convention that `InterfaceClient.resolve()` recognizes as a non-HTTP source and handles via the embedded interface.

See [`spec/guides/binding-format-conventions.md`](https://github.com/openbindings/spec/blob/main/guides/binding-format-conventions.md) for the broader context on binding format conventions.

## License

Apache-2.0
