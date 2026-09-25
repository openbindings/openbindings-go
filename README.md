# openbindings-go

The core [OpenBindings](https://openbindings.com) SDK for Go: the OBI document
model, its document rules and conformance report, operation resolution, and
validation of values against operation contracts, as the core specification
defines them.

OpenBindings is an open standard. **One interface. Any binding.** Describe
operation contracts separately from how they are realized or consumed. An OBI
(OpenBindings Interface) document can declare operations it makes available
through bindings and named dependencies whose implementations are supplied by
its environment, independently of protocol. See the
[spec](https://github.com/openbindings/spec) for details.

**Spec version:** implements OpenBindings 0.2. `openbindings.SupportedVersions` states the versions this SDK supports (§8.1): every release of the 0.2 line (`0.2.x`), and no prerelease. `openbindings.IsSupportedVersion(version)` decides membership: for a well-formed SemVer version it returns true exactly when `Validate` / `ParseDocument` would interpret (not refuse) a document declaring it (a malformed version is an OBI-D-12 violation, not a refusal). `openbindings.AuthoringVersion` (`0.2.0`) is the version a document written with this SDK declares: the lowest version sufficient for everything the document model carries, as §8.1 asks of documents.

> **Draft status:** this branch implements the unreleased 0.2 working draft.
> The install command below describes the released package path; it does not
> install this branch until `v0.2.0` is tagged.

**Conformance:** `ValidateDocument(data, options)` validates a document's exact bytes
and returns a `ValidationReport` in the vocabulary of
[§10.5](https://github.com/openbindings/spec/blob/release/0.2/openbindings.md#105-conformance-conclusions):
evidence for every document rule, located findings, OBI-T-02 diagnostics, and a
conclusion of conformant, non-conformant, or conformance-undetermined.
No document rule needs knowledge of a binding specification: a source's
`content` and a binding's `selector` are the binding specification's to
define, and no core rule judges them. OBI-D-18 needs a parser for the transform language: the SDK carries
none, so an application passes its `TransformParser` in
`ValidateOptions.Transforms`, and without one the rule is inconclusive.
`Interface.Validate(options)` does the same for a document already in
memory, where OBI-D-01 is inconclusive because a host object no longer
carries the exact input bytes. The rules judge the JSON a document is, never
its typed decoding, so a document the typed model cannot carry is still judged
in full, with two exceptions the SDK cannot read in full: a document holding a
string that escapes a lone UTF-16 surrogate, and input nested deeper than
encoding/json reads (10000 levels). For both, OBI-D-01 is decided and every
other rule is inconclusive. Both return a `*ValidationError` beside the
report exactly when a violation is established, so the error is the gate
before acting on a document; a nil error is not a conformance claim.
A version outside the supported set is refused, not concluded (OBI-T-04).
OBI-D-13, OBI-D-14, and OBI-D-15 are retired identifiers. OBI-D-02, OBI-D-11, and
OBI-D-17 use [`santhosh-tekuri/jsonschema/v6`](https://github.com/santhosh-tekuri/jsonschema);
the core schema and locally required JSON Schema 2020-12 meta-schemas are
embedded at build time. To exercise the core conformance corpus, check out the
spec repo alongside this one (at `../spec`, or `./spec` inside the repo), or
point `OB_SPEC_CORPUS` at its `conformance` directory, and run `go test ./...`.

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
  internal/jsonpointer/    ← RFC 6901 pointers for finding locations
  internal/schemacompiler/ ← the SDK's use of the JSON Schema library
```

The [`ob` CLI](https://github.com/openbindings/ob) is built on this SDK but lives in its own repo, with its own versioning and release cadence.

## Install

After 0.2 is released:

```
go get github.com/openbindings/openbindings-go
```

## What this SDK does

- **Core types** for the OpenBindings interface document: operations,
  dependencies, bindings, sources, transforms, and schemas
- **An exact document model**: re-encoding a decoded document reproduces every member, present empty values, unknown fields, and `x-*` extensions included, and a document the model cannot carry exactly fails decoding rather than being altered
- **Validation** reporting per-rule evidence and a §10.5 conformance conclusion, unknown fields surfaced as diagnostics rather than rejected, and a violation gate for acting on documents
- **Operation resolution** by key or alias (`ResolveOperation`)
- **Operation-contract validation** of values against an operation's input or output schema, resolved against the whole document (§7, OBI-T-16): `ValidateOperationInput`, `ValidateOperationOutput`, and `CompileOperationSchema` to compile once and validate many values
- **The two transform capabilities** the specification names, as interfaces an application implements: `TransformParser` (OBI-D-18) and `TransformEvaluator` (§5.5, OBI-T-10). The SDK carries no transform engine; an application gives one implementation to every layer that parses or evaluates transforms, so the expression validation accepts is the expression that runs

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
iface, err := openbindings.ParseDocument(data)
if err != nil {
    log.Fatal(err)
}
if _, err := iface.Validate(openbindings.ValidateOptions{}); err != nil {
    log.Fatal(err)
}

// Optional members are pointers, nil when absent; Value reads one where
// absence and the zero value mean the same.
fmt.Println(openbindings.Value(iface.Name), openbindings.Value(iface.Version))
for name, op := range iface.Operations {
    fmt.Println(name, openbindings.Value(op.Description))
}
```

A dependency names the local operation it consumes by exact key (OBI-D-19);
dependency keys and their operation references do not use alias resolution:

```go
dependency, ok := iface.Dependencies["customerDelivery"]
if !ok {
    log.Fatal("no dependency named customerDelivery")
}
operation := iface.Operations[dependency.Operation]
fmt.Println(dependency.Operation, dependency.BindingSpecs, openbindings.Value(operation.Description))
```

### Validate a value against an operation contract

```go
// A nil error means the value validates. A *SchemaValidationError is an
// established mismatch; a *SchemaGraphUnavailableError means the schema's
// graph could not be fully resolved, so no verdict was reached.
if err := openbindings.ValidateOperationInput(value, iface, "listItems"); err != nil {
    log.Fatal(err)
}
```

## License

Apache-2.0
