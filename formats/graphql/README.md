# `formats/graphql`

Go reference implementation of the unreleased first
`openbindings.graphql@1` candidate. It binds GraphQL queries and mutations as
protocol-blind application operations. Schema introspection inventories root
fields; the caller supplies the exact executable document because a schema
cannot choose a selection set.

No GraphQL binding specification has been published, and this package does not
implement an older compatibility meaning for `@1`.

## Install and register

```sh
go get github.com/openbindings/openbindings-go/formats/graphql
```

```go
invoker := openbindings.NewOperationInvoker(graphqlbinding.NewInvoker())
```

## Invoke

Every invocation needs the exact executable GraphQL document at
`context.configuration.document`, with `source` and optional `operationName`:

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
            },
        },
    })

_ = call.Write(ctx, map[string]any{"id": "user_1"})
viewer, err := openbindings.Single(ctx, call.Outputs())
```

The optional caller input becomes the GraphQL variables map wholesale. The
binding never consumes a variable as metadata or generates a document.

On success, the output is `data[responseKey]`, including root aliases. The
GraphQL envelope and HTTP facts do not become ordinary output fields. If a
trusted response contains GraphQL `errors`, any selected partial application
value is emitted first and the invocation then completes unsuccessfully
without retracting it. Native response evidence is available only through the
explicit diagnostics surface.

The candidate's interpretation points live under `context.configuration`:

- `document`: an object with `source` and optional `operationName`.
- `protocolFields`: optional explicitly named HTTP headers and cookies.

Generic credentials do not identify a GraphQL protocol location, so the
invoker refuses them rather than inventing an Authorization scheme.

Present `content` must be one successful introspection execution-result object
with no `errors` member and an object at `data.__schema`. It is authoritative
and displaces live introspection.

## Synthesis and coverage

Synthesis creates one operation for each non-introspection query or mutation
root field. The input is an object variables boundary. The output schema is
derived from the root field's GraphQL type; composite result objects remain
open because nested selection names depend on the executable document.

Subscriptions are excluded with reason
`graphql.subscription_lifecycle_not_representable` and rule `GQL-P-04`.
Their partial-data-plus-error events may continue the native stream, so the
candidate refuses them rather than approximating their lifecycle.

The delivery-unit limit applies to each HTTP response and introspection body;
zero selects the SDK default.

## License

Apache-2.0
