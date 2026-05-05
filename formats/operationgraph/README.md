# operation-graph-go

Binding invoker for the [`openbindings.operation-graph`](https://openbindings.com/spec) format. Executes operation graphs: directed graphs of typed nodes that orchestrate [OpenBindings](https://openbindings.com) operations.

## Install

```bash
go get github.com/openbindings/openbindings-go/formats/operationgraph
```

## Usage

The operation graph invoker plugs into an `OperationInvoker` from the [Go SDK](https://github.com/openbindings/openbindings-go). Because it invokes sub-operations, it needs a reference to the `OperationInvoker` itself:

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    openapi "github.com/openbindings/openbindings-go/formats/openapi"
    operationgraph "github.com/openbindings/openbindings-go/formats/operationgraph"
)

// Create the OperationInvoker with protocol-level invokers.
opExec := openbindings.NewOperationInvoker(
    openapi.NewInvoker(),
)

// Create the operation graph invoker and register it.
graphExec := operationgraph.NewInvoker(opExec)
opExec.AddBindingInvoker(graphExec)
```

Once registered, operation graph bindings are executed automatically when you call `Invoke` on an OBI that uses them.

## Node types

The invoker supports all node types defined in the operation graph spec:

| Node | Purpose |
|------|---------|
| `input` | Entry point; receives the caller's input |
| `output` | Exit point; emits events to the output stream |
| `operation` | Invokes an OBI operation |
| `filter` | Gates events by schema or expression |
| `transform` | Reshapes events via a transform expression |
| `map` | Unpacks an array into individual events |
| `buffer` | Accumulates events into batches |
| `combine` | Combines latest value from each source into a keyed object |
| `exit` | Terminates the graph (early return or fatal error) |

All nodes support `onError` for error routing. Unhandled errors are silently dropped.

## Links

- [OpenBindings specification](https://openbindings.com/spec)
- [Operation graph format spec](https://github.com/openbindings/spec/blob/main/formats/operation-graph/openbindings.operation-graph.md)
- [Go SDK](https://github.com/openbindings/openbindings-go)

## License

Apache-2.0
