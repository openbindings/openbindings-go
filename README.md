# openbindings-go

The core [OpenBindings](https://openbindings.com) SDK for Go: the OBI document
model, its document rules and conformance report, operation resolution, and
validation of values against value contracts, as the core specification
defines them.

OpenBindings is an open standard. **One interface. Any binding.** Describe
operation contracts separately from how they are realized or consumed. An OBI
(OpenBindings Interface) document can declare operations it makes available
through bindings and named dependencies whose implementations are supplied by
its environment, independently of protocol. See the
[spec](https://github.com/openbindings/spec) for details.

**Spec version:** implements OpenBindings 0.2. `openbindings.SupportedVersions` states the versions this SDK supports (§8.1): every release of the 0.2 line (`0.2.x`), and no prerelease. `openbindings.CheckVersion(version)` makes the version decision (OBI-T-04) for a caller holding a document it decoded itself: it returns the `*VersionRefusalError` that `ParseDocument`, `ValidateDocument`, `Document.Validate`, `Document.References`, and `ValueContractCompiler.Resolve` return for a well-formed version outside the supported set, and nil otherwise. Nil means only that there is no refusal: a malformed version declares no version, which is an OBI-D-09 violation, never a refusal. `openbindings.AuthoringVersion` (`0.2.0`) is the version a document written with this SDK declares: the lowest version sufficient for everything the document model carries, as §8.1 asks of documents.

> **Draft status:** this branch implements the unreleased 0.2 working draft.
> The install command below describes the released package path; it does not
> install this branch until `v0.2.0` is tagged.

This implementation targets the Core 0.2 working draft on the spec's
`release/0.2` branch, at the revision it names, and passes that revision's
conformance corpus with no keyed expected failure. A validation report
names the revision of the text it applied (`ValidationReport.Revision`), and
CI tests the SDK against the corpus at exactly that revision.

**Conformance:** `ValidateDocument(data)` validates a document's exact bytes
and returns a `ValidationReport` in the vocabulary of
[§10.4](https://github.com/openbindings/spec/blob/release/0.2/openbindings.md#104-conformance-conclusions):
evidence for every document rule, findings located by JSON Pointer and by
line and column in the input, a
conclusion of conformant, non-conformant, or conformance-undetermined, and the
specification version its rule identifiers belong to, with its revision while
that version is a working draft.
No document rule requires an implementation or publication for a source's
kind. Core carries source and binding `content` without interpreting it.
`Document.Validate()` does the same for a document already in
memory, judging its serialization, which is what a claim about a value in
memory is about (§10); to judge a file, pass its bytes to `ValidateDocument`.
The rules judge the JSON a document is, never
its typed decoding, so a document the typed model cannot carry is still judged
in full, with two exceptions the SDK cannot read in full: a document holding a
string that escapes a lone UTF-16 surrogate, and input nested deeper than
encoding/json reads (10000 levels). For both, OBI-D-01 is decided, and so
is OBI-D-09, from the declared version. The other rules are inconclusive.
A text that violates OBI-D-01 (not UTF-8 JSON, beginning with a byte-order
mark, or repeating a member name) is non-conformant by that violation
alone: the other rules govern a JSON value only when OBI-D-01 holds, so the
report records them as not applicable (§10). `ValidateDocument` and
`Document.Validate` return a `*ValidationError` beside the
report exactly when a violation is established, so the error is the gate
before acting on a document; a nil error is not a conformance claim.
A version outside the supported set is refused, not concluded (OBI-T-04).
`ParseDocument` returns a `*VersionRefusalError`, a `*ValidationError`, or,
when it cannot read the document in full, an error matching
`ErrInconclusive`, which is no conformance conclusion either way.
OBI-D-02 and OBI-D-10 evaluate fixed schemas (the derived schema and the
JSON Schema 2020-12 meta-schemas, embedded at build time) with a private use
of [`santhosh-tekuri/jsonschema/v6`](https://github.com/santhosh-tekuri/jsonschema).
No document rule evaluates a value against the document's schemas: an
example is an author claim, which a tool can check against its value
contract. Validating values (OBI-T-08) takes a JSON Schema evaluator the
application supplies; see [Validate a value against a value
contract](#validate-a-value-against-a-value-contract). To exercise the core
conformance corpus, check out the
spec repo alongside this one (at `../spec`, or `./spec` inside the repo), or
point `OB_SPEC_CORPUS` at its `conformance` directory, and run `go test ./...`
here and in `schemaeval`, whose tests run the corpus's value cases.

**Implementation limits:** A lone escaped UTF-16 surrogate or input deeper
than the JSON decoder's 10,000-level limit prevents full document inspection.
OBI-D-10 leaves subschemas beyond 256 levels inconclusive. A value contract
gets a located no-verdict, before any evaluator runs, where core meets its
own limits (a schema nesting subschemas deeper than 256 levels, a pattern
nesting groups deeper than 256) or its conservative policies (such as a
cycle of schemas applied in place without advancing into the value, or two
resources sharing an identifier in normal form); each refusal says which
(`NoVerdictError`). Everything else is the evaluator's: which patterns and
numbers it can decide, and the time and memory evaluation takes, which grow
with the value and the schemas as in any JSON Schema validator, so an
application validating large or untrusted values bounds them itself.
`schemaeval` documents its own limits; among them, it locates a failed
`propertyNames` at the root of the value, since its library
(santhosh-tekuri/jsonschema v6.0.3) reports an unreliable instance location
for one below the value's top level. An inconclusive rule or a value
without a verdict is never reported as success or unqualified conformance.

**Declared capability limits:** OBI-T-08 lets a tool give no verdict where it
lacks a capability, and these are this SDK's. Each is a no-verdict, never a
wrong verdict. A value contract is decided as a whole, so what core or the
evaluator refuses withholds a verdict from every value, even one whose
evaluation would never reach it (a reference to a resource nobody supplied,
on a branch the value does not take). A value holding a string with a lone
UTF-16 surrogate, which a Go string cannot carry, gets no verdict. And
`schemaeval` gives no verdict where evaluation reaches a Unicode property
escape in a pattern, since Go's Unicode tables are not ECMA-262's.
The Core corpus does not exercise every behavior in OBI-T-01: the exact kind
comparison has direct Go tests, while Core has no kind-support registry or
implicit dereferencing path.

Pending TypeScript parity for the core is recorded in
[`IMPLEMENTATION_PARITY.md`](IMPLEMENTATION_PARITY.md).

## Scope, and the rebuild

This module carries what the core specification defines, directly or by
implication, and nothing a binding specification, a published interface, or
an application defines. Those layers build on it from outside.

Until 2026-09-24 the repository also carried the invocation runtime
(`invoke`), interface synthesis and source inspection (`synthesize`), HTTP
Discovery (`httpdiscovery`), acquisition (`acquire`), schema comparison
(`compare`, `schemaprofile`), the value layer (`jsonvalue` and its internal
packages), supporting utilities, and the eight `formats/*` binding modules.
They were removed so that each layer can be rebuilt on this core from its own
authority, and each returns only once it names that authority and passes its
conformance corpus. The removed code is preserved, runnable, on the
`legacy/pre-core-rebuild` branch at `aceb788`, the last commit at which every
module built and passed. The published `v0.1.0` and `formats/*/v0.1.0` tags
remain resolvable through the Go module proxy.

## Layout

```
.                          ← github.com/openbindings/openbindings-go (the core SDK)
  openbindingstest/        ← the conformance kit for schema evaluators
  schemaeval/              ← github.com/openbindings/openbindings-go/schemaeval,
                             the project's schema evaluator (its own module)
  internal/jsonpointer/    ← RFC 6901 pointers for finding locations
  internal/schemacompiler/ ← the document rules' use of the JSON Schema library,
                             and the ECMA-262 pattern grammar
  internal/kithook/        ← what the kit reaches that core does not export
```

The [`ob` CLI](https://github.com/openbindings/ob) is built on this SDK but lives in its own repo, with its own versioning and release cadence.

## Install

After 0.2 is released:

```
go get github.com/openbindings/openbindings-go
```

## What this SDK does

- **Core types** for the OpenBindings interface document (`Document`):
  operations, dependencies, bindings, sources, and schemas, named as the
  spec names them (`Operation`, `Dependency`, `Binding`, `Source`,
  `JSONSchema`). `Source.Kind` is an opaque nonempty string;
  `Dependency.Kinds` is an optional nonempty, unique any-of list.
  `Dependency.AcceptsKind` compares complete strings exactly, without
  inferring support, compatibility, or version order
- **An exact document model**: re-encoding a decoded document reproduces every member, present empty values, unknown fields, and `x-*` extensions included, and a document the model cannot carry exactly fails decoding rather than being altered
- **Validation** reporting per-rule evidence and a §10.4 conformance conclusion, an unknown unprefixed field reported as an OBI-D-02 violation (§12 reserves those names), and a violation gate for acting on documents
- **Operation resolution** by key or alias (`Document.ResolveOperation`),
  and an operation's bindings found by its key (`Document.OperationBindings`)
- **Schema references** (`Document.References`): every `$ref` and
  `$dynamicRef` in the schemas a document contains whose value is a
  URI-reference (a string that is not one is no reference of any form,
  §7.1), with the schema each one's initial lookup identifies, looked up as
  OBI-D-12 and value validation look them up
- **Value-contract validation** of values against an operation's input or output contract (§3, OBI-T-08), with a JSON Schema evaluator the application supplies: core resolves the document's schemas (§7), refuses what the specification leaves undefined, and hands the evaluator a closed JSON Schema 2020-12 bundle per value contract; the evaluator evaluates. [`schemaeval`](schemaeval) is the project's evaluator, and [`openbindingstest`](openbindingstest) checks any evaluator against the contract

## Quick start

### Parse and validate an OBI

```go
import (
    "encoding/json"
    openbindings "github.com/openbindings/openbindings-go"
)

// ParseDocument is the front door for untrusted or wire bytes: beyond the
// exact decoding json.Unmarshal also performs (OBI-D-01's checks included), it
// refuses an unsupported version (OBI-T-04) and applies the document schema
// (OBI-D-02).
doc, err := openbindings.ParseDocument(data)
if err != nil {
    log.Fatal(err) // a *VersionRefusalError, a *ValidationError, or ErrInconclusive
}
if _, err := doc.Validate(); err != nil {
    log.Fatal(err)
}

// Optional members are pointers, nil when absent; Value reads one where
// absence and the zero value mean the same.
fmt.Println(openbindings.Value(doc.Name), openbindings.Value(doc.Version))
for name, op := range doc.Operations {
    fmt.Println(name, openbindings.Value(op.Description))
}
```

A name resolves to an operation by its key or an alias, and the operation's
bindings are found by the key it resolves to (OBI-T-07):

```go
key, _, found := doc.ResolveOperation("tasks.create")
if !found {
    log.Fatal("no operation named tasks.create")
}
for _, binding := range doc.OperationBindings(key) { // sorted for display
    fmt.Println(binding, doc.Sources[doc.Bindings[binding].Source].Kind)
}
```

A dependency names the local operation it consumes by exact key (OBI-D-11);
dependency keys and their operation references do not use alias resolution:

```go
dependency, ok := doc.Dependencies["customerDelivery"]
if !ok {
    log.Fatal("no dependency named customerDelivery")
}
operation := doc.Operations[dependency.Operation]
fmt.Println(dependency.Operation, dependency.Kinds, openbindings.Value(operation.Description))
```

### Validate a value against a value contract

Value validation takes an evaluator; `schemaeval` is the project's. Resolve a
document's schemas once, compile the value contracts you validate against,
and keep them:

```go
import (
    openbindings "github.com/openbindings/openbindings-go"
    "github.com/openbindings/openbindings-go/schemaeval"
)

compiler, err := openbindings.NewValueContractCompiler(schemaeval.New(schemaeval.Options{}))
if err != nil { return err }
contracts, err := compiler.Resolve(ctx, doc)      // §7 resolution; no evaluator yet
if err != nil { return err }
input, err := contracts.CompileInput(ctx, "tasks.create")
if err != nil { return err }                      // no such operation, or ctx done
if err := input.Err(); err != nil {
    log.Printf("tasks.create inputs cannot be validated: %v", err)
}

switch err := input.ValidateJSON(ctx, body); {
case err == nil:
    // valid
case errors.Is(err, openbindings.ErrMismatch):
    // reject; errors.As for *MismatchError and its Problems
case errors.Is(err, openbindings.ErrNoVerdict):
    // no verdict (not a rejection): no schema there (ErrNoValueContract), a
    // result the specification leaves undefined (ErrUndefined), a capability
    // core or the evaluator lacks, or a done ctx
case errors.Is(err, openbindings.ErrInconclusive):
    // the body is not JSON, so there is nothing to judge
}
```

`Validate` takes a Go value, read as encoding/json encodes it. Core keeps no
compiled contract: a service compiles the contracts it serves at startup, and
one compiling on demand shares a compile through `singleflight` under a ctx
no single request owns (see the examples in `schemaeval`). Schemas the
document references but does not embed are supplied as `Resource`s to
`NewValueContractCompiler`; core fetches nothing.

### List a document's schema references

`Document.References` lists every `$ref` and `$dynamicRef` in the schemas a
document contains whose value is a URI-reference, for a tool that renames,
removes, or copies schemas:

```go
refs, err := doc.References()
if err != nil {
    return err // refused, no valid version, cannot be encoded, or an index it could not complete
}
for _, r := range refs {
    fmt.Println(r.Location, r.Value, r.Target) // Target "" when it identifies no schema; Unresolved says why
}
```

It fetches and evaluates nothing. A schema no reference targets is not
thereby unused, and a `$dynamicRef` may land elsewhere than its initial
target; its doc says what a caller may and may not conclude.

### Write an evaluator

An evaluator implements two methods over a bundle core writes (see
`SchemaEvaluator` for the contract), and proves itself with the kit:

```go
func TestEvaluator(t *testing.T) {
    openbindingstest.TestSchemaEvaluator(t, myevaluator.New(), openbindingstest.Options{
        Undecided: map[string]string{
            "adversarial/lazy-lookahead/2": "RE2 has no lookaround",
        },
    })
}
```

The kit runs the JSON Schema Test Suite's draft2020-12 tests (with its remote
fixtures) and adversarial cases of its own through core and on the evaluator
directly, checks problem paths, the evaluator's errors, and invariants
(concurrency, isolation, cancellation, no network), and fails any wrong
verdict.

What an evaluator must do has three sources (see `SchemaEvaluator`), and
only the first is the specification's: OBI-T-08's semantics and its
no-verdict rule; the limits of the library it adapts (regular-expression
dialect, arithmetic, loader, error shape), each a no-verdict where evaluation
reaches it; and this SDK's diagnostic contract for problem locations, which
`Options.Unlocated` can exempt case by case.

## License

Apache-2.0
