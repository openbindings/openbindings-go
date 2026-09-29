# Go/TypeScript implementation parity

OpenBindings 0.2.0 treats the Go and TypeScript SDKs as two idiomatic
implementations of one observable contract. They run the same core
conformance corpus. The checked-in
[`reference-sdk-correspondence.json`](../spec/conformance/reference-sdk-correspondence.json)
also guards the public role and family correspondence.

This record covers the core. The layers the Go repository no longer carries
(invocation, synthesis and inspection, comparison, and the binding modules)
are preserved with their parity record on the `legacy/pre-core-rebuild`
branch; each rebuilt layer brings its parity entries back with it.

SDK-01 aligns Go with spec draft `ccfe0b6`: `Source.Kind` and
`DependencyEntry.Kinds` carry the new JSON names exactly, the embedded schema
is copied from that revision, and `DependencyEntry.AllowsKind` implements the
Core any-of constraint with exact string equality independent of runtime
support. Go accepts unknown kinds while validating a document and gives
source and binding content no Core interpretation. The former
`bindingSpec`/`bindingSpecs` names are unknown fields, not aliases.

Document validation reports the core's §10.4 conformance conclusion in Go:
`Interface.Validate(options)` and `ValidateDocument(data, options)` return a
`ValidationReport` with per-rule evidence, findings, and OBI-T-02
diagnostics. TypeScript applies OBI-T-09 to caller evidence through
`concludeConformance`, but `validateInterface` still returns violations
alone; TypeScript alignment is pending.

The Go core's exact document model and the validation that follows it
(2026-09-23) are established in Go first; TypeScript alignment is pending for
each of these observable behaviors:

- **Exact documents.** An optional member is absent exactly when it is
  absent in the document; members match by exact name; a duplicate member
  name, invalid UTF-8, a null where the model has no place for one, and a
  preference that is not an integer number in range fail decoding.
- **Rules over the document's JSON.** Every document rule is judged on the
  document's JSON, never its typed decoding, and literally on the values
  present; a resource limit is inconclusive, never a violation.
- **Operation-contract validation.** The OBI root is not a schema; success
  needs the complete statically reachable graph, available and well-formed,
  whatever branches an evaluator would skip. An unreferenced `$defs` entry is
  not part of that graph, while document well-formedness still checks it;
  `format` never asserts, in any
  dialect; a built-in meta-schema is available; an `$id` that names no one
  embedded schema leaves only the graphs that reach it unavailable; a
  version outside the supported set is refused; an alias names its
  operation; a reference cycle that never advances is unavailable.

| Concept | Go | TypeScript |
|---|---|---|
| validate a document, with its conformance conclusion | `Interface.Validate(options)` / `ValidateDocument(data, options)` | `validateInterface(...)` (report pending) |
| apply OBI-T-09 to rule evidence | `ConcludeConformance(...)` | `concludeConformance(...)` |
| compare a dependency's declared kind constraint | `DependencyEntry.AllowsKind(...)` | pending |
| exact named dependency lookup | removed 2026-09-23 (two map lookups) | `lookupDependency(...)` (removal pending) |
| immutable semantic OBI snapshot | removed 2026-09-23 (no Core role) | `prepareInterface(...)` (removal pending) |

Core parity means the same document fields and validation outcomes, exact
kind comparisons, version refusals, operation resolution, schema graph
outcomes, and conformance conclusions. Kind-specific support and invocation
behavior belong to later modules and are not established by this record.
Parity does not require identical type casing, incidental error prose, or
internal caches.

Names intentionally remain recognizable across languages whenever idiom
allows: `ValidateDocument` corresponds to `validateInterface`,
`ConcludeConformance` to `concludeConformance`, and so on. A user moving
between SDKs should recognize the role before learning its language-specific
mechanics.
