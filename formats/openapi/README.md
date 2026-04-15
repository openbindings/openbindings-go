# openapi-go

OpenAPI 3.x binding executor and interface creator for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to execute operations against OpenAPI specs and synthesize OBI documents from them. It reads OpenAPI 3.x documents, constructs HTTP requests, applies credentials via security schemes, and returns results as a stream of events.

See the [spec](https://github.com/openbindings/spec) and [pattern documentation](https://github.com/openbindings/spec/tree/main/patterns) for how executors and creators fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/openapi
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationExecutor

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    openapi "github.com/openbindings/openbindings-go/formats/openapi"
)

exec := openbindings.NewOperationExecutor(openapi.NewExecutor())
```

The executor declares `openapi@^3.0.0` — it handles any OpenAPI 3.x spec.

### Execute a binding

Typically you don't call the executor directly — the `OperationExecutor` routes operations to it based on the OBI's source format. But direct use is straightforward:

```go
executor := openapi.NewExecutor()
ch, err := executor.ExecuteBinding(ctx, &openbindings.BindingExecutionInput{
    Source: openbindings.ExecuteSource{
        Format:   "openapi@3.1",
        Location: "https://api.example.com/openapi.json",
    },
    Ref:     "#/paths/~1users/get",
    Context: map[string]any{"bearerToken": "tok_123"},
})
for ev := range ch {
    if ev.Error != nil {
        log.Fatal(ev.Error.Message)
    }
    fmt.Println(ev.Data)
}
```

### Create an interface from an OpenAPI spec

```go
creator := openapi.NewCreator()
iface, err := creator.CreateInterface(ctx, &openbindings.CreateInput{
    Sources: []openbindings.CreateSource{{
        Format:   "openapi@3.1",
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

### Credential application

Credentials are applied based on the OpenAPI spec's `securitySchemes`:

- `http` + `bearer`: `Authorization: Bearer <token>` from `bearerToken`
- `http` + `basic`: `Authorization: Basic <encoded>` from `basic.username`/`basic.password`
- `apiKey`: Placed in header, query, or cookie as the spec declares, from `apiKey`

When no security schemes are defined, falls back to bearer, then basic, then apiKey.

### Interface creation

- Operation keys derived from `operationId` when present, otherwise from path + method
- Paths iterated alphabetically, methods in fixed order: get, put, post, delete, options, head, patch, trace
- Input schemas built from parameters (path, query, header) and request body
- Output schemas built from success responses (200, 201, 202)
- Security schemes extracted and mapped to OBI security entries

## How it works

### Execution flow

1. Loads and caches the OpenAPI document (JSON or YAML, local or remote)
2. Parses the ref as a JSON Pointer (`#/paths/~1users/get` -> path `/users`, method `get`)
3. Resolves the base URL from the spec's `servers` array
4. Classifies input fields as path, query, header, or body parameters based on the OpenAPI parameter definitions
5. Applies credentials from the context using the spec's `securitySchemes` (bearer, basic, apiKey with correct placement)
6. Makes the HTTP request and returns the result as a stream event

### Credential application

Credentials are applied based on the OpenAPI spec's security configuration:

- **`http` + `bearer`**: Sets `Authorization: Bearer <token>` from `bearerToken` context field
- **`http` + `basic`**: Sets `Authorization: Basic <encoded>` from `basic.username`/`basic.password` context fields
- **`apiKey`**: Places the `apiKey` context field in the header, query param, or cookie as the spec declares

When no security schemes are defined, falls back to bearer -> basic -> apiKey in that order.

### Interface creation

Converts an OpenAPI 3.x document into an OBI by:
- Extracting operations from each path + method combination
- Building input schemas from parameters and request bodies
- Building output schemas from success responses (200, 201, 202)
- Generating JSON Pointer refs for each binding
- Deriving operation keys from `operationId` or path + method

## License

Apache-2.0
