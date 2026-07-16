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

Typically you don't call the invoker directly — the `OperationInvoker` routes operations to it based on the OBI's source format. But direct use is straightforward:

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

### Consumer hooks

HTTP leaves wire questions the OpenAPI document does not settle: which bytes-to-value rule to apply, whether a given response counts as success, and where the payload lives. This format **consults the consumer hooks seam** (`InvokeHooks`) for all three:

- **Decode** — the builtin rule is chosen from the response `Content-Type`; a `DecodeOutput` hook may override it.
- **Classify** — the builtin verdict is HTTP status (2xx success); a `Classify` hook may reclassify (a 200 envelope carrying an application error, say).
- **Route** — a hook may redirect where the payload is read from.

Each invocation records how the decision was made in its trailer metadata (`builtin` vs `hook`), so a caller can see whether their hook fired. Formats with unambiguous framing (grpc, connect, graphql, mcp, workersrpc) do not consult the seam; `ob plan` reports `not-consulted` for them.

### Interface synthesis

- Operation keys derived from `operationId` when present, otherwise from path + method
- Paths iterated alphabetically, methods in fixed order: get, put, post, delete, options, head, patch, trace
- Input schemas built from parameters (path, query, header) and request body
- Output schemas built from success responses (200, 201, 202)
- No security metadata written to the OBI; `securitySchemes` are honored at invocation time via context negotiation (`CONTEXT_REQUIRED` challenges and the `BindingPreparer` preflight)

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

Converts an OpenAPI 3.x document into an OBI by:
- Extracting operations from each path + method combination
- Building input schemas from parameters and request bodies
- Building output schemas from success responses (200, 201, 202)
- Generating JSON Pointer refs for each binding
- Deriving operation keys from `operationId` or path + method

## License

Apache-2.0
