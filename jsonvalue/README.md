# Generic JSON value carriage

## Isolated run-3 string experiment — not qualified

This candidate additionally retains isolated UTF-16 code units at generic
`any`, `map[string]any`, `[]any` and scalar-string decoding boundaries. Native
Go strings use canonical UTF-8 for scalars and WTF-8 bytes for isolated units;
`jsonvalue.Marshal` emits the latter as JSON escapes, never invalid wire UTF-8.
Generic member names, equality and invocation-error data use that representation.
Other malformed native bytes retain the previous replacement behavior.

Typed struct decoding/encoding still selects the standard representation and
can replace these units, including in generic struct fields. SDK-owned wire
wrappers and document preparation need further work. Do not activate this
candidate or advertise whole-SDK string fidelity. This does not change document
UTF-8/duplicate-member requirements, Core, or binding specifications.

The helper is temporarily duplicated in isolated SDK and native-engine
candidates to prove the boundary without an evaluator dependency in JSON.
The final owned integration must resolve that duplication. This is not the
proposed permanent source layout or a new public package.

## Existing numerical carriage contract

This SDK implementation retains `encoding/json.Number` at generic JSON decode,
clone, invocation and context boundaries. Use `jsonvalue.Unmarshal(data, &value)`
when reading JSON into `any`; encode the result with `jsonvalue.Marshal`.
This is a small adapter over the standard library, not a new parser.

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
