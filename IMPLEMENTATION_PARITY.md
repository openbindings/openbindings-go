# Go/TypeScript implementation parity

OpenBindings 0.2.0 treats the Go and TypeScript SDKs as two idiomatic
implementations of one observable behavior. They run the same core
conformance corpus. The checked-in
[`reference-sdk-correspondence.json`](../spec/conformance/reference-sdk-correspondence.json)
also guards the public role and family correspondence.

This record covers the core. The layers the Go repository no longer carries
(invocation, synthesis and inspection, comparison, and the binding modules)
are preserved with their parity record on the `legacy/pre-core-rebuild`
branch; each rebuilt layer brings its parity entries back with it.

SDK-01 aligns Go with spec draft `ccfe0b6`: `Source.Kind` and
`Dependency.Kinds` carry the new JSON names exactly, the embedded schema
is copied from that revision, and `Dependency.AcceptsKind` implements the
Core any-of constraint with exact string equality independent of runtime
support. Go accepts unknown kinds while validating a document and gives
source and binding content no Core interpretation. The former
`bindingSpec`/`bindingSpecs` names are unknown fields, not aliases.

The core alignment with the spec draft of openbindings/spec#129 (2026-09-29) is established in
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
- **Host objects.** Validating a document in memory decides OBI-D-01 on its
  serialization (§10), so it can conclude conformant.
- **Version decision.** A text beginning with a byte-order mark declares no
  version (OBI-T-04). A report names the release whose text it applies
  (OBI-T-09).
- **Value contracts.** Validating a value (OBI-T-08) takes a schema
  evaluator the application supplies; the core has none of its own. The core
  resolves the document's schemas and the supplied resources (§7), refuses
  before evaluation, located and labeled, what the specification leaves
  undefined (`ErrUndefined`), what it lacks the capability for, and what its
  conservative policies refuse, and hands the evaluator a closed 2020-12
  bundle. A value gets valid, an established mismatch with located problems,
  or no verdict. Go's `openbindingstest` kit runs the whole draft2020-12 JSON
  Schema Test Suite and adversarial cases through any evaluator
  (`openbindingstest/testdata/json-schema-test-suite/README.md`).

Document validation reports the core's §10.4 conformance conclusion in Go:
`Document.Validate()` and `ValidateDocument(data)` return a
`ValidationReport` with per-rule evidence and findings. TypeScript applies OBI-T-09 to caller evidence through
`concludeConformance`, but `validateInterface` still returns violations
alone; TypeScript alignment is pending.

The Go core's exported API for 0.2 (2026-10-01) is established in Go first;
TypeScript alignment is pending for each of these. Parity is in observable
behavior and recognizable names, not identical signatures:

- **Names.** The document type is a document (`Document`), and the binding
  and dependency objects are a binding and a dependency, as the spec's §3
  names them; a dependency's kind check is "accepts" (`AcceptsKind`).
- **Lookups on the document.** Resolving a name to an operation against its
  key and its aliases, with equal standing, where a name several operations
  carry resolves to none (OBI-T-07); and finding an operation's bindings by
  its key alone, sorted for presentation, where an alias or a key no
  operation has finds nothing. Neither finds anything in a missing document.
- **The version decision as a refusal.** A check that returns the same
  refusal every entry point returns, or none; no refusal for a text that
  declares no version (OBI-T-04). No exported SemVer validity predicate.
- **Inconclusive, distinctly.** One category for a call that decided
  nothing: a parse that cannot read a document in full, a document declaring
  no valid version, a document nesting past the decoder, given as bytes or
  in memory, an incomplete reference index, and value input that is not
  JSON; never a conformance conclusion or a value verdict, and disjoint
  from them whatever an evaluator or a value's own encoding says.
- **Schema references.** Every `$ref` and `$dynamicRef` in the schemas a
  document contains, with location, keyword, value, base, initial target or
  why there is none, by the same lookup as OBI-D-12 and value validation;
  the same whole-call refusals; an incomplete index returned with the
  inconclusive category.
- **Concluding from evidence.** The conclusion is reached from the document
  rules alone: evidence under any other identifier is dropped and decides
  nothing, and a document rule missing from the evidence is inconclusive, so
  empty evidence concludes undetermined. The concluded evidence holds exactly
  the document rules.
- **Locating unknown members.** OBI-D-02 reports each member the document
  schema does not allow at the member, one finding each.
- **Writing a document.** Typed members in field order, then kept members in
  name order.

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
- **Value-contract validation.** The OBI root is the resolution context,
  not a schema. A claimed value result follows the applicable JSON Schema
  dialect; a missing capability or resource yields no verdict. A refusal
  follows what evaluation can reach; a problem in a schema a contract copies
  but cannot reach is refused by conservative policy. Both SDKs must
  distinguish an established mismatch from no verdict.

| Concept | Go | TypeScript |
|---|---|---|
| validate a document, with its conformance conclusion | `Document.Validate()` / `ValidateDocument(data)` | `validateInterface(...)` (report pending) |
| apply OBI-T-09 to rule evidence | `ConcludeConformance(...)` | `concludeConformance(...)` |
| compare a dependency's declared kind constraint | `Dependency.AcceptsKind(...)` | pending |
| resolve an operation name, and find its bindings by key | `Document.ResolveOperation(...)`, `Document.OperationBindings(...)` | pending |
| decide a declared version (OBI-T-04) | `CheckVersion(...)` | pending |
| list a document's schema references | `Document.References()` | pending |
| mark what decided nothing | `ErrInconclusive` | pending |
| validate a value against a value contract, with the application's evaluator | `NewValueContractCompiler(...)`, `Resolve`, `CompileInput` / `CompileOutput`, `ValueContract.Validate` | pending |
| check an evaluator against the evaluator contract | `openbindingstest.TestSchemaEvaluator(...)` | pending |
| name the specification text a conclusion applied, with its revision while a working draft (OBI-T-09) | `ValidationReport.Version` and `ValidationReport.Revision` | pending |
| position a finding in the input bytes | `Finding.Position` | pending |
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
