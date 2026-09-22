# Generic JSON value carriage

## JSON boundaries and explicit export

`jsonvalue` is the SDK's maintained codec/equality area. Invocation snapshots and
typed construction live in private implementation packages; applications keep
ordinary Go values. See [invocation values](../INVOCATION_VALUES.md).

`MarshalWithOptions` adds finite value/depth/codec-scratch allowances to explicit
export. `Marshal` and `Unmarshal` keep their existing general-purpose APIs. The
maintained codec derives from Go's JSON codec; its provenance and local changes
are recorded in `internal/thirdparty/jsoncodec/MAINTENANCE.md`.

The maintained string representation uses canonical UTF-8 for scalar values and
WTF-8 for isolated UTF-16 units. Its encoder emits isolated units as JSON escapes.
Other malformed native bytes retain replacement behavior. This does not qualify
every SDK-owned document/wire wrapper or external protocol codec for exact string
carriage; their contracts and tests remain authoritative. Standard Go JSON
encoding can replace isolated units, so use the maintained codec when those
values matter. No Core or binding specification changes are implied.

## Existing numerical carriage contract

This SDK implementation retains `encoding/json.Number` at generic JSON decode,
clone, invocation and context boundaries. Use `jsonvalue.Unmarshal(data, &value)`
when reading JSON into `any`; encode the result with `jsonvalue.Marshal`.
The adapter uses the maintained codec rather than a separate invocation parser.

```go
var value any
err := jsonvalue.Unmarshal([]byte(`{"id":9007199254740993}`), &value)
// After checking err:
id := value.(map[string]any)["id"].(json.Number)
// string(id) is "9007199254740993"; Marshal emits a JSON number, not a string.
```

Generic consumers must handle `json.Number` instead of asserting `float64`.
Choose `Int64`, `Float64`, or a decimal library explicitly when computation is
needed; those conversions retain their own documented range/rounding behavior.
Typed destinations continue to select their declared Go representations.
Already-rounded caller-supplied floating-point values cannot be reconstructed.

`Unmarshal` requires exactly one JSON value; `IsNumber` rejects malformed or
empty numeric tokens. Duplicate-member, encoding, BOM and surrogate acceptance
remain the responsibility of document/protocol boundaries. Canonical hashing
and lexically constrained parameter serialization retain their separate
contracts. This package does not promise exact JSONata arithmetic, change Core
or binding requirements, or qualify other protocol adapters automatically.

`Equal` and `CompareNumbers` use exact mathematical numeric values, not token
spelling, with Go's standard `math/big.Rat`. Numeric predicates currently accept
tokens up to 4,096 characters and explicit decimal exponents from -10,000 to
10,000 inclusive. These operation-specific work limits are checked before
expansion. `CapabilityError` means no verdict was established; it is not
inequality or an instance-validation failure. JSON parsing and carriage do not
expand exponents and are not subject to these arithmetic limits.

The continuation qualification tests exercise both sides of each limit,
1,000 repeated comparisons, and unchanged carriage beyond the predicate range.
The limits are official implementation policy, not a universal specification
floor, and do not imply a JSONata arithmetic guarantee.

`NewValueSet` provides owned, first-insertion-ordered exact membership. Its
private structural index only narrows comparisons; exact equality verifies
hits. `Values` returns detached copies. Numeric indexing has the same predicate
budget. Its keys are not exported, persistent identities or canonical JSON.
