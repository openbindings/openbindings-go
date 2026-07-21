# formats/asyncapi

AsyncAPI 3.x binding invoker and interface synthesizer for the [OpenBindings](https://openbindings.com) Go SDK.

This package enables OpenBindings to invoke operations against AsyncAPI documents and synthesize OBI documents from them. It supports HTTP/SSE for event streaming, HTTP POST for sending messages, and WebSocket for bidirectional communication. Credentials are applied via the document's security schemes.

See the [spec](https://github.com/openbindings/spec) and the [invocation pattern](https://openbindings.com/spec/invocation-pattern) for how binding invokers and interface synthesizers fit into the OpenBindings architecture.

## Install

```
go get github.com/openbindings/openbindings-go/formats/asyncapi
```

Requires [openbindings-go](https://github.com/openbindings/openbindings-go) (the core SDK).

## Usage

### Register with OperationInvoker

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    asyncapi "github.com/openbindings/openbindings-go/formats/asyncapi"
)

opInv := openbindings.NewOperationInvoker(asyncapi.NewInvoker())
```

The invoker declares the binding-spec identifier `openbindings.asyncapi@1` — exact and opaque, never a version range. The operation invoker routes a source to this invoker by string equality on the source's `bindingSpec`.

### Invoke a binding

Direct use follows the complementary perspective (ASYNC-P-02): the AsyncAPI document describes the application, and the invocation is the counterparty — invoking a `receive` operation publishes to the application; invoking a `send` operation subscribes to what it sends.

```go
invoker := asyncapi.NewInvoker()
defer invoker.Close()

source := openbindings.InvocationSource{
    BindingSpec: "openbindings.asyncapi@1",
    Location:    "https://api.example.com/asyncapi.json",
}

// Publish: `receiveOrder` has action `receive` — the described application
// receives, so invoking it publishes. Over http/https this is a unary POST.
call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source:  source,
    Ref:     "#/operations/receiveOrder",
    Context: map[string]any{"bearerToken": "tok_123"},
})

// The one input is the message payload.
if err := call.Write(ctx, map[string]any{"item": "tea"}); err != nil {
    log.Fatal(err)
}

// The operation declares a reply, so the decoded response body is the single
// output. An empty-body acknowledgment (202/204) yields zero outputs instead.
out, err := openbindings.Single(ctx, call.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out)
```

To subscribe, invoke a `send` operation and read the output sequence to EOF:

```go
// Subscribe: `sendOrderUpdates` has action `send` — the described application
// sends, so invoking it subscribes (SSE over http/https, a streaming socket
// over ws/wss).
call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source:  source,
    Ref:     "#/operations/sendOrderUpdates",
    Context: map[string]any{"bearerToken": "tok_123"},
})
outs := call.Outputs()
for {
    ev, err := outs.Read(ctx)
    if err == io.EOF {
        break // clean close
    }
    if err != nil {
        log.Fatal(err) // terminal *openbindings.InvocationError
    }
    fmt.Println(ev)
}
```

### Synthesize an interface from an AsyncAPI document

```go
synth := asyncapi.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        BindingSpec: "openbindings.asyncapi@1",
        Location:    "https://api.example.com/asyncapi.json",
    }},
})
```

## Conventions

These behaviors are pinned by the binding specification (`binding-specs/asyncapi/openbindings.asyncapi.md` in the [spec repo](https://github.com/openbindings/spec)); this section summarizes them.

### Binding-spec identifier

`openbindings.asyncapi@1` — exact and opaque, matched by string equality; never interpreted as a version range. Accepted artifacts are the AsyncAPI **3.0.x** line only, discriminated by the document's own `asyncapi` field (ASYNC-P-01); a later 3.x line is adopted by a compatible revision of the binding specification, never sight-unseen.

### Ref format

JSON Pointer to the operation within the AsyncAPI document, and the only conformant spelling (a bare operation key is refused): `#/operations/<operationId>`

- `#/operations/receiveOrder`
- `#/operations/sendOrderUpdates`

