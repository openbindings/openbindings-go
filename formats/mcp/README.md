# formats/mcp

MCP (Model Context Protocol) binding invoker and interface synthesizer for the [OpenBindings](https://openbindings.com) Go SDK.

The package implements `openbindings.mcp@2` and retains `openbindings.mcp@1` compatibility for existing OBIs. Both revisions use MCP 2025-11-25 over Streamable HTTP.

## Install

```sh
go get github.com/openbindings/openbindings-go/formats/mcp
```

## Register

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    mcpbinding "github.com/openbindings/openbindings-go/formats/mcp"
)

opInv := openbindings.NewOperationInvoker(mcpbinding.NewInvoker())
```

The invoker and synthesizer advertise revision 2 first and revision 1 as a compatibility revision.

## Revision 2: protocol-blind tools

`openbindings.mcp@2` binds only `tools/<name>` targets whose MCP listing declares an `outputSchema`. That schema is the application's output contract; successful invocation emits `structuredContent` alone after checking it against the schema.

```go
invoker := mcpbinding.NewInvoker()
defer invoker.Close()

call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        BindingSpec: "openbindings.mcp@2",
        Location:    "https://mcp.example.com/mcp",
    },
    Ref:     "tools/get_weather",
    Context: map[string]any{"bearerToken": "tok_123"},
})

if err := call.Write(ctx, map[string]any{"city": "Seattle"}); err != nil {
    log.Fatal(err)
}

out, err := openbindings.Single(ctx, call.Outputs())
if err != nil {
    log.Fatal(err)
}
fmt.Println(out) // the tool's structuredContent, not an MCP result envelope
```

Revision 2 deliberately does not turn MCP `content`, `_meta`, `isError`, progress notifications, JSON-RPC fields, HTTP fields, or session facts into ordinary operation values. It also does not solicit progress. A tool error, transport or JSON-RPC failure, or missing/nonconforming `structuredContent` completes the invocation unsuccessfully. Native evidence may be retained only through the invocation's explicit `Diagnostics()` view.

Tools without `outputSchema`, required-task tools, resources, resource templates, and prompts are reported as coverage exclusions during synthesis. Their MCP-native result shapes are not recompiled into application schemas merely to make them bindable.

## Synthesize revision 2

```go
synth := mcpbinding.NewSynthesizer()
iface, err := synth.SynthesizeInterface(ctx, &openbindings.SynthesizeInput{
    Sources: []openbindings.SynthesizeSource{{
        BindingSpec: "openbindings.mcp@2",
        Location:    "https://mcp.example.com/mcp",
    }},
})
```

Live synthesis negotiates MCP 2025-11-25 and exhausts all advertised listing pages. Present `content` may instead pin the complete listing. In revision 2, each eligible tool becomes one operation:

| OBI field | MCP source |
|---|---|
| `binding.ref` | `tools/<name>` |
| operation input | tool `inputSchema` |
| operation output | tool `outputSchema` |
| successful output value | `CallToolResult.structuredContent` |

Operation naming is synthesizer policy rather than binding semantics.

## Revision 1 compatibility

`openbindings.mcp@1` remains available so previously synthesized OBIs keep their published meaning. Revision 1 supports tools, resources, resource templates, and prompts; its output mapping retains the legacy MCP-shaped result behavior, and its optional `solicit` configuration can surface progress values. Use revision 1 only when consuming an existing revision-1 OBI or when those native MCP families and shapes are intentionally required.

The two revisions are not silently interchangeable. A source requesting revision 1 is processed as revision 1, and a source requesting revision 2 receives the narrower protocol-blind contract.

## Source, refs, and credentials

- `location` is a required absolute HTTP(S) Streamable HTTP endpoint.
- Revision-2 refs are byte-exact `tools/<name>` references resolved against the pagination-exhausted listing before dispatch.
- Present `content` is an authoritative pinned listing; it avoids live list calls but does not replace the endpoint used for invocation.
- `bearerToken` maps to `Authorization: Bearer`.
- Explicitly named headers and cookies use their named HTTP carriers.
- Generic `apiKey` or `basic` values have no MCP-declared carrier and surface as context requirements rather than receiving an invented destination.

## Custom HTTP client

The transport installs an internal header injector for per-call diagnostics. Supply a base client when discovery or invocation needs a proxy, mTLS, a custom CA pool, or another transport policy:

```go
invoker := mcpbinding.NewInvoker(mcpbinding.WithHTTPClient(myClient))
synth := mcpbinding.NewSynthesizer(
    mcpbinding.WithSynthesizerHTTPClient(myClient),
)
```

## Session and size behavior

The invoker pools MCP sessions by endpoint and authentication headers, idles unused sessions for 30 seconds by default, and closes them through `Invoker.Close`.

Response reading is delegated to the official MCP Go SDK, which currently exposes no delivery-unit read-bound seam. `MaxDeliveryUnitBytes` therefore does not bound this lane. This is a named implementation exclusion, not a different MCP binding meaning.

## Specification

The normative behavior lives in [`spec/binding-specs/mcp/openbindings.mcp.md`](https://github.com/openbindings/spec/blob/main/binding-specs/mcp/openbindings.mcp.md). Published revisions are immutable.

## License

Apache-2.0
