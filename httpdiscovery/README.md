# HTTP discovery

An optional Go module implementing the client and server contracts of
[OpenBindings HTTP Discovery v0.1.0](https://github.com/openbindings/spec/blob/1d5f08c2c2f2bf9822536ac5e6083edfd0831944/http-discovery.md).
This companion versions independently of core. The implementation applies the
text at that revision (SHA-256
`d64febfabe79b5c27f567158c9945f1029c9c3a44812639fa942c9d53d56e92e`).

The module is under development on `release/0.2`; it is not yet tagged. Its
module path is `github.com/openbindings/openbindings-go/httpdiscovery`. During
development it uses the core module beside it; see the repository's
[release instructions](../RELEASING.md).

## Discover a document

```go
client := httpdiscovery.Client{
    HTTPClient:       &http.Client{Timeout: 10 * time.Second},
    MaxDocumentBytes: 2 << 20,
}
result, err := client.Discover(ctx, "https://service.example")
if err != nil {
    // Handle absence, HTTP status, core refusal/violation, or transport error.
    return err
}
doc := result.Document
report := result.Report // use report.Conclusion for a conformance claim
```

`Endpoint(origin)` constructs `/.well-known/openbindings` on an absolute HTTP(S)
origin. An optional trailing slash is accepted; a resource path, credentials,
query, or fragment is rejected. No scheme is guessed. This convention covers
one interface per origin.

The zero client uses `http.DefaultClient` and a 1 MiB response-body limit. Zero
`MaxDocumentBytes` selects that default; a negative limit is an error. The bound
applies to what the HTTP client reads, including after automatic decompression.
An oversized document is an explicit limit error; its prefix is never validated.

The client sends `Accept: application/vnd.openbindings+json, application/json`.
It accepts both representations, including media-type parameters, and also
inspects bodies with other or missing content types. A metadata mismatch never
hides a refused core version. It checks the received bytes using core's
`ValidateDocument`, preserving the report and exact bytes without rewriting
references or source and binding content.

| Outcome | Result |
|---|---|
| HTTP 404 | `*StatusError`, also matching `ErrNotFound` through `errors.Is` |
| HTTP 401 or 403 | `*StatusError` with that status; gated discovery, never absence |
| Any other non-200 status | `*StatusError`; no interpretation of its body |
| Unsupported declared core version | Core `*VersionRefusalError`, accessible with `errors.As` |
| Established document-rule violation | Core `*ValidationError`, with the full report |
| Core cannot carry the document exactly | Error matching core `ErrInconclusive`, with the report |
| Decoded document with no established violation | Nil error and document; the report may still be conformance-undetermined |
| Body limit, cancellation, network or body-read failure | Explicit error; never absence or a document violation |

Once an HTTP response is available, `Result` accompanies the error as well as
success. It keeps the status, headers (including authentication challenges),
requested URL, and final URL. A complete bounded 200 body is retained even on
validation failure. A custom transport that omits `Response.Request.URL` leaves
the final URL empty. A failed HTTP call returns no result. Each call owns its
result, and the client keeps no cache.

For HTTP/1 connection reuse, short non-200 bodies are discarded with a 2 KiB
read cap and a 100 ms cleanup budget. Known larger bodies and HTTP/2 bodies are
closed directly. Cleanup reads do not interpret the body; read failures,
cancellation, and exhausted cleanup budgets preserve the observed status error
and response metadata. Custom transports must honor request cancellation during
body reads, as `net/http.Transport` does.

Redirects use the supplied HTTP client's policy, including `CheckRedirect` and
its limits. Authentication, cookies, TLS, proxy, scheme and network-range
restrictions belong to that client's configuration and transport; apply them
to the initial request and every redirect/connection. Plain HTTP is allowed.
The default HTTP client has no overall timeout: give requests a context deadline
or supply a client with a timeout. Cancellation interrupts HTTP work. Core
validation checks the completed document synchronously; it is checked for
cancellation before and after, but does not stop midway.

## Publish a document

```go
handler, err := httpdiscovery.NewHandler(documentJSON, httpdiscovery.HandlerOptions{
    AllowOrigin: "*", // public browser discovery (DISC-S-04)
})
if err != nil {
    return err
}
mux.Handle(httpdiscovery.WellKnownPath, handler)
```

The constructor copies the bytes, validates them once, and requires a conformant
core conclusion. A violation or version refusal retains core's error type;
undetermined conformance returns `ErrInconclusive`. This is the helper's
publication policy, not a claim that an undecided document is invalid. It serves
the exact snapshot concurrently; changes to the caller's buffer have no effect.

GET returns 200 and `application/vnd.openbindings+json`, regardless of Accept.
HEAD returns the same representation headers without a body. Other methods get
405 with `Allow: GET, HEAD`; other paths get 404. To replace the snapshot,
construct and install a new handler using the application's routing/lifecycle.

For no published document, leave the path unregistered in a `ServeMux` or use
`http.NotFoundHandler`. Authentication middleware may answer 401/403 without
revealing whether a document exists. An empty `AllowOrigin` emits no CORS header;
public browser discovery should explicitly use `"*"` or an allowed origin.
The constructor checks the syntax of a single ASCII origin
(`scheme://host[:port]`), rejecting controls, whitespace, origin lists,
credentials, paths (including a trailing slash), queries, and fragments.
The header value is emitted unchanged: supply the browser's serialized origin,
including its canonical host and port spelling. Internationalized domain names
use their ASCII form. Literal `"null"` is an explicit opt-in for the origin shared
by opaque origins; it does not identify one particular caller.
Credentialed CORS and preflight requests, when needed, use application middleware.
The helper does not add authentication, credentialed CORS, or cache policy.

## Conformance evidence and scope

The external-package tests exercise the public API against HTTP and HTTPS test
servers. The rule coverage is:

| Companion rule | Tests / implementation choice |
|---|---|
| DISC-C-01 | Both media types, parameters, exact request Accept header |
| DISC-C-02 | All five redirects, changed HTTPS origin, requested/final URLs, application refusal, stopped redirects and redirect limit |
| DISC-C-03 | 404 alone is absence; gated and other statuses remain explicit; core violations and inconclusive reports stay distinct |
| DISC-S-01 | Exact immutable conformant snapshot, absent/unrelated Accept, refused invalid/undetermined publication |
| DISC-S-02 | OBI response media type |
| DISC-S-03 | Deployment tests with an absent route and authentication middleware, then authenticated retrieval |
| DISC-S-04 | Explicit public, restricted-origin, and no-CORS configurations |

A document declaring a version core does not apply returns core's
`*VersionRefusalError`, never `ErrNotFound`: the document exists, and the
refusal is the one diagnostic a user can act on. This is the client's choice,
not a companion rule; tests cover older, newer, and prerelease versions,
including unfamiliar document members and media types.

Additional tests cover context cancellation, response closure, configurable and
decompressed size bounds, invalid CORS configuration, bounded error-body cleanup
and HTTP/1 connection reuse, routing, HEAD, concurrency, result
isolation, and preservation of document references and dependencies. CI also
verifies the companion's pinned text hash using its existing spec checkout.

```sh
GOWORK=off go test -race ./...
```

Set `OB_SPEC_CORPUS` to the checked-out spec's `conformance` directory to verify
the authority hash locally. `OB_CORPUS_REQUIRED=1` makes a missing authority fail,
as in CI. The client/server tests themselves always run.

Discovery supplies neither document identity nor a reference base. It fetches no
sources, schema resources, or dependency implementations, and does no invocation,
synthesis, binding selection, or composition. It supports the core versions the
required core SDK supports. TypeScript alignment for this rebuilt module is
pending in [IMPLEMENTATION_PARITY.md](../IMPLEMENTATION_PARITY.md).