Operation keys containing `/` or `~` carry RFC 6901 escaping in the pointer (`~1`, `~0`) (ASYNC-D-03).

### Source expectations

- **`location`**: Absolute URI addressing the AsyncAPI JSON/YAML document itself (`https://`, `http://`, or `file://`). A bare filesystem path is a relative reference in form and is refused at the invoke lane (ASYNC-D-02); the synthesis entrypoint absolutizes a bare path to its `file://` spelling as an authoring convenience.
- **`content`**: Inline AsyncAPI document.

### Context negotiation

The doc's security declarations are conjunctive (ASYNC-P-07): the targeted server's `security` applies, and the operation's `security`, when declared, applies in addition; within each declared list, satisfying any one entry suffices. Declarations are mapped to requirement families (`auth.bearer`, `auth.basic`, `auth.apiKey`, `auth.oauth2`); a scheme family this SDK cannot itself resolve is surfaced with a derived type (`auth.http.<scheme>`, `auth.<type>`) rather than dropped. When the declared requirements aren't satisfied by the provided context, the invocation terminates with `CONTEXT_REQUIRED` **before any connection is opened**; the challenge's `target` is the resolved server URL. The invoker also implements `PrepareBinding` (side-effect-free preflight) using inline source content or the warm doc cache — it never fetches.

### Credential application

Credentials are applied based on the AsyncAPI document's `securitySchemes`:

- `http` + `bearer` / `httpBearer`: `Authorization: Bearer <token>` from `bearerToken`
- `http` + `basic` / `userPassword`: `Authorization: Basic <encoded>` from `basic`
- `apiKey` / `httpApiKey`: Placed in header, query, or cookie as declared, from `apiKeys[<scheme name>]`, falling back to the single `apiKey`
- `oauth2`: `Authorization: Bearer <token>` from `bearerToken` or `accessToken`

WebSocket credentials ride the upgrade request: headers, plus spec-declared query-param apiKeys appended to the dialed URL. No credential ever rides a message body or a first frame — in-band auth is excluded by the binding specification (§9.5, ASYNC-P-07).

Falls back to bearer, then basic, then apiKey (as `Authorization: ApiKey <key>`) when no security schemes are defined.

### Consumer hooks

Each message's bytes-to-value rule is chosen from the governing declared content type: per message, its own `contentType`, else the document's `defaultContentType`; the governing set is the operation's `messages` (else its channel's), and the reply-side declarations govern a publish's response (direction-correct decode, ASYNC-P-05). Exactly one distinct effective type selects the lane — strict JSON for `application/json` and `+json` (a declared-JSON payload that fails to parse is a loud error), text otherwise; an ambiguous set falls to the text lane, never a guess. This format **consults the consumer hooks seam** (`InvokeHooks`): a `DecodeOutput` hook may override the builtin rule for a message. The unary publish lane stamps decode provenance in its trailer metadata (`x-ob-decode`: `spec/content-type`, or `hook` when overridden), alongside the fixed `x-ob-classify: not-consulted` — message-oriented transports have no per-message success status the way HTTP does, so this format runs no result classifier.

### Interface synthesis

- Operations iterated alphabetically for deterministic output
- Schema direction follows the complementary perspective: input schemas from `receive` operation payloads, output schemas from `send` operation payloads and `receive` operations' declared replies
- Refs generated as `#/operations/<id>`

### Protocol dispatch

The transport comes from the resolved server's protocol; the cell comes from the operation's action under the complementary perspective (ASYNC-P-02) — the artifact describes the application, so invoking `receive` publishes and invoking `send` subscribes:

| Protocol | Invoking `receive` (publish) | Invoking `send` (subscribe) |
|----------|------------------------------|-----------------------------|
| HTTP/HTTPS | POST (unary) | SSE streaming |
| WS/WSS | WebSocket publish (client-streaming) | WebSocket subscribe (bidi-capable) |

## How it works

### Invocation flow

