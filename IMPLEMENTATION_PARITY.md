# Go/TypeScript implementation parity

OpenBindings 0.2.0 treats the Go and TypeScript SDKs as two idiomatic
implementations of one observable behavior. They run the same core
conformance corpus.

This record covers the core and the optional HTTP discovery module. The layers
the Go repository no longer carries
(invocation, synthesis and inspection, comparison, and the binding modules)
are preserved with their parity record on the `legacy/pre-core-rebuild`
branch; each rebuilt layer brings its parity entries back with it.

The rebuilt `httpdiscovery` module (HTTP Discovery companion v0.1.0) is
established in Go first; TypeScript alignment is pending. Its observable
contract is documented in [httpdiscovery/README.md](httpdiscovery/README.md):
origin-only endpoint construction, both JSON media types, redirect metadata,
404 absence distinct from gated discovery and other HTTP errors, core version
refusals/violations/inconclusive results preserved, bounded response reads, and
an immutable publishing handler requiring established document conformance.
Client transport/redirect and server authentication/CORS policies are supplied
by the application. Its rule-keyed conformance and transport tests accompany
the module; they do not constitute a cross-SDK parity claim.

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

The revised 0.2 working draft (spec `cbc17a6`, adopted 2026-10-01) is
established in Go first; TypeScript alignment is pending for each of these,
each with the spec CHANGELOG entry (0.2.0 working draft) that states it:

- **OBI-D-01 decides alone** (Changed, "What the other document rules say
  about a text OBI-D-01 rejects"). On a text violating OBI-D-01, OBI-D-02
  through OBI-D-13 are not applicable, recorded so with no finding, and the
  conclusion is non-conformant from OBI-D-01 alone; nothing is left
  inconclusive. A validator that has not decided OBI-D-01 and fails a check
  it could make exactly concludes non-conformant (§10).
- **A check made while OBI-D-01 is undecided** (the same entry). Go's
  `Document.Validate` cannot write a host object beyond the SDK's own
  limits, so it leaves OBI-D-01 undecided, and it decides OBI-D-09 on the
  declared version, which it holds exactly. A failed check is recorded as
  OBI-D-09 violated and concludes non-conformant, as §10 permits, though
  the check establishes only that OBI-D-01 or OBI-D-09 is violated, not
  which. This attribution is accepted as Go's; TypeScript makes the same
  one, so the two SDKs report the same rule-level evidence for such a
  value.
- **Plain names by the grammar** (Changed, "Only a grammatical anchor
  declares a plain name"). Only an `$anchor` or `$dynamicAnchor` whose
  value matches JSON Schema Core §8.2.2's grammar as a whole declares a
  name, for OBI-D-12's lookup, OBI-D-13's count, and reference resolution
  everywhere (`#1bad` never targets `"$anchor": "1bad"`); OBI-D-10 still
  reports the value.
- **Dialects by resource** (Changed, "A schema resource without `$schema`
  inherits its dialect"). A schema is read under its resource's dialect:
  the document resource's is 2020-12 whatever `$schema` it holds, an `$id`
  resource takes its root's `$schema` or its enclosing resource's, and a
  misplaced `$schema` selects nothing. A foreign `$schema` still violates
  OBI-D-06; value validation gives no verdict only where the resource's
  dialect is one the tool lacks.
- **Malformed strings are no references** (Changed, "A malformed reference
  string is not a reference"). A `$ref` or `$dynamicRef` string that is not
  a well-formed URI-reference is not a reference of any form: OBI-D-05
  reports it in the document resource, OBI-D-12 does not govern it, a value
  whose evaluation depends on it gets no verdict, and the schema-reference
  listing omits it.
- **Ambiguous dynamic capture** (no spec change: JSON Schema Core §8.2.2,
  §8.2.3.2, with §7.2's outermost scope). A value whose evaluation,
  beginning in the document resource, looks a `$dynamicRef`'s name up in
  the dynamic scope gets no verdict when the document resource declares
  that name more than once, by either keyword.
- **Corpus format @2** (Added, "`openbindings.core-tool-scenarios@2`").
  The revised text's corpus holds scenarios in format
  `openbindings.core-tool-scenarios@2` alone; `resolve-schema-cycle`, the
  old outcome tokens (`graph-unavailable`, `resolver-error`), and the
  validity fixtures that listed OBI-T-04 as a version refusal are gone. A
  corpus runner reads @2's actions and expectations: value results as
  `valid`, `instance-mismatch`, or `no-verdict` (bare or in object form
  with `orNoVerdict` and `dependsOn`), `notViolated` beside `violates`,
  validation outcomes where `conformant` admits `conformance-undetermined`
  when the validator lacks evidence, and conclusions naming the applied
  text, verified against the revision's pinned hash. The separate
  `conclude-conformance` action requires the exact conclusion determined
  by its supplied evidence (§10.4). Go's adapter reads @2 only.

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
  refusal the refusing entry points return (parsing, validating bytes or a
  document in memory, listing references, and resolving value contracts),
  or none; no refusal for a text that declares no version (OBI-T-04). The
  operation lookups refuse nothing. No exported SemVer validity predicate.
- **Inconclusive, distinctly.** One category for a call that decided
  nothing because the input is beyond the SDK's own limits: a parse that
  cannot read a document in full, a document nesting past the decoder or
  holding an escaped lone UTF-16 surrogate, given as bytes or, in memory,
  where the SDK's own checks find it in the text the document encodes to or
  in a member the model carries as raw JSON (a document that fails to encode
  at all matches no category; Go states the boundary in `Document.Validate`),
  and an incomplete reference index; never a conformance conclusion or a
  value verdict, and disjoint from them whatever an evaluator or a value's
  own encoding says. A document declaring no valid version gets its
  established OBI-D-09 violation from the reference index and value-contract
  resolution, as from validation. Value input that is not JSON gets an error
  of no category: there is no value, so nothing is undecided.
- **Schema references.** Every `$ref` and `$dynamicRef` in the schemas a
  document contains whose value is a well-formed URI-reference, with
  location, keyword, value, base, initial target or why there is none, by
  the same lookup as OBI-D-12 and value validation;
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
| name the specification text a conclusion applied, with its revision while a working draft (OBI-T-09) | `ValidationReport.Release` and `ValidationReport.Revision` | pending |
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

The conformance-conclusion clarification (spec `04a8413`, adopted 2026-10-02)
keeps positive reporting optional while preserving the meaning of an
undetermined conclusion. Go already computed the specified conclusion; its
corpus adapter now requires that exact result from supplied evidence. This
records the Go adoption without asserting a new TypeScript parity result.
