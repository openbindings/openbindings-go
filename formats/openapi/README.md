# formats/openapi

OpenAPI 3.x binding invoker and interface synthesizer for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to invoke operations against OpenAPI specs and synthesize OBI documents from them. It reads OpenAPI 3.x documents, constructs HTTP requests, applies credentials via security schemes, and yields results through the SDK's cardinality-agnostic `Invocation` handle.

See the [spec](https://github.com/openbindings/spec) and the [invocation pattern](https://openbindings.com/spec/invocation-pattern) for how binding invokers and interface synthesizers fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/openapi
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    openapi "github.com/openbindings/openbindings-go/formats/openapi"
)

opInv := openbindings.NewOperationInvoker(openapi.NewInvoker())
```

The invoker declares the binding specification `openbindings.openapi@1` — it handles OpenAPI 3.0.x and 3.1.x documents.

### Invoke a binding

Typically you don't call the invoker directly — the `OperationInvoker` routes operations to it based on the OBI source's binding specification. But direct use is straightforward:

```go
invoker := openapi.NewInvoker()

inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        BindingSpec: openapi.BindingSpec, // "openbindings.openapi@1"
        Location:    "https://api.example.com/openapi.json",
    },
    Ref:     "#/paths/~1users/get",
    Context: map[string]any{"bearerToken": "tok_123"},
})

// Input flows through the handle, not the args.
if err := inv.Write(ctx, map[string]any{"limit": 10}); err != nil {
    log.Fatal(err)
}

// Unary: assert exactly one output.
out, err := openbindings.Single(ctx, inv.Outputs())
if err != nil {
    log.Fatal(err) // terminal *openbindings.InvocationError
}
fmt.Println(out)
```

### Synthesize an interface from an OpenAPI spec

```go
synth := openapi.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        BindingSpec: openapi.BindingSpec,
        Location:    "https://api.example.com/openapi.json",
    }},
})
// iface is a fully-formed OBInterface with operations, bindings, and sources
```

## Behavior

This package implements the published [`openbindings.openapi@1`](https://github.com/openbindings/spec/blob/main/binding-specs/openapi/openbindings.openapi.md) binding specification; that document is normative for input mapping (the flattened model, OAS style/explode serialization), request media selection, server resolution, interaction shape, and channel assembly.

### Binding specification identifier

`openbindings.openapi@1` (exact, opaque). Accepts OpenAPI 3.0.x and 3.1.x documents, discriminated by the artifact's own `openapi` field.

### Ref format

JSON Pointer into the OpenAPI document: `#/paths/<escaped-path>/<method>`

- `#/paths/~1users/get` - GET /users
- `#/paths/~1users~1{id}/put` - PUT /users/{id}

Path separators are escaped per RFC 6901: `/` becomes `~1`, `~` becomes `~0`. The method is lowercase exactly as the artifact spells it — an uppercase method is refused, never case-folded (OAPI-D-03).

### Source expectations

- **`location`**: absolute URI addressing the OpenAPI JSON/YAML document (it also serves as the artifact's base URI for relative `$ref`s and relative server URLs).
- **`content`**: the inline OpenAPI document (content primacy; a co-present `location` is its base URI). Content with no location must be self-contained.

### Server selection

By default the request target is the OAS effective server list's first entry (operation `servers`, else path item's, else document's, else the implied `/`), with server-variable defaults substituted; a relative server URL resolves against the source `location`. Server resolution is the specification's named configuration point: set `configuration.server` in the invocation context to select another declared entry (`url` or `index`), supply `variables`, or supply a complete `baseUrl` outright:

```go
Context: map[string]any{
    "configuration": map[string]any{
        "server": map[string]any{"baseUrl": "https://staging.example.com"},
    },
    "bearerToken": "tok_123",
}
```

The legacy `metadata.baseURL` override still works, below the configuration point.

### Custom HTTP client

`NewInvokerWithClient(*http.Client)` supplies the client used for every request (corporate proxy, mTLS client certificate, custom CA pool, tracing transport). Do not set an overall `Timeout` on it; cancellation is driven by the invocation context. `NewInvoker()` uses a default client with a 10-redirect cap.

### Credential application

Credentials are applied based on the OpenAPI spec's `securitySchemes`:

- `http` + `bearer`: `Authorization: Bearer <token>` from `bearerToken`
- `http` + `basic`: `Authorization: Basic <encoded>` from `basic.username`/`basic.password`
- `apiKey`: Placed in header, query, or cookie as the spec declares, from `apiKey`
- `oauth2` / `openIdConnect`: `Authorization: Bearer <token>` from the `accessToken` (or `bearerToken`) context field

When no security schemes are defined, falls back to bearer, then basic, then apiKey.

When declared security is unsatisfied by the context, the invoker challenges `CONTEXT_REQUIRED` before any input is read or network touched, deriving the challenge from the artifact's `securitySchemes` (the negotiation surface is the [binding-invoker interface](https://openbindings.com/interfaces/binding-invoker); the challenge's `target` is the resolved base URL):

| Scheme | Requirement type | Carried fields |
| --- | --- | --- |
| `http` / `basic` | `auth.basic` | — |
| `http` / `bearer` | `auth.bearer` | — |
| `apiKey` | `auth.apiKey` | — |
| `oauth2` | `auth.oauth2` | `grantType`, `authorizeUrl`, `tokenUrl`, `scopes` from the selected flow |
| `openIdConnect` | `auth.oauth2` | `openIdConnectUrl` |

