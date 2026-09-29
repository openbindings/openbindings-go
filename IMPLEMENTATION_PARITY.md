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

The core alignment with spec draft `0e2a8d5` (2026-09-29) is established in
Go first; TypeScript alignment is pending for each of these:

- **Model.** `idempotent` is a binding member; a dependency carries an
  optional `description`.
- **Rule numbering.** OBI-D-01 through OBI-D-13 as the draft numbers them; no
  `$vocabulary` rule and no example validity (examples are author claims).
- **References.** OBI-D-05 admits plain names and the dynamic pair in the
  document resource; OBI-D-12 decodes a fragment once, then reads a pointer or
  a plain name the document resource declares, and leaves absolute references
  to JSON Schema; the legacy `definitions` and `dependencies` hold OBI
  positions.
- **Uniqueness.** OBI-D-13 counts each `$anchor` and `$dynamicAnchor`
  declaration and compares `$id`s after strict RFC 3986 §5.2 resolution.
- **Resource boundaries.** A schema with an `$id` member is a boundary
  whatever the member's value; a pointer from the document resource reaches
  nothing inside one, for OBI-D-12 and value validation alike.
- **Version decision.** A text beginning with a byte-order mark declares no
  version (OBI-T-04). A report names the release whose text it applies
  (OBI-T-09).
- **Value validation.** Patterns are ECMA-262 with the `u` flag (native in
  TypeScript); a match that reaches no answer, and an absent schema, give no
  verdict (OBI-T-08), as do a resource declaring one name twice, an `$id` of
  `""` or `#`, and a cycle a `$dynamicRef` closes at run time.

Document validation reports the core's §10.4 conformance conclusion in Go:
`Interface.Validate(options)` and `ValidateDocument(data, options)` return a
`ValidationReport` with per-rule evidence and findings. TypeScript applies OBI-T-09 to caller evidence through
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
- **Operation-contract validation.** The OBI root is the resolution context,
  not a schema. A claimed value result follows the applicable JSON Schema
  dialect; a missing capability or resource yields no verdict. Go currently
  uses an eager compiler, so it can return no verdict for a graph containing
  an unavailable branch even when a particular value does not enter that
  branch. That limitation is Go behavior, not a Core or parity requirement.
  Both SDKs must distinguish an established mismatch from no verdict.

| Concept | Go | TypeScript |
|---|---|---|
| validate a document, with its conformance conclusion | `Interface.Validate(options)` / `ValidateDocument(data, options)` | `validateInterface(...)` (report pending) |
| apply OBI-T-09 to rule evidence | `ConcludeConformance(...)` | `concludeConformance(...)` |
| compare a dependency's declared kind constraint | `DependencyEntry.AllowsKind(...)` | pending |
| exact named dependency lookup | removed 2026-09-23 (two map lookups) | `lookupDependency(...)` (removal pending) |
| immutable semantic OBI snapshot | removed 2026-09-23 (no Core role) | `prepareInterface(...)` (removal pending) |

Core parity means the same document fields and established validation outcomes,
exact kind comparisons, version refusals, operation resolution, and sound
conformance conclusions. Kind-specific support and invocation
behavior belong to later modules and are not established by this record.
Parity does not require identical type casing, incidental error prose, or
internal caches.

Names intentionally remain recognizable across languages whenever idiom
allows: `ValidateDocument` corresponds to `validateInterface`,
`ConcludeConformance` to `concludeConformance`, and so on. A user moving
between SDKs should recognize the role before learning its language-specific
mechanics.
