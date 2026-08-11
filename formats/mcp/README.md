# `formats/mcp`

MCP binding invoker and interface synthesizer for the OpenBindings Go SDK. It
implements the unreleased first `openbindings.mcp@1` candidate over MCP
2025-11-25 Streamable HTTP. No MCP binding specification has been published,
and there is no older compatibility meaning for `@1`.

## Install and register

```sh
go get github.com/openbindings/openbindings-go/formats/mcp
```

```go
opInv := openbindings.NewOperationInvoker(mcpbinding.NewInvoker())
```

The candidate binds only `tools/<name>` targets whose listing declares an
`outputSchema`. That schema is the application output contract; successful
invocation emits conforming `structuredContent` alone.

```go
invoker := mcpbinding.NewInvoker()
defer invoker.Close()

call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
    Source: openbindings.InvocationSource{
        BindingSpec: "openbindings.mcp@1",
        Location:    "https://mcp.example.com/mcp",
    },
    Ref:     "tools/get_weather",
    Context: map[string]any{"bearerToken": "tok_123"},
})

_ = call.Write(ctx, map[string]any{"city": "Seattle"})
out, err := openbindings.Single(ctx, call.Outputs())
```

MCP `content`, `_meta`, `isError`, progress notifications, JSON-RPC fields,
HTTP fields, and session facts never become ordinary operation values. A tool
error, transport or JSON-RPC failure, or missing/nonconforming
`structuredContent` completes unsuccessfully. Native evidence may be retained
only through `Diagnostics()`.

Tools without `outputSchema`, required-task tools, resources, resource
templates, and prompts are coverage exclusions. Their MCP-native result
shapes are not recompiled into application schemas merely to make them
bindable.

## Synthesis

```go
iface, err := mcpbinding.NewSynthesizer().SynthesizeInterface(ctx,
    &openbindings.SynthesizeInput{
        Sources: []openbindings.SynthesizeSource{{
            BindingSpec: "openbindings.mcp@1",
            Location:    "https://mcp.example.com/mcp",
        }},
    })
```

Live synthesis negotiates MCP 2025-11-25 and exhausts all advertised listing
pages. Present `content` may instead pin the complete listing. Each eligible
tool becomes one operation:

| OBI field | MCP source |
| --- | --- |
| `binding.ref` | `tools/<name>` |
| operation input | tool `inputSchema` |
| operation output | tool `outputSchema` |
| successful output value | `CallToolResult.structuredContent` |

`location` is a required absolute HTTP(S) endpoint. Present `content` is an
authoritative pinned listing but does not replace the invocation endpoint.
`bearerToken` maps to `Authorization: Bearer`; explicitly named headers and
cookies use their named carriers. Generic API-key or basic credentials have no
MCP-declared carrier and become context requirements.

Use `WithHTTPClient` and `WithSynthesizerHTTPClient` for proxies, mTLS, custom
CA pools, or other transport policy. The invoker pools sessions by endpoint
and authentication headers and closes them through `Invoker.Close`.

Response reading is delegated to the official MCP Go SDK, which does not
currently expose a delivery-unit read-bound seam. `MaxDeliveryUnitBytes`
therefore has a named implementation exclusion on this lane.

The candidate behavior is defined in the
[`openbindings.mcp@1` document](https://github.com/openbindings/spec/blob/main/binding-specs/mcp/openbindings.mcp.md).

## License

Apache-2.0
