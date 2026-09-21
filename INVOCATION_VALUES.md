# Invocation values

Invocation calls accept ordinary Go values. The SDK snapshots accepted inputs
and outputs, validates their logical JSON meaning, and constructs detached Go
results. Ordinary typed structs, maps, slices and byte leaves do not need a
JSON encode/decode round trip to cross an SDK boundary.

## Ownership

After `Write` or `EmitOutput` returns, the producer can reuse its maps, slices,
structs and byte buffers. Do not mutate them concurrently with that call. Local
handlers, transform callbacks and public readers receive their own mutable
values. A callback may retain its input; mutations must not race the callback's
return or the SDK's capture of its returned result. Duplicate mutable values in
one result are detached from each other. Results remain valid after the
invocation completes or is cancelled. Go garbage collection owns their lifetime.

This changes the old generic local fast path: mutable reference identity is no
longer preserved. There is no borrowing option or result-release API.

```go
input := map[string]any{"label": "before"}
if err := call.Write(ctx, input); err != nil {
    return err
}
input["label"] = "reused" // the accepted input still says "before"
```

## Bytes, numbers and transforms

A byte slice has Base64 string meaning in the logical JSON view. A typed result
with a `[]byte` field recovers its exact contents, including after a transform
moves that string to another field. Buffer identity is not part of this
contract. Nil byte slices represent null; non-nil empty slices represent the
empty string, subject to the binding's existing empty-body correspondence.

```go
type Picture struct {
    Nested struct {
        PhotoData []byte `json:"photoData"`
    } `json:"nested"`
}
call := invoke.Invoke(ctx, invoker, iface,
    invoke.NewOperationSignature[any, Picture]("getPicture"))
if err := call.Close(); err != nil {
    return err
}
picture, err := invoke.Single(ctx, call.Outputs())
// Check err, then use picture.Nested.PhotoData as the original image bytes.
```

Raw and typed-`any` readers expose ordinary maps, slices, strings, booleans,
null and numbers. No SDK-private byte carrier escapes. Generic decoded numbers
and projected integers retain `json.Number`; caller-supplied `float64` values
retain their already-chosen precision. Generic code must accept both. Typed
construction checks numeric range and shape and honors the maintained codec's
field selection and custom JSON/text methods. Unsupported ordinary values,
cycles, nonfinite numbers and malformed numeric carriers are rejected.

The application installs `TransformEvaluator`. The core invocation runtime
selects no evaluator. Evaluators receive detached logical values and must retain
the distinctions between a value, null, undefined and failure. An adapter may
require JSON text itself; that is its integration cost. The optional existing
`invoke/jsonata` adapter still uses its selected runtime's text API. Changing
that application choice is independent of this migration.

## Limits and failures

`ValueLimits` applies at runtime/invoker/local-provider configuration and via
`invoke.WithValueLimits` per operation call. Direct binding invocations carry
limits in `BindingInvocationArgs.ValueLimits`; low-level sessions use
`WithInvocationValueLimits`. Zero fields inherit the configured defaults:

| Limit | Default |
| --- | ---: |
| One value / construction / codec scratch allowance | 64 Mi units |
| Live SDK value retention within one invocation | 256 Mi units |
| Logical nesting depth | 256 |

Units are deterministic accounting allowances: 64 per logical node plus escaped
string/key and number token lengths, with conservative reservations for SDK
containers, construction and codec buffers. They are not a byte-exact heap quota.
Custom callbacks, evaluator engines, transport buffers, prepared documents and
application-retained results have their own resource policies. Repeated aliases
are charged per occurrence; each internal retained owner carries a full root
charge even when immutable backing storage is shared.

```go
call := invoke.Invoke(ctx, invoker, iface, signature,
    invoke.WithValueLimits(invoke.ValueLimits{
        MaxValueUnits: 128 << 20,
        MaxLiveUnits: 512 << 20,
        MaxDepth: 256,
    }))
```

Retries and SDK Operation Graph descendants share the live budget. Internal
retention refuses exhaustion that depends on its own progress to release space;
independently readable public output can provide backpressure. Exhaustion ends
the invocation with `ERR_RUNTIME`. Invalid input capture returns
`ERR_TYPE_MISMATCH`; a rejected input is not accepted into the stream. Outputs
already accepted still drain before the terminal error. `Cancel` preserves that
accepted prefix; `OutputStream.Stop` explicitly discards unread outputs and
releases the SDK's retained terminal data.

A failed typed output conversion consumes that one logical output, returns a
zero value and `*invoke.ValueConversionError`, and leaves subsequent outputs and
the terminal outcome readable. `errors.As` can inspect its wrapped invocation
error and, for resource refusal, `*invoke.ValueLimitError`. Local resource causes
are not portable error data. Repeated terminal reads return detached error data,
preserving absent data versus explicitly present null.

## Explicit JSON export

JSON is produced on request or at a protocol boundary. It is not required as
internal invocation carriage.

```go
display, err := jsonvalue.MarshalWithOptions(picture, jsonvalue.MarshalOptions{
    MaxValueUnits: 64 << 20,
    MaxDepth: 256,
})
```

`jsonvalue.Marshal` remains available with its existing behavior. Standard
`encoding/json.Marshal` works for ordinary compatible Go values; use `jsonvalue`
when the SDK's exact supported string/number representation matters. Neither
export API promises that arbitrary JSON text retains native host type identity.

See [qualification](VALUE_MIGRATION_QUALIFICATION.md) for measured tradeoffs and
release gates, and the [language-neutral contract](INVOCATION_DATA_FLOW.md) for
future implementations.