1. Loads and caches the AsyncAPI document (JSON or YAML, local or remote)
2. Parses the ref to extract the operation ID (`#/operations/receiveOrder` -> `receiveOrder`)
3. Resolves the server URL and protocol from the document (consumer configuration may select a server by name or supply a connection URL; an unresolvable server, address, or `{name}` expression is a pre-dispatch refusal, never a guess)
4. Challenges `CONTEXT_REQUIRED` when declared security isn't satisfied (before any connection is opened)
5. Dispatches based on action and protocol:
   - **receive + http/https**: unary POST publish — the one input is the message body; an empty-body response (202/204 acknowledgments included) yields zero outputs, otherwise the decoded response body is the single output
   - **receive + ws/wss**: client-streaming publish — every input is one socket frame; closing input after at least one message completes the call with zero outputs (closing with none sent is a refusal)
   - **send + http/https**: SSE subscribe — each server event is one output; transport close completes the subscription (reconnection is excluded from revision 1)
   - **send + ws/wss**: WebSocket subscribe (bidi-capable) — socket frames become outputs; caller inputs are forwarded as ordinary frames (closing input does not end the subscription)

### WebSocket connection pooling

Operations on the same channel (same server + address + credential identity) share one pooled WebSocket connection — load-bearing for the AsyncAPI two-operation pattern where a subscription (`send` operation) and publishes (`receive` operations) need to land on the same socket so server-side per-connection state is shared. The credential identity is a digest of the upgrade request's auth material, so a pooled connection is never shared across differing credentials. Connections are reference-counted and evicted after a 30s idle timeout; `Invoker.Close()` closes the pool.

### Interface synthesis

Converts an AsyncAPI 3.x document into an OBI by:
- Iterating operations sorted alphabetically for deterministic output
- Following the complementary perspective for schema direction: a `receive` operation's payload becomes the OBI operation's input (invoking it publishes) and its declared reply becomes the output; a `send` operation's payload becomes the output (invoking it subscribes)
- Generating `#/operations/<id>` refs for each binding
- Deriving operation keys from operation IDs

## Endpoint resolution

`ParseDocument` and `Document.ResolveEndpoint` export the server and address
configuration points of `openbindings.asyncapi@1` §9.2 (ASYNC-P-04) for
consumers that dial an AsyncAPI-described endpoint with their own transport:
parse the document bytes (no I/O — fetching is the caller's business),
resolve one operation's connection URL and deciding protocol per the
specification's pinned rules — effective-set ordering, server-variable
substitution, address-parameter expansion, the concatenation URL-assembly
rule, every unresolvable input a refusal, never a guess. The resolution is
the same code path the invoker runs before dispatch, so an exported
resolution and an invocation can never disagree. Not carried: declared
protocol `bindings` governing the upgrade request, and credential
application — those are the invoker's business at dispatch.

**Go-only — a named gap, not an oversight.** The TS
`@openbindings/asyncapi` package keeps its document model and target
resolution internal: this seam's sole consumer is the ob CLI's delegate
frame lane (resolving a delegate's advertised `invokeBinding` endpoint from
its AsyncAPI document), and ob is Go. A TS consumer with the same need
delegates to a running `ob start` instead, per the delegate model. If a
second, non-ob consumer appears on the TS side, port the seam there rather
than re-deriving server selection outside the format package.

## Resource bounds

Delivery units — the unary publish reply body, one SSE event, and one
WebSocket message (the socket's per-message read limit) — are
consumer-bounded: set `MaxDeliveryUnitBytes` on the `OperationInvoker` (or
per invocation on `BindingInvocationArgs`); zero selects
`openbindings.DefaultMaxDeliveryUnitBytes` (10 MiB). On a pooled WebSocket
the read limit only ever rises: a later acquirer with a larger bound raises
it, a smaller one never lowers it mid-life. Deliberately fixed: the HTTP
error-body diagnostics capture, the SSE line-scanner guard, and the
synthesis-lane artifact-fetch guard — none is a delivery unit.

## License

Apache-2.0
