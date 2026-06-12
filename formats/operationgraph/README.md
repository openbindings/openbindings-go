# operation-graph-go

Binding invoker for the [`openbindings.operation-graph`](https://openbindings.com/spec) format (the transparency rewrite of `@0.2.0`). Executes operation graphs: directed graphs of typed nodes that compose [OpenBindings](https://openbindings.com) operations, governed by the identity law — `input → operation(y) → output` is observationally indistinguishable from invoking `y` directly.

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
| `input` | The caller's input side, surfaced; each write becomes one event rooting a lineage |
| `output` | The caller's output side; every arriving event is emitted to the caller |
| `operation` | The conduit: one held invocation per graph invocation, piping its incoming stream in |
| `each` | One invocation per arriving event (the per-item built-in; `maxIterations` bounds cycles) |
| `filter` | Gates events by schema or expression |
| `transform` | Reshapes events via a JSONata expression (`$input` is the lineage root) |
| `map` | Unpacks an array into individual events |
| `buffer` | Accumulates events into batches (one accumulator per graph invocation) |
| `combine` | Joins latest-per-source into a keyed object once every source is ready |
| `exit` | Terminates the graph (early return or fatal error) |

Error policy follows the spec: per-event failures (`each` errors, `WRITE_REJECTED`,
`MAP_NOT_ARRAY`, `TRANSFORM_UNDEFINED`) route via `onError` or drop silently;
an unhandled terminal error on an `operation` conduit terminates the graph
invocation with that error (the identity law's terminal-status clause). The
graph back-closes the caller's input side when every direct consumer of the
input node is a non-accepting conduit.

## Conformance

The test suite runs the spec repository's conformance corpus unmodified
(execution fixtures plus the OG-V validation rules). Point `OB_SPEC_CORPUS`
at `spec/conformance/operation-graph` (or keep the local-dev sibling layout)
and run `go test ./...`.

## Links

- [OpenBindings specification](https://openbindings.com/spec)
- [Operation graph format spec](https://github.com/openbindings/spec/blob/main/formats/operation-graph/openbindings.operation-graph.md)
- [Go SDK](https://github.com/openbindings/openbindings-go)

## License

Apache-2.0
