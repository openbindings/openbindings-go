# `formats/graphql`

Go reference implementation of the published
[`openbindings.graphql@1`](https://openbindings.com/binding-specs/graphql/1)
binding specification.

The package invokes GraphQL queries and mutations over GraphQL-over-HTTP,
invokes subscriptions over the pinned `graphql-transport-ws` protocol,
inspects GraphQL schemas, and synthesizes OpenBindings interfaces. It preserves
the distinction the specification depends on: introspection identifies
available root fields, but it cannot choose a caller's selection set.

## Install and register

```sh
go get github.com/openbindings/openbindings-go/formats/graphql
```

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    graphqlbinding "github.com/openbindings/openbindings-go/formats/graphql"
)

invoker := openbindings.NewOperationInvoker(graphqlbinding.NewInvoker())
```

The implementation advertises the exact identifier
`openbindings.graphql@1`. Refs are exact and lower-case:

```text
query/<field>
mutation/<field>
subscription/<field>
```

The root-kind prefix is not a schema type name. Resolution follows the
schema's actual query, mutation, or subscription root type.

## Invoke

Every invocation needs the exact executable GraphQL document. Supply it at
`context.configuration.document` as either source text or an object with
`source` and optional `operationName`:

```go
call := graphqlbinding.NewInvoker().InvokeBinding(ctx,
    &openbindings.BindingInvocationArgs{
        Source: openbindings.InvocationSource{
            BindingSpec: graphqlbinding.BindingSpec,
            Location:    "https://api.example.com/graphql",
        },
        Ref:         "query/viewer",
        InputSchema: map[string]any{"type": "object"},
        Context: map[string]any{
            "configuration": map[string]any{
                "document": map[string]any{
                    "source":        "query Viewer($id: ID!) { viewer(id: $id) { id name } }",
                    "operationName": "Viewer",
                },
                "protocolFields": map[string]any{
                    "httpHeaders": map[string]any{
                        "Authorization": "Bearer tok_123",
                    },
                },
            },
        },
    })

_ = call.Write(ctx, map[string]any{"id": "user_1"})
response, err := openbindings.Single(ctx, call.Outputs())
```

The one caller input value, when present, must be an object and becomes the
GraphQL variables map wholesale. `_query` is an ordinary variable name; this
implementation never consumes it as metadata and never generates a document
or selection set.

The output is the complete GraphQL response envelope, including `data`,
`errors`, and `extensions`. GraphQL errors remain in-band. Use an OBI
`outputTransform` such as `data.viewer` when an operation contract
intentionally exposes only the selected field.

## Runtime configuration

The Go implementation carries the specification's interpretation points below
under `context.configuration`:

- `document`: GraphQL source text, or
  `{"source": "...", "operationName": "..."}`.
- `subscriptionTarget`: required absolute `ws` or `wss` URI for a
  subscription. It is never derived from the HTTP endpoint.
- `protocolFields`: optional `httpHeaders`, `httpCookies`,
  `websocketHeaders`, `websocketCookies`, and
  `connectionInitPayload`.

Header and cookie names identify their exact protocol locations. Generic
`bearerToken`, `apiKey`, `basic`, and OAuth context do not identify such a
location, so the invoker refuses them rather than inventing an Authorization
scheme. Processor-owned HTTP and WebSocket fields, duplicate destinations,
and raw-Cookie/cookie-entry collisions are also refused before dispatch.

`content`, when present, must be one successful introspection execution-result
object with no `errors` member and an object at `data.__schema`. It is a pin
and completely displaces live introspection; wrapper-stripped, bare,
stringified, and SDL representations are not accepted by revision 1.

## Subscriptions

```go
"configuration": map[string]any{
    "document": "subscription { orderUpdates { id status } }",
    "subscriptionTarget": "wss://api.example.com/graphql",
    "protocolFields": map[string]any{
        "connectionInitPayload": map[string]any{"token": "tok_123"},
    },
}
```

Each `next.payload` is one complete output envelope. A protocol `error` is
terminal without retracting already emitted outputs; cancellation attempts the
protocol's `complete` exchange.

## Synthesis and coverage

`NewSynthesizer()` inventories every non-introspection root field and creates
one binding per field. Synthesized operations deliberately use broad schemas:
an object variables boundary for input and a complete GraphQL response
envelope for output. Projecting GraphQL argument or return types into a fixed
JSON shape would falsely imply a selection set that introspection cannot
choose.

`SynthesizeInterfaceWithCoverage` reports exhaustive coverage of the observed
root-field inventory. Each represented entry records the runtime requirement
`document`; subscription entries additionally record `subscriptionTarget`.
Pinned content is inspected without network access and displaces live schema
acquisition.

## Resource bounds

The invocation delivery-unit limit applies to each HTTP response body,
introspection response, and subscription message. Set it on
`BindingInvocationArgs` or the enclosing `OperationInvoker`; zero selects the
SDK default.

## License

Apache-2.0
