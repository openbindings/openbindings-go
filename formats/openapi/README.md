# formats/openapi

Thin OpenAPI binding-invoker and interface-synthesizer adapters for the
[OpenBindings](https://openbindings.com) Go SDK. Artifact execution is owned by
the standalone [`openapi-client/go`](https://github.com/openbindings/openapi-client)
module; this package maps OpenBindings contracts onto it.

This package enables OpenBindings to invoke operations against OpenAPI specs and synthesize OBI documents from them. It reads OpenAPI 3.x documents, constructs HTTP requests, applies credentials via security schemes, and yields results through the SDK's cardinality-agnostic `Invocation` handle.

See the [spec](https://github.com/openbindings/spec) and the [invocation pattern](https://openbindings.com/spec/invocation-pattern) for how binding invokers and interface synthesizers fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/openapi
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Invoke an artifact without an OBI

Use the standalone client when an application needs OpenAPI without an OBI or
OpenBindings SDK dependency:

```go
client, err := openapiclient.Load(ctx, openapiclient.Source{
    Location: "https://api.example.com/openapi.json",
}, openapiclient.ClientOptions{
    Auth: map[string]any{"session": "tok_123"},
})
if err != nil {
    log.Fatal(err)
}
result, err := client.Call(ctx, openapiclient.OperationRef(
    "#/paths/~1users/get",
), openapiclient.Input{
    Parameters: openapiclient.Parameters{Query: map[string]any{"limit": 10}},
})
```

`Runtime` remains as a Core-invocation-shaped compatibility façade, but it now
delegates to that same engine. New standalone applications should use
`openapiclient.Client`; `Invoker` is the OpenBindings bridge and `Synthesizer`
constructs OBI documents.

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    openapi "github.com/openbindings/openbindings-go/formats/openapi"
)

opInv := openbindings.NewOperationInvoker(openapi.NewInvoker())
```

The invoker declares the unreleased first `openbindings.openapi@1` candidate.
No OpenAPI binding specification has been published and there are no
compatibility revisions. It handles exactly OpenAPI 3.0.0–3.0.4 and
3.1.0–3.1.2 documents. The candidate includes collision-preserving routed
inputs, raw-octet request and response carriage, configured media ranges,
dynamic-object carriage, declaration-complex exact JSON carriage, and exact
schema-omitted OAS 3.0 byte carriage.

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

An HTTP response classified as unsuccessful completes with a terminal
`*openbindings.InvocationError`; it does not become an operation output. Its
ordinary code and message are protocol-independent. Use
`openapi.FailureEvidenceFrom(err)` as the explicit diagnostic escape hatch to
recover the native HTTP status, headers,
final URL/status text where available, exact response bytes, and the OpenAPI
Response Object key/media that governed the response. `BodyCaptured` separates
an exact empty body from an uncaptured body. Non-2xx SSE responses use the same
bounded exact-byte capture as unary failures. The legacy
`openbindings.HTTPStatus` and `HTTPResponseBody` accessors remain available for
status and a convenience text view.

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

This package implements the unreleased first
[`openbindings.openapi@1` candidate](https://github.com/openbindings/spec/blob/main/binding-specs/openapi/openbindings.openapi.md).
That document defines routed input mapping, OAS serialization, request and
response media selection, server resolution, interaction shape, and channel
assembly for qualification before publication.

### Binding specification identifier

`openbindings.openapi@1` (exact and opaque). It accepts exactly OpenAPI
3.0.0–3.0.4 and 3.1.0–3.1.2 documents, discriminated by the artifact's own
`openapi` field.

### Ref format

JSON Pointer into the OpenAPI document: `#/paths/<escaped-path>/<method>`

- `#/paths/~1users/get` - GET /users
- `#/paths/~1users~1{id}/put` - PUT /users/{id}

Path separators are escaped per RFC 6901: `/` becomes `~1`, `~` becomes `~0`. The method is lowercase exactly as the artifact spells it — an uppercase method is refused, never case-folded (OAPI-D-03).

### Source expectations

- **`location`**: absolute URI addressing the OpenAPI JSON/YAML document (it also serves as the artifact's base URI for relative `$ref`s and relative server URLs).
- **`content`**: the inline OpenAPI document (content primacy; a co-present `location` is its base URI). Content with no location must be self-contained.

### Request media and raw bytes

The candidate defines declaration-led lanes without adding HTTP fields to the
operation contract. An OAS 3.0 non-JSON governing selection whose resolved
schema is `type: string, format: binary`, or an OAS 3.1 non-JSON governing
selection with no schema, exposes a canonical Base64 string at the JSON
operation boundary and emits the exact decoded octets. OAS 3.1 `type: string`
with `contentEncoding` instead sends the encoded application string unchanged.
Media ranges require a concrete `context.configuration.requestMedia`; exact,
`type/*`, and `*/*` declarations are matched in that order and a range is never
emitted as `Content-Type`. A required range-only body surfaces this as
preflight `CONTEXT_REQUIRED`, while coverage records
`configuration.requestMedia` without changing the application schema.

### Response media and raw bytes

The actual concrete response `Content-Type` selects the most
specific exact, `type/*`, or `*/*` declaration in the governing Response
Object. JSON remains strict application JSON, text and SSE remain application
strings, and OAS 3.0 binary schemas plus OAS 3.1 schema-omitted non-text media
emit canonical Base64 of the exact response bytes. Status, headers, and media
identity remain binding-native diagnostics rather than operation values.

### Dynamic object carriage

An object body that explicitly declares `additionalProperties` or
`patternProperties` remains one application object under the synthesized
protocol-neutral `payload` property. A binding-private `inputTransform` routes
that value whole, so arbitrary runtime member names never collide with path,
query, header, cookie, or other operation fields. Form and multipart members
resolve exact, pattern, additional-property, and `allOf` schemas before using
the OAS carriage rules; finite named object bodies retain their flat
application-property surface.

### Declaration-complex JSON carriage

An exact JSON-family body whose top-level schema uses combinators,
conditionals, dependent schemas, or explicit `unevaluatedProperties` remains
one complete application value under the synthesized protocol-neutral
`payload` property. The binding-private route carries that value whole: it
does not choose a schema branch or expose a media or HTTP wrapper. Non-JSON
candidates still require an independently defined faithful carriage lane.

### Server selection

The OAS effective server list comes from operation `servers`, else the path
item's, else the document's, else the implied `/`. A sole entry is selected
with server-variable defaults substituted; more than one requires the named
`server` configuration point because the artifact declares alternatives but
no preference. A relative server URL resolves against the source `location`.
Set `configuration.server` in the invocation context to select a declared
entry (`url` or `index`), supply `variables`, or supply a complete `baseUrl`
outright:

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

`NewInvokerWithClient(*http.Client)` supplies the client used for every request (corporate proxy, mTLS client certificate, custom CA pool, tracing transport). Do not set an overall `Timeout` on it; cancellation is driven by the invocation context. `NewInvoker()` uses a default client that returns artifact-observed redirect responses without following them, because following can rewrite the declared method or body.

For an artifact-authored security mechanism outside the built-ins, install a
handler by the scheme's authored name. This is adapter/runtime configuration;
it does not add protocol fields to the OBI or change the built-in schemes'
Core context resolution:

```go
invoker := openapi.NewInvokerWithOptions(openapi.RuntimeOptions{
    HTTPClient: client,
    SecurityHandlers: map[string]openapi.SecurityHandler{
        "digestAuth": func(req *http.Request, scheme openapi.SecurityHandlerContext) error {
            return applyDigest(req, credentialsFor(scheme.SchemeName))
        },
    },
})
```

Handlers run after the built-in request is finalized and may support Digest,
request signatures, mTLS-associated request policy, or future authored
mechanisms. Without a handler, the existing artifact-derived
`CONTEXT_REQUIRED` prerequisite remains authoritative and dispatch stays
fail-closed. Installing one declares that the handler is the complete,
configured implementation for that scheme; the handler therefore owns any
scheme-specific credential or transport resolution it requires.

### Credential application

Credentials are applied based on the OpenAPI spec's `securitySchemes`:

- `http` + `bearer`: `Authorization: Bearer <token>` from `bearerToken`
- `http` + `basic`: `Authorization: Basic <encoded>` from `basic.username`/`basic.password`
- `apiKey`: Placed in header, query, or cookie as the spec declares, from `apiKey`
- `oauth2` / `openIdConnect`: `Authorization: Bearer <token>` from the `accessToken` (or `bearerToken`) context field

Credentials are never volunteered when the effective operation declares no
security. Security Requirement Objects are OR alternatives and the schemes
within one object are an AND set; invocation selects exactly one complete,
channel-safe alternative and never combines credential fragments from
different alternatives.

When declared security is unsatisfied by the context, the invoker challenges `CONTEXT_REQUIRED` before any input is read or network touched, deriving the challenge from the artifact's `securitySchemes` (the negotiation surface is the [binding-invoker interface](https://openbindings.com/interfaces/binding-invoker); the challenge's `target` is the resolved base URL):

| Scheme | Requirement type | Carried fields |
| --- | --- | --- |
| `http` / `basic` | `auth.basic` | — |
| `http` / `bearer` | `auth.bearer` | — |
| `apiKey` | `auth.apiKey` | — |
| `oauth2` | `auth.oauth2` | `grantType`, `authorizeUrl`, `tokenUrl` from each usable flow; `scopes` required by the Security Requirement |
| `openIdConnect` | `auth.oauth2` | `openIdConnectUrl`; `scopes` required by the Security Requirement |

An `oauth2` scheme declaring several usable flows surfaces each as a separate
context alternative; it does not invent a flow preference. `grantType` names
each flow in its RFC 6749 spelling (`authorization_code`, `implicit`,
`password`, `client_credentials`). Every requirement carries the scheme's
declared name (its `securitySchemes` key), which disambiguates ANDed
requirements of one type and keys the scheme-scoped credential lookup: an
API-key scheme named `N` resolves `apiKeys[N]` first, falling back to the
single `apiKey` convenience.

A scheme outside this table is **surfaced, never dropped**: it emits a requirement typed from the artifact's own scheme (`http`/`digest` → `auth.http.digest`; any other type `T` → `auth.<T>`, e.g. `auth.mutualTLS`) that this package cannot itself apply. The alternative stays discoverable — unselectable only for runtimes without a resolver for that family — and a document whose every alternative is unmapped produces a readable challenge instead of an unauthenticated dispatch into a blind 401.

### Consumer hooks

HTTP leaves wire questions the OpenAPI document does not settle: which bytes-to-value rule to apply, whether a given response counts as success, and where the payload lives. This format **consults the consumer hooks seam** (`InvokeHooks`) for all three:

- **Decode** — the builtin rule is chosen from the response `Content-Type`; a `DecodeOutput` hook may override it.
- **Classify** — the builtin verdict is HTTP status (2xx success); a `Classify` hook may reclassify (a 200 envelope carrying an application error, say).
- **Route** — a hook may redirect where the payload is read from.

Each invocation records how each decision was made in its trailer metadata
(`x-ob-decode` / `x-ob-classify`), so a caller can see whether their hook
fired: a builtin decision stamps a provenance token naming what decided it
(`header/content-type` for decode, `assumption/2xx` for classify), and a hook
decision stamps `hook`. Binding families with unambiguous framing (gRPC,
Connect, GraphQL, MCP) do not consult the seam.

### Interface synthesis

Deterministic generation of OBI documents is a synthesis concern outside the binding specification (`openbindings.openapi@1` §10); these are this package's conventions, chosen so both reference SDKs emit an identical OBI for the same artifact:

- **Operation keys** come from `operationId` when present, sanitized to the OBI key grammar (non-key characters become `_`, leading/trailing `_` trimmed, a leading non-letter gets an `_` prefix). An `operationId` whose sanitized key is already taken falls through to path+method derivation: template segments (`{id}`) dropped, remaining segments joined with `.`, the lowercased method appended (`/users/{id}` + `GET` → `users.get`), then deduplicated deterministically with `_2`, `_3`, … suffixes.
- **Iteration order is fixed**: paths alphabetically, methods in the order get, put, post, delete, options, head, patch, trace.
- **Input schemas** merge effective path-level and operation-level parameters from every supported location (path, query, header, cookie) with each realizable request-media candidate's own body surface. Distinct finite declarations keep their application names when unique; collisions receive deterministic neutral suffixes and a binding-private `inputTransform` carries the exact protocol route. An explicitly dynamic object or declaration-complex exact JSON body is preserved as one full schema under a protocol-neutral `payload` property and privately routed whole, so runtime members cannot collide with independent parameters and no schema branch is selected by the binding. Distinct candidate surfaces are preserved with `anyOf`; parameter-only and non-JSON surfaces are closed against fields the invoker would refuse, while flattened JSON object candidates remain open for the binding's declared passthrough rule.
- **Output schemas** conservatively union every value-bearing success lane that can govern a 2xx response: exact 2xx entries, `2XX`, and an unshadowed `default`. Exact and ranged JSON declarations contribute their schemas, text/SSE declarations contribute strings, artifact-authorized raw-byte lanes contribute canonical Base64 strings, and a schema-less JSON lane leaves output unspecified rather than inventing a shape.
- **Schema projection** targets JSON Schema 2020-12 (spec OBI-D-06), keyed on the artifact's declared `openapi` version and operation direction. OpenAPI 3.0.x schemas are translated from their subset dialect and ignore Reference Object siblings; 3.1.x Schema Object `$ref` siblings compose under JSON Schema semantics before typed resolution, while legal non-schema Reference Object descriptions remain local to each reference site. A per-load synthesis sidecar preserves authored null, empty, zero, false, and `x-*` Schema Object values that the typed parser otherwise cannot distinguish from absence; typed OpenAPI objects remain authoritative for structure and invocation never consults the sidecar. Request contracts omit `readOnly` properties and response contracts omit `writeOnly` properties, with required lists repaired through nested and recursive graphs. An operation whose projected contract inherits a custom 3.1 schema dialect that cannot be losslessly projected to 2020-12 is excluded by tolerant synthesis and fails strict synthesis explicitly; schema-free operations and supported per-schema overrides remain available, and the dialect does not by itself prevent artifact-native invocation.
- **Unrealizable targets fail synthesis**: declaration-complex form, multipart, text, raw, and media-range schemas without one artifact-defined carriage; case-colliding HTTP header declarations; and required bodies with no supported media candidate make the whole strict synthesis call fail. Exact JSON-family declaration-complex bodies use whole-value carriage. An optional body may be omitted with a warning only when the remaining no-body operation is still faithfully invocable.
- **No security metadata is written to the OBI**; `securitySchemes` are honored at invocation time via context negotiation (`CONTEXT_REQUIRED` challenges and the `BindingPreparer` preflight).

## How it works

### Invocation flow

1. Loads and caches the OpenAPI document (JSON or YAML, local or remote), discriminating the exact accepted 3.0.0–3.0.4 and 3.1.0–3.1.2 editions
2. Parses the ref as a JSON Pointer (`#/paths/~1users/get` -> path `/users`, method `get`)
3. Resolves the server (effective list + variables + the `server` configuration point)
4. Routes application fields to their artifact declarations — using the binding-private routed representation when names collide — serializes parameters per OAS style/explode rules, and selects an artifact-declared request media candidate
5. Selects one complete, satisfiable Security Requirement alternative and applies only that alternative's credentials with the artifact-declared placement, refusing channel collisions pre-dispatch
6. Makes the HTTP request; the declared success media bound the interaction shape (unary, or server-streaming for a declared `text/event-stream` response), successful results emit through the invocation handle, and unsuccessful completion preserves the native response through `FailureEvidenceFrom`

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