An `oauth2` scheme declaring several flows selects one by fixed preference — `authorizationCode`, then `implicit`, then `password`, then `clientCredentials` — and `grantType` names the selection in its RFC 6749 spelling (`authorization_code`, `implicit`, `password`, `client_credentials`). Every requirement carries the scheme's declared name (its `securitySchemes` key), which disambiguates ANDed requirements of one type and keys the scheme-scoped credential lookup: an API-key scheme named `N` resolves `apiKeys[N]` first, falling back to the single `apiKey` convenience.

A scheme outside this table is **surfaced, never dropped**: it emits a requirement typed from the artifact's own scheme (`http`/`digest` → `auth.http.digest`; any other type `T` → `auth.<T>`, e.g. `auth.mutualTLS`) that this package cannot itself apply. The alternative stays discoverable — unselectable only for runtimes without a resolver for that family — and a document whose every alternative is unmapped produces a readable challenge instead of an unauthenticated dispatch into a blind 401.

### Consumer hooks

HTTP leaves wire questions the OpenAPI document does not settle: which bytes-to-value rule to apply, whether a given response counts as success, and where the payload lives. This format **consults the consumer hooks seam** (`InvokeHooks`) for all three:

- **Decode** — the builtin rule is chosen from the response `Content-Type`; a `DecodeOutput` hook may override it.
- **Classify** — the builtin verdict is HTTP status (2xx success); a `Classify` hook may reclassify (a 200 envelope carrying an application error, say).
- **Route** — a hook may redirect where the payload is read from.

Each invocation records how the decision was made in its trailer metadata (`builtin` vs `hook`), so a caller can see whether their hook fired. Formats with unambiguous framing (grpc, connect, graphql, mcp, workersrpc) do not consult the seam; `ob plan` reports `not-consulted` for them.

### Interface synthesis

Deterministic generation of OBI documents is a synthesis concern outside the binding specification (`openbindings.openapi@1` §10); these are this package's conventions, chosen so both reference SDKs emit an identical OBI for the same artifact:

- **Operation keys** come from `operationId` when present, sanitized to the OBI key grammar (non-key characters become `_`, leading/trailing `_` trimmed, a leading non-letter gets an `_` prefix). An `operationId` whose sanitized key is already taken falls through to path+method derivation: template segments (`{id}`) dropped, remaining segments joined with `.`, the lowercased method appended (`/users/{id}` + `GET` → `users.get`), then deduplicated deterministically with `_2`, `_3`, … suffixes.
- **Iteration order is fixed**: paths alphabetically, methods in the order get, put, post, delete, options, head, patch, trace.
- **Input schemas** merge path-level and operation-level parameters (path, query, header) with the request body's properties into the flattened contract. Cookie parameters are excluded from synthesized input schemas.
- **Output schemas** come from the first declared success response of `200`, `201`, `202` (in that order), reading the JSON-preferring media type: exact `application/json` first, else the lexicographically least declared media type containing `json`, else the lexicographically least declared media type.
- **Schema translation** targets JSON Schema 2020-12 (spec OBI-D-06), keyed on the artifact's declared `openapi` version: 3.0.x schemas are normalized out of the Draft-4 subset dialect; 3.1.x schemas pass through unchanged.
- **Param/body collisions**: a field declared as both a parameter and a body property is one name, one value, delivered to every declared wire location at invocation (`openbindings.openapi@1` §9.1); synthesis surfaces the merge as the `openapi.param_body_collision` warning.
- **No security metadata is written to the OBI**; `securitySchemes` are honored at invocation time via context negotiation (`CONTEXT_REQUIRED` challenges and the `BindingPreparer` preflight).

## How it works

### Invocation flow

1. Loads and caches the OpenAPI document (JSON or YAML, local or remote), discriminating the accepted 3.0.x/3.1.x lines
2. Parses the ref as a JSON Pointer (`#/paths/~1users/get` -> path `/users`, method `get`)
3. Resolves the server (effective list + variables + the `server` configuration point)
4. Routes input fields per the flattened model — parameters serialize per the OAS style/explode rules; unmatched fields pass into a declared request body and refuse loudly otherwise — and selects the request media type per the specification's preference order
5. Applies credentials from the context using the spec's `securitySchemes` (bearer, basic, apiKey with correct placement), refusing credential/parameter channel collisions pre-dispatch
6. Makes the HTTP request; the declared success media bound the interaction shape (unary, or server-streaming for a declared `text/event-stream` response), and results emit through the invocation handle

### Credential application

Credentials are applied based on the OpenAPI spec's security configuration:

- **`http` + `bearer`**: Sets `Authorization: Bearer <token>` from `bearerToken` context field
- **`http` + `basic`**: Sets `Authorization: Basic <encoded>` from `basic.username`/`basic.password` context fields
- **`apiKey`**: Places the `apiKey` context field in the header, query param, or cookie as the spec declares
- **`oauth2` / `openIdConnect`**: Sets `Authorization: Bearer <token>` from the `accessToken` (or `bearerToken`) context field

When no security schemes are defined, falls back to bearer -> basic -> apiKey in that order.

### Interface synthesis

Converts an OpenAPI 3.x document into an OBI: operations extracted from each path + method combination, input/output schemas from parameters, request bodies, and success responses, and a JSON Pointer ref per binding. The derivation conventions (key derivation, iteration order, media selection, schema translation) are specified under [Behavior → Interface synthesis](#interface-synthesis) above.

## Resource bounds

The unary response body is one **delivery unit** and is consumer-bounded:
set `MaxDeliveryUnitBytes` on the `OperationInvoker` (or per invocation on
`BindingInvocationArgs`); zero selects `openbindings.DefaultMaxDeliveryUnitBytes`
(10 MiB). The SSE line-scanner's internal 16 MB line guard is deliberately
fixed — a parser guard, not a delivery-unit bound.

## License

Apache-2.0
