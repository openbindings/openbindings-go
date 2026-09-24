# Changelog

## 0.2.0 (working draft)

> A 0.1.1 patch release was prepared 2026-04 but never tagged or published; its entries are folded into this section.

### Fixed

- **One operation's evidence no longer depends on another's, and the same
  bytes give the same report.** The library shared state across the
  operations of a document, and listed the members `additionalProperties:
  false` rejects in map order: one document gave four different OBI-D-11
  reports over 200 runs.
- **Validating a document takes time in proportion to it.** Each schema was
  processed on a copy of the library's bookkeeping for the whole document, so
  an ordinary document compiled in quadratic time. ValidateDocument on
  Stripe's API (612 operations, 1,537 schemas) takes about 1 s, from 5.5 s;
  a 1.1 MB document whose schemas all reference one another, 3.3 s from 37
  s, the rest being the library's own compile. Payload validation is
  unchanged, a few microseconds.
- **The model encodes only what it would decode back unchanged.** A member
  carried as raw JSON (an example value, source content, or an
  `Extensions`/`Unknown` entry) holding what decoding refuses (an escaped
  lone UTF-16 surrogate, a repeated member name, invalid UTF-8, or nesting
  deeper than the decoder reads) now fails `MarshalJSON`, where encoding/json
  wrote it out or altered it. So does invalid UTF-8 in any typed string or
  map key (a description, a schema's `const`, a member name), which
  encoding/json replaced with U+FFFD and could merge two names into one; a
  name held in both `Extensions` and `Unknown`, of which only one value could
  be written; and any other bytes the encoding writes that decoding would
  refuse, such as a schema value with its own `MarshalJSON`.
  `Interface.Validate` and the contract API judge a host object's encoding,
  so such an object returned a verdict on a different document: an example
  `"\ud800"` passed a `const` of U+FFFD. They now return the encoding error
  and no report. A nil `Extensions` or `Unknown` entry still encodes as
  `null`.
- **A number beyond the numeric limits, or a subschema past the depth
  limit, hides only what depends on it.** Each guard set a whole value
  aside, hiding violations that were certain: `bindingSpecs: ["", 1e99999]`,
  a schema `{"type": 42, "default": 1e99999}`, a schema holding `type: 42`
  above 300 nested `not`s, and an example `{"a": 1, "b": 1e99999}` for a
  schema whose `a` is a string were all undetermined. Now:
  - Such a number is replaced by a stand-in within the limits, with the same
    sign, an integer exactly when it is one, and equal to another number
    exactly when it is. The stand-in is used wherever the check tells numbers
    apart by nothing else: the document schema (OBI-D-02), the 2020-12
    meta-schemas (OBI-D-17), and an operation's schema graph that compares no
    number by order or divisibility and holds none in `const` or `enum`
    (OBI-D-11, and `CompiledSchema.Validate`). A finding on a stand-in states
    the number it stands for. Tests hold the document schema and the
    meta-schemas to that premise. Where the graph does compare numbers, the
    value still reaches no verdict, and the error names where.
  - An operation's schema meets the numeric limits only through a number the
    library reads: a keyword's value, or one in `const` or `enum`. A number
    in `default`, `examples`, or an unknown keyword no longer leaves the
    schema unavailable.
  - OBI-D-17 checks a schema down to 256 levels and is inconclusive only for
    the subschemas below.
  - Input nested past 10,000 levels has OBI-D-12 decided on the version it
    declares, read by the same scan as OBI-T-04's.
- **Every schema the library compiles is checked, and a reference is read
  only in a schema.** A schema a reference names in an annotation (an `x-`
  member or other unknown keyword) is compiled by the schema library as a
  schema of its own, but core checked only what a copy's keywords reach, so
  such a schema escaped the resource limits, the well-formedness and pattern
  checks, and the numeric-comparison check: a `minimum` there read a
  stand-in's value, and a number beyond the limits there reached the library,
  which dropped the keyword and validated anything. A `$ref` held in an
  annotation's data, which no reference names, was read as a reference, and
  a target that is not a schema left the graph without a verdict where a
  mismatch was certain. Copying data an operation schema only carries took
  time quadratic in its depth.
- **OBI-D-01 no longer depends on how encoding/json reads deep input.** One
  scan of the input checks JSON syntax, repeated names, lone surrogates, and
  the declared version, at any depth and with its own stack. Under
  encoding/json built on its v2 implementation (`GOEXPERIMENT=jsonv2`),
  valid input nested past 10,000 levels was an OBI-D-01 violation and a
  deeply nested unsupported version was not refused; neither depends on
  encoding/json now. A syntax error is worded as encoding/json words its own
  (for example "invalid character '1' after top-level value"), at any depth.
  The single pass is also about a third faster than the two it replaces.
- **An `$id` of `""` or `"#"` declares no resource**, as the schema library
  reads it. Inside an embedded resource it was taken for a second resource
  with the same URI, so every reference to the resource was ambiguous: a
  violating example left OBI-D-11 inconclusive and validation reported the
  graph unavailable.
- **OBI-D-16 is violated when an ambiguous reference resolves nowhere.** A
  reference to a URI that more than one schema declares names no one schema
  and stays inconclusive, unless its fragment resolves within none of them.
- **An absolute URI names the resource it resolves to.** A `$id` or `$ref`
  holding dot segments (`https://example.com/x/../a`) was compared as
  written, while the schema library removes them (RFC 3986 §5.2.4). A
  reference to the embedded schema by the other spelling was read as
  external: a violating example was skipped and the document concluded
  conformant, validation of a value reported the graph unavailable, and a
  fragment that resolves nowhere in the resource satisfied OBI-D-16.
- **OBI-D-01 is decided at any depth.** Input nested deeper than
  encoding/json reads (10,000 levels) is read in full, so a repeated name, a
  syntax error, or trailing data anywhere in it violates OBI-D-01. Input that
  satisfies OBI-D-01 still cannot be decoded, so every other rule but
  OBI-D-12, decided on the version read from the bytes, is inconclusive.
- **A leading byte-order mark does not hide the declared version.** A
  BOM-prefixed document declaring an unsupported version is refused
  (OBI-T-04), as one with invalid UTF-8 already was, rather than judged
  under 0.2's OBI-D-01.
- **OBI-D-16 resolves a malformed same-document fragment.** A `$ref` of
  `#/schemas/Missing Thing` violates OBI-D-05 and, resolving nowhere, now
  OBI-D-16 as well; it satisfied OBI-D-16.
- **Validation no longer does quadratic work in three places on hostile
  input.** Setting members aside for OBI-D-02 copied the document once per
  member (232 KB allocated 5 GB); collecting schema resources and walking
  schemas for OBI-D-05/06/07/16 copied the path at every node (62 KB
  allocated 640 MB); and every anchor reference re-walked its resource
  (232 KB took 3.3 s). Each now does one pass, and an anchor's location is
  spelled out only when a reference resolves to it. A chain of thousands of
  nested `$id` resources still costs time quadratic in its depth, as does a
  report with a finding at every level of a deeply nested schema. OBI-D-17
  is inconclusive for the subschemas a schema nests deeper than 256 levels,
  the limit compilation applies, since the meta-schema validator's work
  grows faster than linearly with that depth; data inside a schema (`const`,
  `default`, and the like) does not count toward either limit.
- **A document with a large number in its aliases no longer crashes
  validation.** An operation's `aliases` or a dependency's `bindingSpecs`
  holding more than 20 items, one of them a number beyond the numeric limits
  of schema evaluation, made `ParseDocument` and `ValidateDocument` panic
  inside the schema library, whose `uniqueItems` compares items as numbers.
  No such number reaches the library (see above).
- **A number beyond the limits no longer hides any of OBI-D-02.** A
  `preference` is decided exactly, however it is spelled: `1e10001` violates
  its range, and `1.` followed by 5,000 zeros is 1. Any other such number is
  checked as a stand-in (see above), which the document schema cannot tell
  from it: a test holds that the schema compares numbers only at a
  preference. The typed model decodes a preference with the same check,
  whose work no longer grows with the exponent.
- **An unsupported version is refused however deeply the input nests.** The
  declared version is read by a scan with no depth limit, so input nested
  past the decoder's 10,000 levels is refused under OBI-T-04 rather than
  reported conformance-undetermined.
- **A repeated member name is located.** The OBI-D-01 finding is at the
  object that repeats it. A leading byte-order mark is named as such.
- **The same document gets the same findings on every run.** The URI the
  schema library is given for the document is derived from the document's
  content rather than drawn at random, and messages locate a schema by its
  JSON Pointer in the document rather than by that URI.
- **Checking for repeated names and lone surrogates is linear in the
  input.** It copied a value's location for every value, so deep and wide
  input cost depth times width: a 2 MB document took 16 seconds to parse.
- **Resource limits cover what the schema library reaches, and nothing
  else.** Over the schemas the library compiles for an operation (its schema
  and, transitively, what the references in them name), a schema is held to
  a nesting depth of 256, and a number the library reads (a comparison or
  count keyword's value, or one in `const` or `enum`) to the numeric limits of
  schema evaluation. A large number in `default`, in an annotation, in an
  unrelated extension, or in source content no longer leaves every operation
  unavailable and OBI-D-02 inconclusive, and a schema nested thousands of
  levels deep no longer takes seconds to compile. A finding about a limit
  names the offending value's location.
- **`ParseDocument` applies the document schema whatever numbers the
  document holds.** A number beyond the limits of schema evaluation kept the
  document schema from being applied, so a missing `operations` member went
  unreported. An OBI-D-01 violation is a `*ValidationError` like every other
  violation.
- **Input nested deeper than the decoder reads is inconclusive** (§10.5), not
  an OBI-D-01 violation.
- **A reached pattern Go's regexp cannot compile no longer stops
  compilation.** The graph is still resolved, so a graph that reaches
  outside the document is outside OBI-D-11 whatever patterns it holds; one
  that does not leaves the operation unavailable, as before.
- **Operation-schema conclusions are deterministic.** The compiled graph is
  inspected in full, and its first problem in sorted order reported, so a
  document no longer concludes differently from run to run.
- **Locations are percent-encoded as the schema library reads them.** A
  property name with a space escaped the dialect checks, and an operation key
  holding `%` compiled another operation's schema.
- **OBI-D-11 compiles a document once.** Every operation's schema shares one
  compilation of the document and its embedded resources; a document with
  hundreds of operations and `$id` schemas took seconds.
- **Validation keeps no state that grows with its input.** Only URIs that
  name a meta-schema the library carries are remembered.
- **OBI-D-17 is violated beside OBI-D-06 and OBI-D-07.** Its definition
  includes §5.2's dialect constraints.
- **OBI-D-05 stops at a schema resource's boundary.** A schema that declares
  its own `$id` is a schema resource whose references, nested `$id`s, and
  dynamic pair are its internal business, resolved per JSON Schema exactly as
  for an externally fetched schema (§7). A relative or malformed `$ref`
  inside one was an OBI-D-05 violation; OBI-D-05 now judges only the
  resource's own `$id`, which must be an absolute, well-formed URI
  (`https://[::1` is not). A same-document fragment whose pointer has an
  invalid escape (`#/$defs/~2`) is not a JSON Pointer: it violates OBI-D-05
  and no longer resolves for OBI-D-16.
- **`ParseDocument` refuses an unsupported version before applying the
  schema.** It judged a `0.3.0` document against the 0.2 document schema
  and reported it non-conformant; it now returns the `*VersionRefusalError`
  (OBI-T-04), as `ValidateDocument` already did. Its schema violations are
  built by the same rule checks as validation, so their text matches.
- **A version with surrounding whitespace is not SemVer.** `IsValidSemver`,
  `IsSupportedVersion`, and OBI-D-12 no longer trim, so `" 0.2.0"` violates
  OBI-D-12.
- **One defect is one finding.** Checks that restated the embedded document
  schema are gone, so OBI-D-02 has one owner. An `anyOf` or `oneOf` that no
  alternative satisfies is one finding at its own location that says what
  each alternative lacked; a source with neither `location` nor `content`
  was three findings.
- **Schema compilation never reads local files.** The schema backend's
  default loader read `file:` references from disk, so a document could make
  validation read the validating machine's files and a verdict could depend
  on that machine. Every external reference now leaves the schema graph
  unavailable (`*SchemaGraphUnavailableError`), as `http(s)` references
  already did (§7, OBI-T-16). The JSON Schema meta-schemas are built in and
  still resolve.
- **Operation contracts compile against the document's content, not its
  root.** The whole OBI was compiled as a JSON Schema, so its unknown members
  acted as schema keywords: a root `$defs` supplied embedded resources, and a
  root `"type": 5` made every operation's graph unavailable (OBI-T-02, §7).
  The schema library is now given the document without the root members
  named like a JSON Schema 2020-12 keyword, which it would read as keywords
  there (the OBI's own `dependencies` and `description` among them, so a
  dependency entry is never read as a schema). Every other member stays at
  the location it holds, so a same-document reference means what its author
  wrote. Every resource the document embeds by `$id`, nested ones included,
  resolves by it, one whose `$id` is a meta-schema's URI too, and sets the
  base of the locations inside it, so a same-document pointer into a
  resource's interior resolves the references there against that resource.
  An `$id` two schemas declare leaves only the graphs that reach it
  unavailable. Each compilation gives the document a URI unique to it, so no
  `$id` collides with the document itself.
- **Operation-contract validation needs a complete graph** (§5.2, OBI-T-16).
  `CompileOperationSchema`, `ValidateOperationInput`, and
  `ValidateOperationOutput` report the graph unavailable when what the
  schema library compiles reaches a resource outside the document other
  than a built-in meta-schema, a reference that does not resolve, a schema
  the 2020-12 meta-schemas refuse, another `$schema`, or a `$vocabulary`. An
  unreferenced definition stays outside the graph. A cycle of references
  that never advances into the value is unavailable wherever it sits, not a
  mismatch or a pass. They refuse an unsupported version
  (OBI-T-04), refuse to interpret a document declaring no valid version
  (OBI-D-12), and resolve an operation by key or alias (OBI-T-12). A value
  outside the JSON value domain (a Go struct, a `map[string]int`) is refused
  as such, not reported as a mismatch.
- **A name several operations carry resolves to none of them.** In a
  document violating OBI-D-04, `ResolveOperation` preferred a key match and
  otherwise picked an alias match at random (OBI-T-12).
- **The document rules no longer depend on typed decoding.** When the typed
  model could not decode a document, `ValidateDocument` reported every
  remaining rule inconclusive, OBI-D-02 and OBI-D-12 included although both
  were decided, and missed violations it could establish, such as an
  OBI-D-17 `"input": null` or an OBI-D-08 dangling operation. Every rule now
  judges the document's JSON, literally on the values present: a value
  that fails a rule's predicate violates it (an operation reference that is
  a number names no operation key), and one outside the rule's domain gives
  it nothing to judge. `Interface.Validate` judges the
  encoding of the host object the same way. A resource limit met while
  checking a rule leaves it inconclusive, never violated (§10.5).
- **OBI-D-05 follows RFC 3986's grammar.** A character screen plus `net/url`
  passed `#/a[0]` and a second `#` in a fragment, and refused a
  percent-encoded host. A URI-form reference is now checked against the
  URI-reference grammar; an empty location is relative in form. The
  named-transform `$ref` clause is now checked. `dependencies` subschemas,
  which the 2020-12 meta-schema describes and the backend applies, are
  walked like `definitions`.
- **OBI-D-16 covers absolute references into embedded resources.** An
  absolute `$ref` matching an embedded schema's `$id` is in the rule's
  scope; one that does not resolve within that resource was reported
  conformant. OBI-D-16 now judges every same-document fragment, one whose
  spelling OBI-D-05 refuses included, and a reference to an anchor two
  schemas declare is inconclusive. OBI-D-10 decodes a percent-encoded
  transform reference before resolving it.
- **An oversized version is refused.** A version whose numbers exceed a
  machine integer failed to parse and was interpreted under 0.2 rules;
  SemVer bounds no number, so versions now compare exactly at any size. The
  version is read and refused before OBI-D-01 judges the bytes, whenever a
  JSON decoder can read it (§10.1).
- **OBI-D-11 follows a fragment into an embedded resource.** An example
  behind `https://example.com/t#/$defs/S`, into a schema the document embeds
  by that `$id`, was left unchecked; it is now validated, as is one behind a
  plain-name anchor.
- **OBI-T-02 diagnoses the transform `$ref` object.** Its unknown members
  are now reported like those of every other OBI-defined object.
- **A document schema finding about a map key is located at the key**, not
  at the whole document.
### Changed

- **Operation schemas reach the schema library as a bundle, and core
  resolves every reference** (breaking, pre-1.0). The OBI document is no
  longer handed to the library whole, with root members named like schema
  keywords withheld. Core resolves each reference with the one resolver
  OBI-D-16 uses, and gives the library a JSON Schema 2020-12 bundle (§9.3)
  holding copies of the schemas an operation's graph uses; a resource that
  declares `$id` keeps it, and the library resolves within it as it does any
  schema. The answers that change:
  - **Strict 2020-12.** `dependencies`, `$recursiveRef`, and
    `$recursiveAnchor` constrain nothing, though the library would evaluate
    them in a 2020-12 schema. An `$id` or anchor inside `definitions` or
    `dependencies` declares nothing: an absolute reference to such an `$id`
    points outside the document, so its examples are outside OBI-D-11 and a
    document can go from non-conformant to conformant, and a plain-name
    reference to such an anchor violates OBI-D-16. OBI-D-05 and OBI-D-16 no
    longer judge references inside those entries (a relative `$ref`, a
    malformed or percent-encoded one, a relative `$id`, `$dynamicRef`,
    `$dynamicAnchor`, a fragment that resolves nowhere); OBI-D-06, OBI-D-07,
    and OBI-D-17 still follow the meta-schema into them.
  - **RFC 3986.** A reference under a base whose path is not hierarchical
    resolves as the RFC says: `b` against `urn:x:y` is `urn:b`, where the
    library's reading kept `urn:x:y`. A graph holding such a relative
    reference gets no verdict, since the library resolves it otherwise.
  - **References into the document root resolve,** whatever the member is
    named: `#/$defs/a` or `#/dependencies/d` names that location, and gets a
    verdict. A same-document fragment inside a resource whose `$id` resolves
    to no URI also gets one.
  - **No verdict** where the library would answer wrongly or the spec does
    not decide: a schema reached outside the schema positions that declares
    `$id`, `$anchor`, or `$dynamicAnchor` (only schema positions declare
    them, §7); a `$dynamicRef` applied to property names, which the library
    checks without the dynamic scope; a cycle of references that never
    advances, under `if` or `not` too, where the library answered as if it
    were an ordinary failure; and a graph holding a `$dynamicRef` in a
    document whose schemas declare `$dynamicAnchor` outside every resource
    (OBI-D-05).
  - **A resource is given to the library whole.** The library compiles a
    whole resource when any part of it is used, so a part of an `$id`
    resource the graph does not reach, referencing a resource outside the
    document or holding a number the library reads beyond the numeric
    limits, leaves the graph without a verdict. Core had read the first as
    reaching outside, and skipped the examples.
  - An `$id` that names a meta-schema the library carries names the schema
    the document embeds, as before.
- **The version API states the supported set and the version documents
  declare** (breaking, pre-1.0). `MinSupportedVersion`, `MaxTestedVersion`,
  and `SupportedRange` are removed: they named a tested range, which §8.1
  does not define, and did three jobs under one name. `SupportedVersions`
  (`0.2.x`) states the versions this SDK supports, and `IsSupportedVersion`
  still decides membership. `AuthoringVersion` (`0.2.0`) is the version a
  document written with this SDK declares: the lowest version sufficient for
  what the document model carries, as §8.1 asks of documents, where
  `MaxTestedVersion` would have moved with every tested patch. A prerelease
  is supported only when named explicitly (§8.1); it was inferred from the
  tested range, which would have admitted `0.2.1-rc.1` once the range reached
  0.2.1. None is named, so what the SDK accepts is unchanged, and a refusal
  of a version outside the line names the line (`0.2.x`).
- **The document model does not carry lone UTF-16 surrogates** (breaking,
  pre-1.0). A string escaping an isolated surrogate (`"\uD800"`) is RFC 8259
  JSON, so it breaks no document rule, but a Go string cannot hold it and
  encoding/json replaces it with U+FFFD. The model kept it through a private
  copy of encoding/json that stored it as WTF-8, which the JSON Schema
  library then counted as three characters, reporting false OBI-D-11
  violations. Core now decodes with encoding/json and detects the escape:
  decoding refuses such a document, `ParseDocument` refuses it naming where
  the string is, and `ValidateDocument` decides OBI-D-01 and leaves every
  other rule inconclusive. Member names are still compared exactly, so
  `"\uD800"` and `"\uFFFD"` are two names. The TypeScript SDK, whose strings
  are UTF-16, carries such strings; the difference is an accepted divergence.
- **`ValidationError` carries findings** (breaking, pre-1.0). `Problems
  []string` is replaced by `Findings []Finding`, each naming its rule and
  location; the message is unchanged apart from saying "non-conformant
  document". `ErrOperationNotFound` reads "no one operation is named", which
  covers an ambiguous name too, and a version refusal names the release line
  the SDK supports (0.2.x) rather than the tested version.
- **Validation takes the transform parser it is given** (breaking, pre-1.0).
  Core defines the two capabilities the specification names over the pinned
  transform language (§5.5), and carries neither: `TransformParser` decides
  whether an expression is in the language, and `TransformEvaluator`
  evaluates one with an input and the context bindings a binding
  specification defines (§5.5 clause 5). One implementation of the language
  usually provides both, and an application gives the same one to every
  layer that parses or evaluates transforms, so the expression validation
  accepts is the expression that runs. `Interface.Validate` and
  `ValidateDocument` take a `ValidateOptions`, whose `Transforms` field is a
  `TransformParser`. OBI-D-18 is decided by that parser; without one it is
  inconclusive at every expression, as §10.2 provides for a validator
  without a parser, so a document with transforms is
  conformance-undetermined rather than conformant. Core no longer imports
  the JSONata syntax package. `ErrTransformNoResult` marks an expression
  that yields no result (JSONata's undefined), and `ErrTransformUndecided`
  one that could not be decided (the implementation's own limits, not the
  expression): from `Parse` it leaves OBI-D-18 inconclusive rather than
  violated.
- **The JSON Schema library is an ordinary dependency** (behavior changes in
  rare cases). Core validated with a private, patched copy of
  `github.com/santhosh-tekuri/jsonschema/v6` v6.0.3; it now requires the
  published module and uses it as documented, and the copy, its patch, and
  the scripts that maintained them are gone. Where the library differs from
  what the patches did, the library's behavior stands: patterns use Go's
  `regexp`, so an ECMAScript-only pattern such as a lookahead leaves the
  graph unavailable; counts beyond the largest Go `int` are not
  corrected; and a document schema finding about a map key can name the
  wrong parent map, because v6.0.3 reuses that location's storage. Two
  library behaviors are corrected through its public options instead:
  `format` never rejects a value, in any draft (the library otherwise
  enforces it under draft-07 and earlier, which a reference to their
  meta-schemas reaches, with no option to stop), and the graph an operation
  schema reaches is walked by core itself, so a `then` or `else` no `if`
  selects counts, as §5.2 has it, though the library does not compile it. A
  number beyond the numeric limits of schema evaluation (4096 characters, an
  exponent within ±10000) never reaches the library, where v6.0.3
  dereferences nil: where a check cannot tell it from a stand-in it is
  checked as one, and otherwise that check reaches no verdict. The
  `github.com/dlclark/regexp2/v2` dependency is gone.

- **The document model is exact** (breaking, pre-1.0). Decoding matched
  member names without regard to case, so `"OPERATION"` beside `"operation"`
  replaced the binding's operation, and it accepted duplicate member names.
  Re-encoding a decoded document dropped members whose value is a Go zero value
  (`deprecated: false`, empty strings, empty arrays and maps) and every
  member of a transform's `$ref` object besides `$ref`, extensions included.
  So `Interface.Validate()` missed violations its bytes carry, such as a
  present empty `location`, and a present empty `selector` became an absent
  one, which a binding specification can give a different meaning (§5.3).
  Now an optional member is absent exactly when its Go value is nil:
  optional strings and booleans are pointers (`Name`, `Version`,
  `Description`, `Deprecated`, `Source.Location`, `BindingEntry.Selector`),
  set with `Present` and read with `Value` where absence and the zero value
  mean the same; optional collections encode `omitzero`, so nil is absent
  and empty is present. Example values are `json.RawMessage`, where `null` is
  a present value, like `Source.Content`; `InputPresent`, `OutputPresent`,
  `HasInput`, `HasOutput`, and `Source.ContentPresent` are gone.
  `BindingEntry.Preference` is an exact `*int64`. `TransformOrRef` is a
  sealed union of `InlineTransform` and `*TransformReference`, which keeps
  the `$ref` object's other members, so `{"$ref": ""}` stays an object and
  no transform holds both forms; `IsRef` is gone. Members are matched by
  exact name, and a case variant is an unknown member. A document the model
  cannot carry exactly fails decoding instead of being altered: invalid
  UTF-8, a duplicate member name, JSON null where null is not a value
  (members, map entries other than `schemas`, and array elements), a missing required string
  member, or a preference that is not an integer number in range (`"7"` is
  not). A typed field alone states its member: an `Unknown` or `Extensions`
  entry of the same name is never encoded. `ValidateDocument` still judges
  such a document in full.
- **Finding and diagnostic paths are JSON Pointers** (breaking, pre-1.0).
  `Finding.Path` and `Diagnostic.Path` are RFC 6901 pointers into the
  document, such as `/bindings/createTask/operation`; the empty pointer is
  the whole document, and a missing member is reported at the object that
  lacks it. Schema-derived findings (OBI-D-02, OBI-D-11, OBI-D-17) now carry
  the location the schema check reports instead of an empty path, down to
  the offending keyword or example member. Key and alias findings point at
  the entry. `ValidationError` problems use the same paths.
- **`Interface.Validate()` and `ValidateDocument(data)` return a
  `ValidationReport` beside their error** (breaking, pre-1.0). The error is
  a `*ValidationError` listing every violation established, now tagged with
  the rule each one breaks (several schema-level checks carried no rule
  identifier), so `if _, err := iface.Validate(); err != nil` remains the
  gate before acting on a document. A nil error is documented as "no
  violation established", not conformance; the report's `Conclusion` says
  which. `ValidateDocument` validates the exact input bytes instead of
  parsing first, so input that is not a JSON document is reported as a
  violation of OBI-D-01 rather than returned as a parse error, and it returns
  the decoded document whenever the document model can carry it exactly. A version outside the
  supported set is a `*VersionRefusalError` from `Validate`,
  `ValidateDocument`, and `ParseDocument` alike, returned with no report,
  because a refused document is not interpreted under this version's rules
  at all. `ValidationError` reads "non-conformant interface" instead of
  "invalid interface".
- **OBI-D-11 checks exactly the examples in its scope.** Example validation
  used to abstain for every operation as soon as any schema in the document
  referenced an external resource, hiding in-scope mismatches. It now walks
  each operation schema's reachable graph, following same-document pointers
  and references into schema resources the document embeds by `$id`, and
  leaves out only positions whose graph reaches an external resource. A
  schema that cannot be compiled is reported as inconclusive rather than as a
  violation of the example rule, since failing to compile is not evidence
  that an example is wrong.

- **The binding entry's target member is `selector`, not `ref`** (breaking;
  the ratified pre-launch rename, executed with no aliases or deprecation
  shims). The OBI member `bindings[*].ref` is now `bindings[*].selector`,
  and `BindingEntry.Selector` follows. JSON Schema
  `$ref` handling is deliberately untouched everywhere: the rename covers the
  binding-target-selector concept, never JSON References.
- **Core OBIs now carry named operation dependencies.** `Interface` adds the
  optional `Dependencies` map of `DependencyEntry` values. Each entry names an
  exact canonical local operation key and may constrain acceptable exact,
  opaque binding specifications with an unordered non-empty `BindingSpecs`
  any-of list. Lossless JSON, the document schema, and the core corpus's
  OBI-D-19 fixtures cover the new shape. A dependency is a consumption declaration, not a provider
  address, binding, liveness/readiness claim, or routing policy.

- **OBI-D-05 literal form is enforced.** A percent-encoded same-document
  fragment (`#/schemas/T%61sk`) now fails validation at OBI positions, and the
  OBI-D-16 resolver no longer percent-decodes — the non-conformant spelling is
  never honored.

- **`IsSupportedVersion` now answers OBI-T-04 acceptance (patch-lenient within a
  supported minor line), matching `Validate`/`ParseDocument`; previously it was
  the strict tested-range check.** A 0.2.0 SDK now reports `true` for `0.2.1`,
  `0.2.99`, etc. — the versions `Validate`/`ParseDocument` actually process —
  and continues to report `false` for a different major, a pre-1.0 different
  minor, and unsupported prereleases. The oracle now shares the single refusal
  predicate the validation paths use, so it cannot drift from them. The
  tested-range constants were later removed; see the `SupportedVersions`
  entry.

- **`Interface.ValidateInterface()` renamed to `Interface.Validate()`.** The package-name-flavored verb was redundant when the receiver was already an `Interface`. `ValidateDocument(data)` (which parses then validates) keeps its name.

- **Validation options trimmed.** `WithExampleValidation` and `WithRequireSupportedVersion` removed: example schema validation (OBI-D-15 then; OBI-D-11 under the current numbering) and the supported-version check (OBI-T-04) are now unconditional in `Validate()`. `WithRejectUnknownTypedFields` is the only remaining option.

### Removed

- **Everything outside the core** (breaking, pre-1.0). The module now carries
  only what the core specification defines: the root package and the two
  internal packages it uses (`internal/jsonpointer`,
  `internal/schemacompiler`). What 0.1.0 carried beside the core in the root
  package is gone: execution (`OperationExecutor`, `NewOperationExecutor`,
  `BindingExecutor`, `CombineExecutors`, and their input, output, and error
  types), `InterfaceClient`, synthesis (`InterfaceCreator`,
  `CombineCreators`), interface compatibility checking
  (`CheckInterfaceCompatibility`, `IsOBInterface`), context stores
  (`ContextStore`, `NewMemoryStore`), the error codes, and the HTTP, key, and
  content helpers; so are the eight `formats/*` modules. Each layer is to be
  rebuilt on this core from its own authority. The code as it stood before
  this cut is preserved on the `legacy/pre-core-rebuild` branch at `aceb788`,
  where every module built and passed; the published `v0.1.0` and
  `formats/*/v0.1.0` tags are unaffected. The root module no longer requires
  `github.com/openbindings/jsonata/go` or `golang.org/x/net`.

- **`WithRejectUnknownTypedFields` and the exported `ValidateOption`**
  (breaking, pre-1.0). OBI-T-02 requires every processor to ignore unknown
  fields; the option turned them into rejections. Unknown non-`x-` fields
  are now always surfaced as OBI-T-02 diagnostics in a `ValidationReport`,
  which is what the rule asks for, and never affect validation.
  The option is gone; `ValidateOptions` carries only capabilities validation
  does not have itself.

- **The `security` surface, per spec 0.2.0**: the OBI `security` section
  (`Interface.Security`), `BindingEntry.Security`, `SecurityMethod`,
  `ResolveSecurity`, and the security-reference validation
  (`security.go`/`security_test.go` deleted). Credentials are never part of an
  OBI document; they are runtime context.

- **Conformance rule IDs corrected** to match the spec: `OBI-D-16` → `OBI-D-13`
  (SemVer `openbindings` field; `OBI-D-12` under the current numbering),
  `OBI-T-13` → `OBI-T-12` (operation-name resolution).

### Fixed

- **Operation-boundary schema validation now preserves the OBI document as
  the same-document reference root.** Input, output, and example validation
  compile schemas at their canonical `#/operations/...` addresses instead of
  extracting them into a synthetic root, so operation-local recursive
  `$defs`, cross-operation pointers, escaped operation keys, named schemas,
  and embedded absolute `$id` resources retain their JSON Schema meaning.
  `ValidateOperationInput` and `ValidateOperationOutput` expose the same
  interface-aware boundary to applications that drive binding invokers
  directly.

- **`Validate` accepts leading-digit identifiers (`2fa.verify`) per the
  committed OBI-D-03 grammar**, so `ParseDocument` and `Validate` agree again.

- **Schema validation failure rendering.** `splitSchemaError` and
  `collectValidationFailures` formatted jsonschema/v6 `ErrorKind` leaves with
  `%v`, printing the raw struct (`&{[customer]}` for a missing required
  property) instead of the kind's localized message (`missing property
  'customer'`). Leaves now render via `ErrorKind.LocalizedString`.

### Added

- **Document validation reports its conformance conclusion, not just its
  violations.** The `ValidationReport` from `Interface.Validate()` and
  `ValidateDocument(data)` uses the core's §10.5 vocabulary: per-rule
  `Evidence` for every document rule (satisfied, violated, or inconclusive),
  the `Violated` and `Inconclusive` rule identifiers OBI-T-17 requires,
  located `Findings` for each violation and each check this SDK could not
  decide, OBI-T-02 `Diagnostics`, and a `Conclusion` of conformant,
  non-conformant, or conformance-undetermined. Previously a check the SDK
  could not decide passed silently, so a nil error from `Validate` was
  indistinguishable from conformance; a tool reporting it as such violated
  OBI-T-17. The cases now recorded as inconclusive rather than passed:
  OBI-D-13 for any document with bindings (only the governing binding
  specification decides it), an OBI-D-05 source location that is
  colon-bearing but not a well-formed URI (`10.0.0.1:443`), OBI-D-11
  examples whose schema could not be compiled or whose graph's reach could
  not be established, and OBI-D-01 on a host object, which no longer carries
  the exact input bytes (`ValidateDocument` decides it). `ValidateDocument`
  decides OBI-D-12 from the raw document, so a non-string version is still
  identified. `DocumentRules()` lists the rules a report covers; a caller
  holding evidence the SDK cannot produce, such as a binding specification
  implementation deciding OBI-D-13, can amend `Evidence` and call
  `ConcludeConformance` again.

- **CI corpus gating (`OB_CORPUS_REQUIRED`)**: CI checks out the spec's
  conformance corpus, and every corpus locator fails loudly when a corpus is
  required and absent; local skip-if-absent behavior is unchanged.

- **Conformance-runner `requiresSupports` annotation**: the corpus harness
  honors the per-test `requiresSupports: "X.Y.Z"` annotation — the test is
  administered only when this SDK's OBI-T-04 version-acceptance predicate
  (`IsSupportedVersion`) accepts X.Y.Z; otherwise it is skipped and reported
  as a skip, never a failure, alongside the `requiresMinSupported` gate. A
  corpus without the annotation runs unchanged.

## 0.1.0 — 2026-03-31

> The date reflects the content freeze; the v0.1.0 tags were created 2026-04-15. From 0.2.0 on, entry dates are tag dates.

Initial public release.

- Core types for OpenBindings interface documents with lossless JSON round-tripping
- Interface validation with strict mode for unknown fields and format token validation
- Schema compatibility checking (Profile v0.1) with covariant/contravariant directionality and diagnostic reasons
- InterfaceClient for OBI resolution via URL, well-known discovery, and synthesis
- OperationExecutor with format token range matching (caret, exact, versionless)
- Unified stream execution model — every operation returns `<-chan StreamEvent`
- BindingKey support for explicit binding selection bypassing the default selector
- Context store with scheme-agnostic key normalization (`host[:port]`)
- Transform pipeline (input + output) with per-event error propagation
- Security types (`SecurityMethod`, `Interface.Security`, `BindingEntry.Security`) for declaring auth methods on bindings
- `ResolveSecurity` helper for interactive credential resolution via `PlatformCallbacks`
- Standard error codes (`errcodes.go`) for protocol-agnostic error handling
- HTTP error helpers (`HTTPErrorOutput`, `httpErrorCode`) for mapping HTTP status codes to error codes
- Security method pass-through in `OperationExecutor` via `BindingExecutionInput.Security`
- Subpackages: `canonicaljson` (RFC 8785), `formattoken` (semver range matching), `schemaprofile` (Profile v0.1)
