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

The invoker declares `openapi@^3.0.0` — it handles any OpenAPI 3.x spec.

### Invoke a binding

Typically you don't call the invoker directly — the `OperationInvoker` routes operations to it based on the OBI's source format. But direct use is straightforward:

```go
invoker := openapi.NewInvoker()

inv := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        Format:   "openapi@3.1.0",
        Location: "https://api.example.com/openapi.json",
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
        Format:   "openapi@3.1.0",
        Location: "https://api.example.com/openapi.json",
    }},
})
// iface is a fully-formed OBInterface with operations, bindings, and sources
```

## Conventions

These are non-normative conventions specific to the `openapi` binding format.

### Format token

`openapi@^3.0.0` (caret range). Matches any OpenAPI 3.x document.

### Ref format

JSON Pointer into the OpenAPI document: `#/paths/<escaped-path>/<method>`

- `#/paths/~1users/get` - GET /users
- `#/paths/~1users~1{id}/put` - PUT /users/{id}

Path separators are escaped per RFC 6901: `/` becomes `~1`, `~` becomes `~0`. Method is lowercase.

### Source expectations

- **`location`**: URL or file path to the OpenAPI JSON/YAML document. Resolved relative to the OBI document location.
- **`content`**: Inline OpenAPI document (parsed directly, bypasses location).

### Base URL override

By default the request target is the spec's first `servers[]` entry (a relative server URL like `/api/v3` is resolved against the source `location`'s origin). To send the same OBI at a different host, e.g. staging or a local mock, set `metadata.baseURL` in the invocation context:

```go
Context: map[string]any{
    "metadata":    map[string]any{"baseURL": "https://staging.example.com"},
    "bearerToken": "tok_123",
}
```

`metadata.baseURL` takes precedence over the spec's `servers`. When absent and the spec has no server URL, invocation fails with a message telling you to set one or the other.

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

1. Loads and caches the OpenAPI document (JSON or YAML, local or remote)
2. Parses the ref as a JSON Pointer (`#/paths/~1users/get` -> path `/users`, method `get`)
3. Resolves the base URL from the spec's `servers` array
4. Classifies input fields as path, query, header, or body parameters based on the OpenAPI parameter definitions
5. Applies credentials from the context using the spec's `securitySchemes` (bearer, basic, apiKey with correct placement)
6. Makes the HTTP request and emits the result as the invocation's output

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
