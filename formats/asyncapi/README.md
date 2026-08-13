# `formats/asyncapi`

Thin OpenBindings adapter and interface synthesizer over the standalone
[`asyncapi-client/go`](https://github.com/openbindings/asyncapi-client/tree/main/go)
artifact runtime.

The package implements the unreleased first `openbindings.asyncapi@1`
candidate. No AsyncAPI binding specification has been published, and there is
no older compatibility meaning for `@1`.

The adapter owns OBI source/ref validation, invocation-handle bridging,
protocol-independent unsuccessful completion, synthesis, and coverage. The
standalone runtime owns AsyncAPI loading, normalization, target and message
resolution, security interpretation, and execution. Protocol drivers own the
concrete transport and nested AsyncAPI protocol-binding behavior.

Under `openbindings.asyncapi@1`, AsyncAPI Core and the artifact's nested
protocol binding are deliberately incorporated as authorities. Synthesis is
not restricted to an allowlist of installed drivers.

## Install and register

```sh
go get github.com/openbindings/openbindings-go/formats/asyncapi
```

```go
opInv := openbindings.NewOperationInvoker(asyncapi.NewInvoker())
```

The candidate accepts exact AsyncAPI editions 2.0.0–2.6.0, 3.0.0, and 3.1.0.
It normalizes authored operations without rewriting the source artifact:

- v2 `publish` is caller-input/publish and uses
  `#/channels/<escaped-channel>/publish`;
- v2 `subscribe` is caller-output/subscribe and uses
  `#/channels/<escaped-channel>/subscribe`;
- v3 `receive` is caller-input/publish;
- v3 `send` is caller-output/subscribe;
- v3 refs use `#/operations/<escaped-operation-key>`.

This complementary perspective follows AsyncAPI's description of the
application: the invocation acts as its counterparty.

## Invoke

```go
invoker := asyncapi.NewInvoker()
defer invoker.Close()

call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        BindingSpec: "openbindings.asyncapi@1",
        Location:    "https://api.example.com/asyncapi.yaml",
    },
    Ref:     "#/operations/receiveOrder",
    Context: map[string]any{"bearerToken": "tok_123"},
})

_ = call.Write(ctx, map[string]any{"item": "tea"})
_ = call.CloseInput()
```

The invocation handle remains cardinality-neutral. Ordering, half-close,
cancellation, partial output, and completion behavior emerge from the selected
operation and protocol driver; they are not written into the OBI document.

Ordinary inputs and outputs are message payload application values. AsyncAPI
message envelopes, protocol-binding objects, headers, status, framing, and
transport facts stay below the operation boundary. Artifact runtimes, native
clients, logs, and traces may retain native evidence below this boundary.

Message headers are an explicit abstraction-boundary exclusion. JSON-family
and UTF-8 text payloads have built-in value carriage; binary and codec-specific
payloads require a faithful mapping and are otherwise excluded.

## Protocol drivers

The standalone engine contains the current HTTP and WebSocket drivers. A host
can construct an artifact engine with additional `ProtocolDriver`
implementations and inject it into the OpenBindings invoker. A missing driver
is a local pre-dispatch capability error. Driver availability does not change
synthesis or the resulting OBI.

The WebSocket driver preserves AsyncAPI reply direction as a full-duplex
session for both `receive` and `send`, including static reply channels on a
second endpoint of the same server. The adapter forwards only application
values and lifecycle; it adds no WebSocket fields to Core frames. Dynamic
reply addresses and cross-protocol/server reply routes remain explicit
standalone-runtime exclusions.

## Synthesis and coverage

```go
result, err := asyncapi.NewSynthesizer().SynthesizeInterfaceWithCoverage(ctx,
    &openbindings.SynthesizeInput{
        Sources: []openbindings.SynthesizeSource{{
            BindingSpec: "openbindings.asyncapi@1",
            Location:    "https://api.example.com/asyncapi.yaml",
        }},
    })
```

Synthesis is deterministic and protocol-independent. It preserves the source
artifact and exact native ref, derives schemas only from authored payload
contracts, and reports every target as represented, excluded, lossy, or
failed. It does not write protocol names, channel addresses, headers, methods,
status codes, or driver requirements into operation schemas.

Present `content` is authoritative. The authoring helper can read local files;
the invocation lane treats a conformant `location` as an absolute artifact
URI.

The Go and TypeScript adapters produce exactly equal OBI and exhaustive
coverage results for all 247 independently adjudicated valid artifacts in a
250-repository internal qualification corpus of independently sourced GitHub
artifacts. The corpus is not redistributed; its sealed 63-repository holdout
is committed by SHA-256
`ad9a78e13914a343f1e7dc37a7de22b749e0fbbbceba1995b17a83c59663a3da`, and the
distilled qualification report is published with the OpenBindings
specification. The sole raw parser mismatch is invalid external YAML and
remains outside the supported-artifact denominator.

## License

Apache-2.0
