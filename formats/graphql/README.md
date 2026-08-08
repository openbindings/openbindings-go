# `formats/graphql`

Go reference implementation of
[`openbindings.graphql@2`](https://openbindings.com/binding-specs/graphql/2),
plus the immutable
[`openbindings.graphql@1`](https://openbindings.com/binding-specs/graphql/1)
compatibility revision.

Revision 2 binds GraphQL queries and mutations as protocol-blind application
operations. Schema introspection inventories root fields, while the caller
supplies the exact executable document because introspection cannot choose a
selection set.

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

The implementation advertises both exact identifiers, latest first:

| Revision | Refs | Ordinary output |
| --- | --- | --- |
| `openbindings.graphql@2` | `query/<field>`, `mutation/<field>` | selected root-field application value |
| `openbindings.graphql@1` | those refs plus `subscription/<field>` | complete GraphQL response envelope |

## Invoke revision 2

Every invocation needs the exact executable GraphQL document at
`context.configuration.document`, as source text or an object with `source`
and optional `operationName`:

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
viewer, err := openbindings.Single(ctx, call.Outputs())
```

The optional caller input, when present, must be an object and becomes the
GraphQL variables map wholesale. `_query` is an ordinary variable name; the
binding never consumes it as metadata or generates a document.

On success, the output is `data[responseKey]`, where `responseKey` includes a
root alias when present. The GraphQL envelope, HTTP status, headers, and bytes
do not become ordinary output fields. If a trusted response contains GraphQL
`errors`, any selected partial application value is emitted first and the
invocation then completes unsuccessfully without retracting that value.
Native response evidence may be inspected only through the invocation's
explicit diagnostics surface; correct application use does not depend on it.

## Runtime configuration

Revision 2 carries these interpretation points under
`context.configuration`:

- `document`: GraphQL source text, or
  `{"source": "...", "operationName": "..."}`.
- `protocolFields`: optional explicitly named HTTP headers and cookies.

Generic credentials do not identify a GraphQL protocol location, so the
invoker refuses them rather than inventing an Authorization scheme.
Processor-owned fields, duplicate destinations, and cookie collisions are
also refused before dispatch.

Present `content` must be one successful introspection execution-result object
with no `errors` member and an object at `data.__schema`. It is authoritative
and displaces live introspection; wrapper-stripped, bare, stringified, and SDL
representations are not accepted.

## Synthesis and coverage

Revision-2 synthesis creates one operation for each non-introspection query or
mutation root field. The input is an object variables boundary. The output
schema is derived from the root field's GraphQL type; composite result objects
remain open because nested selection names depend on the executable document.

Subscription fields are reported as excluded with reason
`graphql.subscription_lifecycle_not_representable` and rule `GQL-P-04`.
GraphQL subscription events can contain partial data and errors while the
native stream continues, which revision 2 deliberately refuses rather than
approximates.

## Revision 1 compatibility

Use `graphqlbinding.LegacyBindingSpec` only when compatibility requires the
published revision-1 contract. Revision 1 emits complete response envelopes
and supports `graphql-transport-ws` subscriptions using the additional
`subscriptionTarget` and WebSocket protocol fields. `FailureEvidenceFrom`
recovers its native HTTP or WebSocket failure evidence from diagnostics.

## Resource bounds

The delivery-unit limit applies to each HTTP response body, introspection
response, and revision-1 subscription message. Set it on
`BindingInvocationArgs` or the enclosing `OperationInvoker`; zero selects the
SDK default.

## License

Apache-2.0
