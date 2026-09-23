# Changelog

## 0.2.0 (working draft)

> A 0.1.1 patch release was prepared 2026-04 but never tagged or published; its entries are folded into this section.

### Fixed

- **A document with a large number in its aliases no longer crashes
  validation.** An operation's `aliases` or a dependency's `bindingSpecs`
  holding more than 20 items, one of them a number beyond the numeric limits
  of schema evaluation, made `ParseDocument` and `ValidateDocument` panic
  inside the schema library, whose `uniqueItems` compares items as numbers.
  Every member the document schema does numeric work on is now checked
  (`preference`, `aliases`, `bindingSpecs`), and a test holds that list to
  the embedded schema.
- **A number beyond the limits in one member no longer hides the rest of
  OBI-D-02.** The member is set aside and the rest of the document is still
  checked. A `preference` is decided exactly, however it is spelled: `1e10001`
  violates its range, and `1.` followed by 5,000 zeros is 1. An array holding
  such a number is inconclusive at its location. The typed model decodes a
  preference with the same check, whose work no longer grows with the
  exponent.
- **An unsupported version is refused however deeply the input nests.** The
  declared version is read a token at a time, so input nested past the
  decoder's 10,000 levels is refused under OBI-T-04 rather than reported
  conformance-undetermined.
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
  else.** OBI-D-02 holds only the members the document schema does numeric
  work on to the numeric limits of schema evaluation. An operation's schema is held to them, and to a nesting depth
  of 256, over the values the library can reach from it: its schema and,
  transitively, what the references in them name. A large number in an
  unrelated extension or in source content no longer leaves every operation
  unavailable and OBI-D-02 inconclusive, and a schema nested thousands of
  levels deep no longer takes seconds to compile. A finding about a limit
  names the offending value's location.
- **`ParseDocument` refuses what it cannot check.** A document holding a
  number beyond the limits parsed without its document schema applied, so a
  missing `operations` member went unreported. It now returns an error when
  the document schema could not be applied. An OBI-D-01 violation is a
  `*ValidationError` like every other violation.
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
  mismatch or a pass. A relative `$id` at an OBI position has no base and
  leaves the graph unavailable. They refuse an unsupported version
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
- **A present empty `selector` no longer runs a Usage root command.** An
  absent selector addresses the root command and USAGE-D-03 refuses `""`,
  but invocation received both as `""` and ran the root.
- **Synthesized documents state no empty optional collection.** The exact
  model made synthesizers emit the empty `bindings` and `dependencies` maps
  their skeletons start with; `synthesize.FinalizeSynthesis` now omits empty
  optional collections. A dependency's `bindingSpecs` is left as authored.

- **`canonicaljson` refuses numbers it cannot carry exactly.** A JSON number
  whose exact value is not representable in IEEE 754 binary64 (for example
  `9007199254740993`, or a decimal with more precision than a double holds)
  has no RFC 8785 serialization; `Marshal` now returns a
  `*NumberNotRepresentableError` instead of silently rounding, per Appendix A
  of the core specification. Spelling differences that carry the same value
  (`1.10`, `1e2`) still canonicalize. Consumers that hash documents through
  this package now fail loudly on such input rather than attesting rounded
  data.

- **Preflight failures remain visible.** OpenAPI now reports failed required
  description loads, edition/selector checks and analysis during preflight,
  including cancellation, instead of returning a successful unknown result.

- **Context matching keeps the complete challenge's scheme identities.**
  `MatchContextAlternative` exposes the same first-match decision used by
  satisfaction and scoping so applications can retain it while applying a
  resolution. Storage eligibility filtering no longer makes ambiguous flat
  credentials appear to identify a single named scheme. Store-backed resolution
  also preserves alternative order across storage keys while caching reads.
- **Basic credential checks agree across representations and extraction.**
  Both username and password must be strings; either may be explicitly empty
  when the other is nonempty. Missing or wrongly typed members no longer pass
  the flat or extraction paths when the named representation would reject them.
- **Cancellation also reaches preflight and its context resolver.** Cancelling
  an invocation after rejected input now cancels cooperative preflight and
  resolution work, as well as binding execution.

- **Named credential scoping preserves usable fallbacks.** Empty or malformed
  named bearer, basic or OAuth credentials no longer suppress a valid flat
  credential during `ScopeContext`. Admission shares the checks used to decide
  which representation satisfies the challenge; valid named credentials keep
  their precedence.

- **Caller-owned context recovery example handles either challenge timing.**
  It resolves challenges returned by a write or the output reader, reads the
  outcome after input closure, retires every attempt, and merges scoped
  resolution into existing context without discarding unrelated caller fields.
  Requested configuration values and credentials are replaced whole, preserving
  siblings while retiring stale credential aliases. Executable coverage also
  checks ordinary failures, replacement boundaries and the single-redo limit.

- **Output handoffs after termination skip capture.** A cancelled, completed or
  failed invocation returns its existing outcome before invoking the submitted
  value's codec. Accepted outputs and detached terminal details remain readable.

- **Usage hook-table machine lane keeps exact JSON values.** `HookTable.Hooks()`
  decoded a JSON machine lane with `encoding/json` into `any`, collapsing every
  number to float64 (2^53+1 and 1e400 could not survive a CLI round trip). The
  decoder now uses the SDK's exact `jsonvalue` decode, so numbers keep their
  token and trailing content after the value is refused, like every other
  SDK decode boundary.

### Changed

- **Core's tests use only core.** The operation-contract witnesses from the
  interfaces repository's comparison corpus are checked in `schemaprofile`,
  which owns that profile, and the `canonicaljson` example is in
  `canonicaljson`. The core package documentation no longer lists the
  packages built on it.
- **Core depends on no other package of the SDK.** It used `jsonvalue` for
  four helpers, and so compiled that package's invocation, schema-comparison
  and binding-specification helpers, `internal/value`, `internal/jstring`,
  and the private copy of encoding/json. Core now keeps its number checks
  beside its use of the JSON Schema library and encodes with encoding/json:
  a host object holding an empty `json.Number` is validated as the `0`
  `json.Marshal` writes for it.
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
- **Validation takes the transform engine it is given** (breaking, pre-1.0).
  `Interface.Validate` and `ValidateDocument` take a `ValidateOptions`, whose
  `Transforms` field is a `TransformEngine`: an implementation of the pinned
  transform language (§5.5) that parses expressions and evaluates them with
  an input and named variables. The SDK carries none. An application
  chooses one engine and gives the same engine to every layer that parses or
  evaluates transforms, so the expression validation accepts is the
  expression that runs. OBI-D-18 is decided by that engine; without one it
  is inconclusive at every expression, as §10.2 provides for a validator
  without a parser, so a document with transforms is
  conformance-undetermined rather than conformant. Core no longer imports
  the JSONata syntax package. `ErrTransformNoResult` marks an expression
  that yields no result (JSONata's undefined), and `ErrTransformUndecided`
  an engine that could not decide (its own limits, not the expression): from
  `Parse` it leaves OBI-D-18 inconclusive rather than violated.
- **The JSON Schema library is an ordinary dependency** (behavior changes in
  rare cases). Core validated with a private, patched copy of
  `github.com/santhosh-tekuri/jsonschema/v6` v6.0.3; it now requires the
  published module and uses it as documented, and the copy, its patch, and
  the scripts that maintained them are gone. Where the library differs from
  what the patches did, the library's behavior stands: patterns use Go's
  `regexp`, so an ECMAScript-only pattern such as a lookahead leaves the
  graph unavailable; the pre-2019 `dependencies` and `$recursiveRef` are
  evaluated in 2020-12 schemas; counts beyond the largest Go `int` are not
  corrected; and a document schema finding about a map key can name the
  wrong parent map, because v6.0.3 reuses that location's storage. Two
  library behaviors are corrected through its public options instead:
  `format` never rejects a value, in any draft (the library otherwise
  enforces it under draft-07 and earlier, which a reference to their
  meta-schemas reaches, with no option to stop), and the graph an operation
  schema reaches is walked by core itself, so a `then` or `else` no `if`
  selects counts, as §5.2 has it, though the library does not compile it. A number beyond the numeric limits of schema
  evaluation (4096 characters, an exponent within ±10000) in a value, a
  schema, or the document leaves that check without a verdict instead of
  reaching the library, where v6.0.3 dereferences nil. The
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
  such a document in full. `PreparedBindingDescriptor.Selector` keeps
  selector presence; compare descriptors with the new `Equal`.
- **Invocation carries selector presence** (breaking, pre-1.0).
  `invoke.BindingInvocationArgs.Selector`, `InvokeSite.Selector`, and the
  realization records' `Selector` are `*string`, nil when the binding has no
  selector, and each format invoker applies its binding specification's
  rule for both cases. The JSON encoding of `BindingInvocationArgs` is the
  binding-invoker (0.1) contract's input, which requires a selector string,
  so it writes an absent selector as `""`. The interface-synthesizer (0.2)
  coverage and source-inspector (0.1) target records require one too;
  `synthesize.ContractSelector` is that projection. `BindingInvocationArgs`
  gains `HookSite`, the consultation site the format invokers each built.
  `PreparedDependencyDescriptor.BindingSpecsPresent` is gone: a present
  `bindingSpecs` holds at least one identifier (§5.6), so nil is absence.
  `httpdiscovery.VersionRefusalError` is gone: discovery reports a refused
  version with the core's `*openbindings.VersionRefusalError`.

- **Finding and diagnostic paths are JSON Pointers** (breaking, pre-1.0).
  `Finding.Path` and `Diagnostic.Path` are RFC 6901 pointers into the
  document, such as `/bindings/createTask/operation`; the empty pointer is
  the whole document, and a missing member is reported at the object that
  lacks it. Schema-derived findings (OBI-D-02, OBI-D-11, OBI-D-17) now carry
  the location the schema check reports instead of an empty path, down to
  the offending keyword or example member. Key and alias findings point at
  the entry. `ValidationError` problems use the same paths.
- **Standalone schema validation moved to `schemavalidate`** (breaking,
  pre-1.0). `ValidateAgainstSchema` took a pool of named schemas and
  rewrote `#/schemas/X` into the schema's own `$defs`, where a same-named
  local entry won, so it could validate a value against the wrong schema.
  `schemavalidate.Validate(value, schema)` takes only the schema, which is
  its own resolution root as JSON Schema defines. Validating against a
  standalone schema is not a Core capability, so it lives outside the root
  package. A schema at a position of an OBI resolves against the whole
  document (§7):
  `ValidateOperationInput` and `ValidateOperationOutput` do that. They and
  `CompileOperationSchema` now return a plain error when there is nothing to
  validate against (no such operation, or no schema at that position),
  distinct from `*SchemaGraphUnavailableError`. `SchemaValidationError`
  exposes its `Problems`, each a `SchemaProblem` with the JSON Pointer path
  into the value and the message, and its `Cause`.
- **Some root exports without a Core role are gone** (breaking, pre-1.0).
  `FormatValidationErrors` had no callers; `IsOBInterface`, a shape probe
  for fetched responses, is now `acquire.LooksLikeOBI`, beside the
  retrieval it serves;
  `PreparedInterface.Prepared` returned its receiver;
  `synthesize.RepresentedCoverageEntries` had no callers and wrote an
  absent selector as an empty `sourceRef`, which the interface-synthesizer
  contract refuses; and
  `AllOperationIdentifiers` had no callers; `ErrDependencyNotFound` moved
  to `invoke`, which returns it; and
  `IsUnsupportedPrerelease`, one step of the OBI-T-04 refusal, is private,
  `IsSupportedVersion` being the refusal's oracle.

- **Non-Core helpers moved out of the Go root package.** Binding
  implementation support types and exact-match checking moved to
  `bindingsupport`, and generic JSON helpers moved to `jsonvalue`. Import those
  packages for the moved APIs. URI location utilities moved to
  `internal/location` for SDK use and no longer have a public replacement;
  external callers must supply their own URI handling. This is a Go source
  break for direct callers, with no document or runtime semantics changed.
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

- **A name or alias correspondence is the provider's compatibility claim;
  composition only sets a candidate aside on a proven contradiction.** The
  reference composition policy treats a profile verdict of `indeterminate`
  (a differing keyword the comparison profile cannot read) as the claim
  standing: the provider stays eligible, ranks with compatible candidates,
  and its evidence rides the resolved route. `contract_indeterminate` is no
  longer an assessment code; `contract_incompatible` remains one. The
  composition corpus case `SCOMP-F04-indeterminate-keyword` now expects
  `available`. Undecidable compatibility issues are classified explicitly:
  `compare.CompatibilityIssue.Undecidable` is set for the profile's
  outside-profile finding, an external `$ref` the SDK declines to fetch, and
  a schema that does not normalize (detail text unchanged), and consumers
  decide on the field rather than on the `schema check failed:` prefix. The
  deprecated `MatchOperationRequirement` family carries undecidable issues
  on `OperationMatch.Issues` as evidence instead of excluding the candidate;
  only a proven contradiction, or a preflight that reports a resolution
  fact (`ERR_OPERATION_NOT_FOUND`, `ERR_BINDING_NOT_FOUND`,
  `ERR_BINDING_SELECTION_REQUIRED`, `ERR_UNKNOWN_SOURCE`), excludes, and
  any other preflight error leaves the match with no known context
  requirements. A preflight error no longer stops ordinary invocation: the
  binding could not answer and predicts nothing, so the one attempt runs as
  if preflight had reported nothing and the outcome is the attempt's
  (`ERR_BINDING_NOT_FOUND` and cancellation still end the invocation).

- **The schema-comparison profile decides identity first and fails closed
  at comparison time** (Schema Comparison Profile `OB-2020-12`, amended
  2026-09-22). `schemaprofile.Normalizer.Normalize` no longer refuses a
  keyword outside the profile: it is retained verbatim and marks its
  position, and an `allOf` whose siblings or branches carry one is retained
  unmerged (branches normalized individually, authored order kept) instead
  of refused. `InputCompatible`/`OutputCompatible` now run the identity rule
  at every position before any other rule: structurally identical
  normalized sub-schemas (`EqualNormalizedSchemas`, which now compares a
  retained `allOf` branch by branch) are compatible in both directions
  whatever keywords they carry, and the walk does not descend. Only a
  differing position marked outside the profile on either side is
  indeterminate, reported as the same `*OutsideProfileError` (path and
  keyword) the normalization-time refusal produced; every other position
  runs the existing rules unchanged, with byte-identical reasons. Identical
  mode compares retained keywords as ordinary values, so a difference at
  one is incompatible, never indeterminate. `compare.CheckInterfaceCompatibility`
  and `CheckOperationCompatibility` inherit this: identical contracts using
  `pattern` or any other outside-profile keyword report no issue.

- **The operation is named preflight and its documented contract is the
  signal contract** (the preflight signal contract proposal, 2026-09-21).
  `invoke.BindingPreparer` is `invoke.BindingPreflighter`;
  `OperationInvoker.PrepareOperation` and `.PrepareBinding` are
  `PreflightOperation` and `PreflightBinding`;
  `CompiledBindingInvoker.PrepareBinding` is `PreflightBinding`;
  `OperationMatch.Prepare` is `OperationMatch.Preflight`;
  `sdk.Runtime.PrepareOperation` is `PreflightOperation`; `PREPARATION.md` is
  `PREFLIGHT.md`. The static compilation family (`PrepareInterface`,
  `PrepareProvider`, `Prepared*`) is unchanged. The result is advisory: it may
  omit requirements, nil is always conformant, and the live `CONTEXT_REQUIRED`
  remains authoritative. Invocation never requires a prior preflight. Context
  supplied to preflight is supplied for that call alone. Preflight never
  dispatches the requested operation, consumes its input, emits its outputs,
  or spends an approval for it. An error means the binding could not answer
  and carries no prediction. See `PREFLIGHT.md`.

- **Invocation values now have snapshot ownership and finite per-value
  limits** (breaking, pre-1.0 minor-release change). Ordinary typed/local paths
  use private projection and checked construction instead of JSON text bridges.
  Mutable producer storage can be reused after an accepted handoff; handlers,
  evaluator callbacks and public readers receive detached values. Generic local
  reference identity is no longer preserved. Bytes retain exact typed recovery
  with Base64 logical meaning. Every single admitted, delivered, constructed or
  exported value is bounded by `ValueLimits.MaxValueUnits` and `MaxDepth`; a
  value over the allowance ends the invocation with `ERR_RUNTIME`, and accepted
  output drains before that failure. Handoff queues are bounded and apply
  blocking backpressure, but do not establish an aggregate memory bound:
  concurrent pending handoffs, pipeline values, scratch and terminal data also
  retain memory. Binding-specific buffering remains the binding's concern.
- **A live `CONTEXT_REQUIRED` now ends the invocation instead of being
  replayed** (breaking, pre-1.0 minor-release change; the context-challenge
  replay removal ruling, 2026-09-21). The operation invoker still resolves
  requirements a binding states before the first attempt (`PreflightBinding`)
  through its `ContextResolver` and starts the one attempt with the merged
  context. A `CONTEXT_REQUIRED` raised during the attempt surfaces as the
  terminal error with its `ContextRequiredDetails` intact, whether or not any
  input was forwarded or any output was produced; the resolver is not consulted
  for it and no second attempt is started. The replay log, retry window and
  retry cap are gone. A `Write` the SDK accepted is accepted into exactly one
  attempt. Callers that relied on the invisible redo add their own loop around
  `Invoke` (see the README's context section); `StoreContextResolver` is
  unaffected. `OutputStream.Stop` is exactly `Cancel` again.
- **Value configuration and export are explicit.** `invoke.ValueLimits`, runtime
  and provider options, per-call `WithValueLimits`, low-level
  `WithInvocationValueLimits`, local `ValueLimitError` causes and
  `ValueConversionError` describe the new finite boundary. A failed output
  conversion consumes only that result. `jsonvalue.MarshalWithOptions` supports
  bounded, requested JSON display/export. Evaluator injection remains required.
  See `INVOCATION_VALUES.md` and `VALUE_MIGRATION_QUALIFICATION.md`.


- **The SDK can now prepare immutable provider revisions and expose bounded,
  process-local operation-validation diagnostics.** `Runtime` performs exact,
  opaque binding-capability checks and prepares providers from raw or already
  prepared interfaces. Optional diagnostic collectors identify only the input
  or output phase and safe contract locations; they never alter portable error
  codes/data or retain rejected values, protocol facts, or credentials.

- **Post-dispatch decode and response-interpretation failures now surface as
  generic `ERR_EXECUTION_FAILED`, never `ERR_RESPONSE_ERROR` or
  `ERR_PROTOCOL`** (breaking; the error-code ownership ruling, 2026-08-31).
  Codes carry only what their owning interface licenses — dispatch state and
  boundary facts, never cause or protocol category — so the OpenAPI
  adapter's engine-error bridge, the invoke decode-hook seam, and the usage
  builtin decode all collapse those cause refinements at the invocation
  surface. Cause detail stays on the wrapped error and in diagnostics. The
  bridge now maps every standard engine spelling deliberately; only authored
  extension codes pass through. Pre-dispatch refusal codes
  (`ERR_SOURCE_LOAD_FAILED`, `ERR_SELECTOR_NOT_FOUND`, …) are unchanged: they
  refine `ERR_REFUSED`'s no-side-effect boundary fact, which codes may carry.

- **The binding entry's target member is `selector`, not `ref`** (breaking;
  the ratified pre-launch rename, executed with no aliases or deprecation
  shims). The OBI member `bindings[*].ref` is now `bindings[*].selector`,
  and every public symbol naming that concept follows: `BindingEntry.Selector`,
  `invoke.BindingInvocationArgs.Selector`, `invoke.InvokeSite.Selector`,
  `synthesize.BindableTarget.Selector` (wire `selector`),
  `synthesize.SynthesisCoverageEntry.BindingSelector` (wire `bindingSelector`,
  synthesis-scenario `bindingSelector` likewise), and the SDK implementation
  error codes `invoke.ErrCodeInvalidSelector` (`ERR_INVALID_SELECTOR`) and
  `invoke.ErrCodeSelectorNotFound` (`ERR_SELECTOR_NOT_FOUND`). JSON Schema
  `$ref` handling is deliberately untouched everywhere: the rename covers the
  binding-target-selector concept, never JSON References.
- **Core OBIs now carry named operation dependencies.** `Interface` adds the
  optional `Dependencies` map of `DependencyEntry` values. Each entry names an
  exact canonical local operation key and may constrain acceptable exact,
  opaque binding specifications with an unordered non-empty `BindingSpecs`
  any-of list. Lossless JSON, strict unknown-field
  validation, the derived schema, and the complete OBI-D-19 Core corpus cover
  the new shape. A dependency is a consumption declaration, not a provider
  address, binding, liveness/readiness claim, or routing policy.

- **Named OBI dependencies now have a prepared composition runtime.**
  `PreparedInterface`, `PreparedProvider`, `CompositionSession`, generated
  `DependencySignatures`, and opaque retained routes separate static closure
  from live preflight and invocation. The versioned reference policy reports
  provider and realization ambiguity separately and preserves exact or
  tri-state compatibility evidence. Generic JSON-domain values use native container shapes with snapshot ownership. The older operation-requirement family is transitional.

- **config.value requirements carry an engine-asserted `schema` instead of
  `choices`** (breaking; the 2026-08-20 working-draft amendment of the
  binding-invoker contract — one mechanism, no sugar).
  `invoke.NewConfigValueRequirement` takes a `map[string]any` JSON Schema
  (nil = absent = unconstrained); a present `schema` member must be a JSON
  object (not metaschema-validated) or the challenge is invalid; satisfaction
  validates the selected configuration value against the schema when one is
  carried (an `enum` member is a closed admissible set). Engines emit
  `{"enum": […]}` exactly where they previously emitted `choices` — where the
  admissible set is already computed at the emission site — and stay absent
  otherwise.

- **`invoke.StoreContextResolver` keys config-only alternatives by the exact
  asserted target** (the 2026-08-19 context-scope model): an alternative
  consisting solely of config.value requirements fetches under the verbatim
  challenge target — the engine-asserted artifact-bound scope — while any
  credential-bearing alternative keeps the endpoint-normalized
  (`NormalizeEndpoint`) convention. Endpoint normalization would conflate a
  canonicalized source URL with its origin, letting one artifact's
  configuration answers resolve a same-host sibling's challenge. The asyncapi
  engine's challenge target accordingly falls back resolved server URL →
  artifact host hint → canonicalized source location (empty for a
  content-only source, which asserts nothing).

- **The SDK is layered into core plus `invoke`, `synthesize`, and `compare`
  sub-packages** (breaking; import-path changes only, no renames or behavior
  changes). Placement follows the authority source: what `openbindings.md`
  defines stays in the root package (document model, validation, operation
  resolution, boundary schema validation, versions/constants); the
  binding-invoker/operation-invoker realization lives in `invoke`; the
  interface-synthesizer/source-inspector realization — including
  `FetchInterface` and the synthesis-scenarios runner, now at
  `synthesize/synthesisscenarios` — lives in `synthesize`; interface and
  operation compatibility checking lives in `compare`. The root package
  imports none of the three. The former unexported `compileOperationSchema`
  is exported as `CompileOperationSchema` (core) for the invocation runtime;
  `IsOBInterface`, `IsHTTPURL`, and `BindingSpecInfo` are seated in core.

- **The portable synthesis corpus runs at
  `openbindings.binding-spec-synthesis-scenarios@4`** (revved from @3 for the
  binding-identity member rename `bindingRef` → `bindingSelector`; `sourceRef`
  is unchanged). `synthesisscenarios.Verify`
  takes a `SynthesizerFactory` rather than one synthesizer, because a scenario
  may now declare companion documents that the family adapter has to serve
  through its own artifact-resolver seam. `synthesisscenarios.Fixed` adapts a
  single synthesizer for the six families whose corpus sources are
  self-contained and **refuses** a scenario declaring `resources` rather than
  running it against a resolver that would never see them; `fixedSynthesizer`
  in `@openbindings/sdk` is its twin, with the same message and the same
  placement outside the expected-outcome handling. A scenario's optional
  `assertions` are evaluated against the emitted OBI document through
  `processorscenarios.CheckAssertions`, which is the processor runner's own
  evaluator exported rather than a second implementation of the same five
  verbs, and are not part of the compared identity surface.

- **Synthesis coverage: an `invalid` entry now clears `FullyRepresented`.**
  Every non-represented status — lossy, excluded, invalid,
  implementation-unsupported — clears the derived flag; previously an
  upstream-invalid unit left it standing, so a document whose every target
  was invalid could report `fullyRepresented: true` (MC5 seal-1 finding
  F-V3-1). The TS SDK carries the identical change.

- **Invocation failures now use the minimal abstract record `{code,data?}`.**
  Portable message, details, and diagnostics members were removed; data is
  normalized to the JSON domain and preserves absent versus explicit null.
  `CONTEXT_REQUIRED` retains its closed OR-of-AND challenge in data and is
  validated before resolution. Frame and operation-schema mechanics now use
  the collision-resistant owned codes `ERR_FRAME_PROTOCOL` and
  `ERR_OPERATION_VALIDATION_FAILED`; binding-specific `ERR_PROTOCOL` and
  `ERR_VALIDATION_FAILED` remain open identifiers. Caller cancellation and
  caller-supplied lifetime deadlines uniformly produce `ERR_CANCELLED`;
  native timeout evidence remains below the bridge. `config.value` now uses a
  relative JSON Pointer path, stored context is reused only when every
  requirement of the selected alternative explicitly permits durability, and
  named credentials remain scheme-scoped. The Core OBI document model is
  unchanged.

- **OpenAPI artifact invocation now lives in the independently published
  native client.** `openapi.Invoker` remains the thin invocation adapter,
  `openapi.Synthesizer` owns OBI construction, and `openapi.Adapter` provides
  one cohesive registration for the optional protocol-neutral `sdk.Runtime`.
  OpenAPI-only callers use `openapi-client/go` without constructing an OBI.

- **The OpenAPI module now defaults to `openbindings.openapi@1`.** Exact
  schema-omitted OAS 3.0 non-JSON request and response representations cross
  the protocol-independent boundary as canonical Base64. Media ranges and
  artifact-defined codecs remain unchanged. No OpenAPI binding specification
  has been published; this is part of its first `@1` candidate. Core is
  unchanged.

- **The OpenAPI module now defaults to `openbindings.openapi@1`.** Exact
  JSON-family request schemas whose top-level declarations require
  combinators, conditionals, dependent schemas, or explicit
  `unevaluatedProperties` remain one protocol-neutral application value.
  Binding-private routing preserves the complete value without choosing a
  schema branch or exposing HTTP concepts. Dynamic-object carriage remains
  part of the same first candidate. Core is unchanged.

- **OpenAPI security and request-channel handling now preserves artifact
  alternatives without leaking HTTP concepts into OBI contracts.** Invocation
  selects one complete Security Requirement Object instead of unioning OR
  alternatives, never volunteers ambient credentials when the operation
  declares no security, and refuses processor-owned `Host`, `Content-Length`,
  and conflicting raw/structured cookie declarations before dispatch.
  Synthesis excludes parameter-content media that the candidate cannot
  faithfully carry instead of emitting an operation guaranteed to refuse.
  Undefined security-scheme names fail closed, including in mixed OR sets.
  These changes remain entirely in the OpenAPI binding adapter; the core OBI
  document model is unchanged.

- **OpenAPI synthesis now projects request and response schemas in their
  authored data directions.** Request contracts omit `readOnly` properties,
  response contracts omit `writeOnly` properties, and nested, composed, map,
  and recursive required sets remain coherent. OpenAPI 3.1 Schema Object
  `$ref` siblings are normalized before typed artifact resolution so their
  constraints and annotations compose; strict 3.0 Reference Object siblings
  remain ignored, and legal 3.1 Reference Object descriptions remain local to
  each reference site. Schema-shaped data in examples/extensions remains
  opaque. A synthesis-only raw presence sidecar now preserves authored null,
  empty, zero, false, and `x-*` Schema Object values that the typed upstream
  parser cannot distinguish from absence; typed OpenAPI objects remain the
  operational authority, and invocation does not use the sidecar. Unsupported
  custom schema dialects fail portable synthesis honestly without globally
  disabling artifact-native invocation or reference listing. This stays
  within the OpenAPI loader/projection layer and does not alter Core.

- **OpenAPI invocation, inspection, and synthesis now resolve complete
  multi-document descriptions through the caller-supplied HTTP client and
  context.** External artifact retrieval is cached, cancellation propagates,
  redirects contribute their final retrieval URI, non-success retrievals fail
  loudly, and resolved external SchemaRefs are internalized with stable
  collision-resistant local identities before OBI projection so
  artifact-relative references cannot escape as dangling OBI schema
  references. Resolver configuration remains binding-private and does not
  alter the protocol-blind OBI document model.

- **`openbindings.openapi@1` added response-carriage fidelity.** It adds
  response media-range selection and exact artifact-authorized raw response
  bytes as canonical Base64 application values, without exposing HTTP facts
  in ordinary outputs.

- **`openbindings.openapi@1` added request-media fidelity.** It adds
  declaration-led raw-octet requests and configured media-range selection
  while keeping HTTP identities out of operation contracts. The same first
  candidate retains collision-preserving routed inputs.

- **Per-operation dependencies compose compatibility, invocability, and
  caller policy without introducing a registry.** The core SDK now exposes
  `OperationRequirement`, `CheckOperationCompatibility`,
  `MatchOperationRequirement`, and `ResolveOperationRequirement`. A consumer
  pairs an ordinary required OBI with a typed operation signature; an
  application supplies concrete interfaces and its explicitly installed
  `OperationInvoker`s. Matching is alias-aware, checks only the requested
  operation against both complete schema graphs, offers preflight, and
  carries advisory context requirements. The neutral
  matcher returns every invocable match; the route-to-one convenience selects
  a unique highest caller preference and refuses a tie as
  `OperationRequirementAmbiguous`. Format modules remain optional and
  separately linked.

- **`FetchInterface` retains synthesis coverage.** A synthesized
  `FetchedInterface` now carries the durable `SynthesisCoverage` emitted by a
  coverage-capable synthesizer instead of discarding it at acquisition. A
  synthesizer without that optional surface still falls back to strict
  synthesis. Direct and well-known OBI fetches leave coverage absent.

- **MCP synthesis and invocation now support a fidelity-tested native
  round trip.** Synthesized tool outputs describe the complete
  `CallToolResult` (with an upstream `outputSchema` correctly scoped to
  `structuredContent`) and admit solicited progress; resource operations
  describe complete `ReadResourceResult` values. Live embedding retains the
  raw pagination-exhausted listing for descriptor-preserving adapters,
  unsupported negotiated revisions are gated consistently, native
  `isError` results retain their complete MCP payload in structured error
  details, and `bearerToken` uses the specification's declared
  `Authorization: Bearer` carrier.

- **Portable synthesis conformance now proves refusal as well as successful
  coverage.** The shared version-2 corpus requires loud whole-source failure
  where faithful synthesis is impossible, and records runtime configuration
  prerequisites on represented targets. The resulting loop fixed gRPC and
  Connect authoring paths that could emit sources with non-conforming target
  addresses; OpenAPI coverage now identifies unresolved server selection, and
  gRPC coverage identifies the transport election required by bare
  `host:port`.

- **The experimental Workers RPC stub was removed.** It could neither invoke
  the runtime-local protocol from Go nor synthesize an interface, and no
  published binding specification governed its legacy token. The Go SDK now
  exposes only binding modules with complete first-party implementations.

- **GraphQL now implements the unreleased first `openbindings.graphql@1`
  candidate end to end.** Invocation requires the exact executable document,
  verifies its selected kind and one-root-field correspondence, passes caller
  input wholesale as variables, and emits the selected root application value.
  GraphQL response envelopes remain diagnostic; a selected partial value is
  preserved before unsuccessful completion. Synthesis inventories query and
  mutation root fields with root-value schemas and exhaustive coverage;
  subscriptions are excluded rather than approximating their continuing
  partial-data/error lifecycle. Go and TypeScript exercise the same candidate
  semantics. No GraphQL binding specification has been published.

- **Comparison-engine cross-SDK canon (three rulings, 2026-07-20).**
  (1) `CheckInterfaceCompatibility`'s issue ordering — sorted
  required-operation-key order, output before input within an operation — is
  now a documented, pinned contract (the TS SDK changed to match; the Go
  order is unchanged). (2) Property/`required` member names in reason
  strings now interpolate in JCS (RFC 8785) rendering — the same rendering
  values already get — instead of Go `%q`; visible only for names carrying
  quotes, backslashes, or control characters (e.g. a name carrying U+0001
  now renders it as `\u0001`, not Go's `\x01`); plain names render
  byte-identically to before.
  (3) The package-level `schemaprofile.InputCompatible` /
  `OutputCompatible` now refuse tell-tale non-normalized inputs loudly with
  the new `NotNormalizedError` (`not normalized at <path>: keyword "<kw>"
  must be <requirement>`) instead of risking silently divergent verdicts:
  a scalar `type`, an unresolved `$ref`, or an unflattened `allOf`,
  anywhere the comparison would recurse. Normalized-path callers
  (`Normalizer` methods, `CheckInterfaceCompatibility`) are unaffected.
  All three are pinned byte-for-byte against the TS SDK in the mirrored
  alignment tables (`schemaprofile/reasons_test.go` ↔
  `packages/sdk/src/schema-profile/reasons.test.ts`).

- **Type names in comparison reason strings join the JCS canon** (a direct
  extension of ruling (2) above). The missing-type diagnostics (`type:
  candidate does not allow ...` / `type: candidate allows ... but target
  does not`) now render type names via the same JCS (RFC 8785) string
  rendering member names and values use, instead of Go `%q`. For the seven
  legitimate JSON Schema type names the output is byte-identical to before
  (verified, not assumed); the change is visible only for pathological type
  names reachable via non-normalized input (a name carrying a quote,
  backslash, or control character — e.g. U+0001 now renders `\u0001`, not
  Go's `\x01`). Pinned byte-for-byte against the TS SDK in the mirrored
  alignment tables.

- **OBI-D-05 literal form is enforced.** A percent-encoded same-document
  fragment (`#/schemas/T%61sk`) now fails validation at OBI positions, and the
  OBI-D-16 resolver no longer percent-decodes — the non-conformant spelling is
  never honored.

- **One endpoint-key derivation.** `NormalizeContextKey` strips URL userinfo
  and case-folds the host, and the resolver read path (`NormalizeEndpoint`)
  delegates to it. Derived context-store keys change for URLs with mixed-case
  hosts or userinfo; they now match the TS SDK byte-for-byte.

- **`IsSupportedVersion` now answers OBI-T-04 acceptance (patch-lenient within a
  supported minor line), matching `Validate`/`ParseDocument`; previously it was
  the strict tested-range check.** A 0.2.0 SDK now reports `true` for `0.2.1`,
  `0.2.99`, etc. — the versions `Validate`/`ParseDocument` actually process —
  and continues to report `false` for a different major, a pre-1.0 different
  minor, and unsupported prereleases. The oracle now shares the single refusal
  predicate the validation paths use, so it cannot drift from them.
  `MinSupportedVersion`/`MaxTestedVersion`/`SupportedRange()` are unchanged and
  remain the maintainer-*tested* range — a distinct, narrower notion (a version
  can be accepted without being inside the tested range).

- **Added `ErrCodeUnavailable` (`ERR_UNAVAILABLE`) to the open code space.**
  Binding implementations decide when their governing rules use it. The
  abstract code carries no universal retry category, side-effect claim, or
  native status mapping.

- **The consumer hook seam (specification + configuration = complete
  invocation).** New core types `OutputDecoder`, `ResultClassifier`, and
  `FieldRouter` — generic callbacks consulted by every format invoker for the
  wire questions a source artifact cannot answer (how output bytes decode,
  which completion statuses are success, which channel an input field rides).
  Consultation decline-chains per axis: per-invocation options
  (`WithOutputDecoder`/`WithResultClassifier`/`WithFieldRouter`) → invoker-level
  fields (`OperationInvoker.OutputDecoder` etc.) → the format built-in, with
  `ErrUseDefault` as the uniform decline. Hooks see an `InvokeSite` (canonical
  operation key, format, ref, target) and a `RawResult` (status, body, meta);
  failures carry tier provenance. `SnapshotHooks` exposes the both-tier
  snapshot to direct binding-layer callers; `WithRuntime` carries hook fields.
  Decode/classify/route provenance and unvalidated-assumption warnings remain
  below the abstract invocation boundary as binding-interpretation evidence.

- **BREAKING: content-independent decode/classify in the openapi and asyncapi
  invokers (de-sniffed).** openapi now decodes by the response's Content-Type
  HEADER (strict JSON for `application/json`/`+json` — a declared-JSON body
  that fails to parse is a loud `ERR_RESPONSE_ERROR` — text otherwise) and
  classifies success as 2xx; asyncapi decodes by the operation's declared
  message `contentType` and no longer unwraps `{error}`/`{data}` convention
  envelopes in the builtin (attach an `OutputDecoder` for convention lanes).
  The `MaybeJSON` helper (payload sniffing) is REMOVED from the core surface.
  Raw captures remain below the abstract invocation boundary and never become
  failure data.

- **BREAKING: the usage invoker consumes bare jdx artifacts.** The
  `openbindings.usage` wrapper format is deleted; the artifact IS the source
  (`usage@^2.0.0`), refs are space-separated command paths, and the exec
  assumptions are documented and hook-overridable: stdout decodes as text
  (command-substitution semantics; the JSON heuristic is gone), exit 0 is
  success, fields ride argv. Channel constants (`RouteArgv`, `RouteStdinDash`,
  `RouteStdin`, `RouteFile`) name the `FieldRouter` value space with loud
  argv-assembly refusals; `HookTable` compiles per-CLI elections
  (JSON lanes, ok-exits, routes) into guarded hooks. Synthesis emits
  floor-true `{"type":"string"}` output schemas carrying an in-schema
  `x-ob.floor` stamp that keys the diagnostics and clears on election.

- **Operations are invoked through signatures.** Added `OperationSignature[I, O]`
  (an inert `{Key}` carrying its input/output types as phantom parameters),
  `NewOperationSignature[I, O](key)`, the variadic functional options `InvokeOption`
  (`WithContext`, `WithBindingKey`), and the public free function
  `Invoke(ctx, invoker, obi, sig, ...InvokeOption) *TypedInvocation[I, O]`. The interface is a
  runtime argument, never part of the signature, so a signature is
  provider-agnostic; `[any, any]` is the dynamic flavor. The previous
  `(*OperationInvoker).Invoke(ctx, *OperationInvocationArgs)` method and the exported
  `OperationInvocationArgs` type are removed — the engine logic lives directly in the
  free `Invoke`, the single public invocation verb (no separate dispatch method and no
  args carrier).

- **Invocation is now a cardinality-agnostic handle.** `BindingInvoker.InvokeBinding`
  and the free `Invoke` return an `Invocation[I, O]` synchronously
  instead of `(<-chan InvocationOutput, error)`: the caller writes input
  messages (`Write`/`Close`), acquires the output sequence (`Outputs()
  OutputStream[O]`, read to `io.EOF` / terminal), and observes lifecycle via
  `Cancel`. One call shape serves unary,
  server-streaming, client-streaming, and bidirectional bindings; cardinality
  lives in the binding, never in the signature. Bindings implement the
  push-side `BindingHandle[I, O]` (`ReadInput`, `CloseInput`, `EmitOutput`,
  `CloseOutput`, `FireError`, `Done`) over the
  shared reference `InvocationImpl` (`NewInvocationImpl(ctx)`). The impl uses
  bounded buffered channels that are never closed on terminal — terminal state
  is a `done` channel plus `terminalErr`, every blocking op `select`s on it,
  and readers drain buffered outputs before surfacing the error (verified under
  `-race`). `Outputs()` is acquire-once (a second call panics
  `ERR_ALREADY_CONSUMED`); `OutputStream.Stop()` cancels. The one blessed
  terminal is the free function `Single(ctx, out)` — strict, short-circuiting
  "exactly one" (`ERR_EXPECTED_SINGLE`). `TypedInvocation[I, O]` adapts the
  untyped handle at the codegen boundary. The `InvocationOutput` envelope and
  its `Status`/`DurationMs` fields are gone: outputs are bare values.
  Unsuccessful completion is exactly `Code` plus optional `Data`; transport
  facts remain below the abstract boundary.
  - `BindingInvocationInput`/`BindingInvocationSource` → `BindingInvocationArgs`/
    `InvocationSource` (`{Source, Ref, Binding, Context, Interface, InputSchema}`;
    no `Input`, no `Security`, no `Store`, no `Callbacks`);
    `OperationInvocationInput` is removed — input flows through the handle, and
    invocation goes through the free `Invoke` + `OperationSignature`.
    `SingleEventChannel`, `FailedOutput`, and `HTTPErrorOutput`
    are removed (`HTTPError`/`HTTPErrorCode` replace the HTTP helpers).
  - OBI-T-07 failures are terminal AND reject the offending `Write` with the
    same `*InvocationError`; OBI-T-08 failures are terminal and the invalid
    value is not emitted (previously surfaced data-alongside-error in the
    envelope). Transforms evaluate per message in both directions.
  - `Invoke` returns a pre-errored handle (local codes
    `ERR_OPERATION_NOT_FOUND` / `ERR_BINDING_NOT_FOUND` / `ERR_UNKNOWN_SOURCE`)
    for wiring errors; runtime outcomes travel on the handle.

- **Error-code wire values are now SCREAMING_SNAKE with the `ERR_` prefix**
  (`ErrCodeCancelled = "ERR_CANCELLED"`, etc.), plus the un-prefixed
  negotiation signal `CONTEXT_REQUIRED` (`ErrCodeContextRequired`), in lockstep
  with the TypeScript SDK and the `openbindings.binding-invoker` role. New
  codes: `ErrCodeAlreadyConsumed`, `ErrCodeExpectedSingle`, `ErrCodeInputClosed`,
  `ErrCodeInvocationClosed`, `ErrCodeTooManyInputs`, `ErrCodeMissingInput`,
  `ErrCodeProtocol`, `ErrCodeTransportClosed`, `ErrCodeRuntime`. Consumers
  switching on `Code` must update. `ErrCodeInvalidInput` is removed (use
  `ErrCodeValidationFailed`).

- **Authentication is negotiated context, not a document field.** Bindings that
  need missing runtime context fire `CONTEXT_REQUIRED` (details:
  `ContextRequiredDetails` — `Key` + disjunctive `Alternatives` over
  conjunctive `Requirements`, families
  `auth.bearer`/`auth.apiKey`/`auth.basic`/`auth.oauth2`) before output or observable
  effects of the requested operation. The `OperationInvoker` resolves challenges known at preflight
  via a composition-time `ContextResolver` (a live challenge surfaces to the
  caller; see the replay-removal entry above). Invokers that can derive
  requirements from their source implement optional `BindingPreflighter`
  preflight; `StoreContextResolver(store)`/`ContextSatisfies` compose the
  binding-invoker and context-store roles. `OperationInvoker.WithRuntime` now
  takes a `ContextResolver`.

- **Renamed binding "executor" terminology to "invoker" / "invoke"** to align with the OpenBindings spec 0.2.0 rename. Pre-1.0 hard rename, no deprecated aliases. Both layers — the per-format component and the orchestrator — use the `Invoker` noun, with the verb `Invoke` shared across them.
  - Types: `BindingExecutor` → `BindingInvoker`; `OperationExecutor` → `OperationInvoker`; per-format `*.Executor` → `*.Invoker` (e.g., `openapi.Executor` → `openapi.Invoker`); `BindingExecutionInput`/`BindingExecutionSource` → `BindingInvocationInput`/`BindingInvocationSource`; `OperationExecutionInput` → `OperationInvocationInput`; `ExecuteOutput`/`ExecuteError`/`ExecutionOptions` → `InvocationOutput`/`InvocationError`/`InvocationOptions`.
  - Methods: `BindingExecutor.ExecuteBinding(...)` → `BindingInvoker.InvokeBinding(...)`; `OperationExecutor.ExecuteOperation(...)` → the free `Invoke(...)`; `OperationExecutor.AddBindingExecutor(...)` → `OperationInvoker.AddBindingInvoker(...)`; `InterfaceClient.Execute(...)`/`ExecuteWithOptions(...)` → `InterfaceClient.Invoke(...)`/`InvokeWithOptions(...)`.
  - Constructors: `NewOperationExecutor` → `NewOperationInvoker`; per-format `NewExecutor` → `NewInvoker`.
  - Helpers: `CombineExecutors` → `CombineInvokers`; `ErrNoExecutor` → `ErrNoInvoker`.
  - File renames: `executor.go` → `binding_invoker.go`, `operation_executor.go` → `operation_invoker.go`, `executor_types.go` → `invoker_types.go`; per-format `executor.go` → `invoker.go`, `execute.go` → `invoke.go`.

- **`Interface.ValidateInterface()` renamed to `Interface.Validate()`.** The package-name-flavored verb was redundant when the receiver was already an `Interface`. `ValidateDocument(data)` (which parses then validates) keeps its name.

- **Validation options trimmed.** `WithExampleValidation` and `WithRequireSupportedVersion` removed: example schema validation (OBI-D-15 then; OBI-D-11 under the current numbering) and the supported-version check (OBI-T-04) are now unconditional in `Validate()`. `WithRejectUnknownTypedFields` is the only remaining option.

- **OBI-T-07 / OBI-T-08 nil guards tightened.** `Invoke` and the streaming output path now validate input/output against the operation's schema whenever the schema is specified, including when the value is `nil`. Previously these checks silently skipped on `nil`, which let invalid omissions slip past the contract.

- **Combiner format-token lookup** prefers exact token equality before falling back to range matching, so a source pinned to `openapi@3.1` no longer accidentally selects an invoker advertising `openapi@^3.0.0` when an exact entry is registered.

- **`schemaprofile`, `compatibility.go`, and `formattoken.normalizeSemverVersion` reframed as openbindings reference-tooling conventions** (not spec primitives). Spec 0.2.0 explicitly leaves schema comparison, operation matching, and format-token equivalence to tools per its §2 Scope principle. The package docstrings now state this; the helpers themselves are unchanged in behavior. `formattoken.normalizeVersion` was renamed to `normalizeSemverVersion` to make the SemVer-only scope explicit.

- **`ErrCodeExecutionFailed` retains its name** with a new comment explaining the deliberate retention: error codes name runtime outcomes (the call was *executed* and the service returned an error), not the SDK type or method that produced them, so the rename did not propagate to the error code.

### Removed

- **`PreparedInterface` and root helpers without a Core role** (breaking,
  pre-1.0). `PrepareInterface`, `PreparedInterface`, and its operation,
  dependency, and binding descriptors were a validated, indexed snapshot for
  the composition runtime; the specification defines nothing like it and the
  package never used it. `SchemaObjectForm` served schema comparison. The
  version-line predicates `IsHigherMajorOrPre1MinorThanMaxTested` and
  `IsLowerThanMinSupported` are private: `IsSupportedVersion` is the
  OBI-T-04 acceptance predicate. `LookupDependency` and
  `ResolvedDependency` are gone: a dependency is two map lookups,
  `iface.Dependencies[key]` and then `iface.Operations[dependency.Operation]`. `invoke`, the `sdk` facade, and the README's
  dependency-composition example still use `PrepareInterface` and are
  reconnected separately.

- **`WithRejectUnknownTypedFields` and the exported `ValidateOption`**
  (breaking, pre-1.0). OBI-T-02 requires every processor to ignore unknown
  fields; the option turned them into rejections. Unknown non-`x-` fields
  are now always surfaced as OBI-T-02 diagnostics in a `ValidationReport`,
  which is what the rule asks for, and never affect validation.
  The option is gone; `ValidateOptions` carries only capabilities validation
  does not have itself.

- **The local provider is gone: `PrepareLocalProvider`, `PrepareLocalProviderOptions`,
  `LocalBindingImplementation`, `LocalImplementationOption`, `LocalUnary`,
  `LocalStream`, `LocalPreflight`, `WithLocalPreflight`** (breaking, pre-1.0).
  It keyed handlers by binding key while warranting support for whatever
  binding specification the OBI's source declared, a conformance warrant for
  rules it did not implement. An application that realizes operations in
  process writes a `BindingInvoker` for its own application-private
  specification identifier, exactly like every other realization; the README
  shows one. No consumer outside the SDK's tests used the removed surface.
  TypeScript removal is a tracked parity item.

- **Root-package comparison and dead helpers** (breaking, pre-1.0).
  `CompareBoundaryContracts`, `PreparedBoundaryContract`, and
  `PreparedInterface.BoundaryContract` are gone: the composition policy's
  exact-identity step is removed and `AssessContract` goes straight to
  `compare.CheckOperationCompatibility`, where the profile's identity rule
  makes identical contracts compatible; `ContractEvidence.Method` is always
  `"directional-profile"` (the `"exact"` value no longer exists).
  `PreparedInterface.ExportJCS` and `JCSExport` (unused) are gone;
  `SnapshotID` stays as a process-local handle identity. `ResolveRef`
  (unused; the SDK never resolves relative references, per OBI-D-05) and
  `ECMARegexpEngine` (dead; the engine lives in `internal/schemacompiler`)
  are gone, and the root module no longer depends on
  `github.com/dlclark/regexp2` v1. `WellKnownPath` moved from the root
  package to `synthesize` (same value; documented against the HTTP
  Discovery companion specification). ob re-exports `WellKnownPath` and
  follows separately.

- **The `security` surface, per spec 0.2.0**: the OBI `security` section
  (`Interface.Security`), `BindingEntry.Security`, `SecurityMethod`,
  `ResolveSecurity`, and the security-reference validation
  (`security.go`/`security_test.go` deleted). Credentials are never part of an
  OBI document; they are context, supplied per call or resolved through the
  `CONTEXT_REQUIRED` protocol. Format invokers derive auth requirements from
  their source artifacts (e.g. OpenAPI `securitySchemes`) and read credentials
  from context's well-known fields. `ContextStore`/`PlatformCallbacks` are no
  longer threaded through binding invocations; interactive resolution lives in
  the app's `ContextResolver`.

- **Conformance rule IDs corrected** to match the spec: `OBI-D-16` → `OBI-D-13`
  (SemVer `openbindings` field; `OBI-D-12` under the current numbering),
  `OBI-T-13` → `OBI-T-12` (operation-name resolution).

- **`InterfaceClient`.** The struct and its `InterfaceClientOption`,
  `WithContextStore`, `WithPlatformCallbacks`, and `WithDefaultContext`
  options are gone. Generated typed invokers (from `ob codegen`) wrap an
  `*OperationInvoker` directly and take the OBI per method call. Direct
  callers use the free `Invoke(ctx, invoker, obi, sig, opts)` and configure
  runtime via `OperationInvoker.WithRuntime(resolver)`.

- **`InvocationOptions`.** Folded into `BindingContext`. Transport fields
  (`headers`, `cookies`, `environment`, `metadata`) are well-known keys
  inside the context map; helpers `ContextHeaders`/`ContextCookies`/
  `ContextEnvironment`/`ContextMetadata` read them. `BindingInvocationInput`
  no longer carries a separate `Options` field.

### Fixed

- **Compatibility checking now handles the boolean `false` schema — the
  spec's spelling for "carries no caller input" / "emits no output".**
  `CheckInterfaceCompatibility` previously rewrote `false` to its object
  spelling `{"not": {}}`, which the schema-compatibility profile rejects
  (`outside profile: keyword "not"`), so any operation declaring
  `input: false` failed requirement resolution as `input_incompatible`
  even against itself. `false` now short-circuits before normalization:
  compatible exactly with `false`, incompatible (with a clear reason)
  against any other specified schema; `true` continues to flow through
  the normal check as the empty schema. Caught live by the Panjir dogfood
  loop resolving a no-input contract operation. Mirrored in the TS SDK.

- **Operation-boundary schema validation now preserves the OBI document as
  the same-document reference root.** Input, output, and example validation
  compile schemas at their canonical `#/operations/...` addresses instead of
  extracting them into a synthetic root, so operation-local recursive
  `$defs`, cross-operation pointers, escaped operation keys, named schemas,
  and embedded absolute `$id` resources retain their JSON Schema meaning.
  `ValidateOperationInput` and `ValidateOperationOutput` expose the same
  interface-aware boundary to applications that drive binding invokers
  directly.

- **Schema-comparison `allOf` normalization is sound.** Branches normalize
  fully before merging (`$ref` branches resolved and profile-checked, nested
  `allOf` flattened), sibling keywords merge as one additional branch, and
  `oneOf`/`anyOf` are refused whether inline, ref-carried, or alongside
  `allOf`. Closes the false-`compatible` family; red-proven against the seven
  new comparison-corpus fixture families.

- **Interface compatibility resolves each side's `#/schemas/` refs against its
  own document.** `CheckInterfaceCompatibility` normalized both sides with a
  single unrooted normalizer, so any fragment-only `$ref` on either side
  failed resolution and surfaced as a spurious
  `output_incompatible`/`input_incompatible` issue — an interface using the
  `schemas` section got a different verdict here than from the TS SDK, which
  roots per side. Each side now normalizes against its own document view and
  the pre-normalized pair runs the package-level directional check. The
  comparison-corpus harness applies the same per-side rooting (it previously
  resolved the right document's refs against the left document), pinned by
  the corpus's `subsumption/schemas-root-per-side-*` fixtures.

- **Comparison reason strings are deterministic and TS-aligned.** Multi-member
  diagnostics no longer leak Go map iteration order into the detail string:
  missing-type lists render sorted lexicographically, and enum/required/
  properties faults name the lexicographically first failing member —
  byte-identical with the TS SDK, pinned by the mirrored reason-string table
  (`schemaprofile/reasons_test.go`).

- **`Validate` accepts leading-digit identifiers (`2fa.verify`) per the
  committed OBI-D-03 grammar**, so `ParseDocument` and `Validate` agree again.

- **Scheme-scoped `apiKeys` redact like every other credential field.**
  Redaction and scoping single-source the one credential registry, pinned by a
  per-field sentinel drift-guard test.

- **`InvokeHooks` decode/classify provenance is published atomically**, with
  the concurrency contract stated in godoc (previously it rode an incidental
  happens-before edge through the emit path).

- **README**: the module bundles a JSONata 2.x parser (gnata) for OBI-D-18
  parse-checks — the "no bundled JSONata runtime" claim was stale. Retired-rule
  citations repointed (OBI-T-08 → OBI-T-16).

- **Schema validation failure rendering.** `splitSchemaError` and
  `collectValidationFailures` formatted jsonschema/v6 `ErrorKind` leaves with
  `%v`, printing the raw struct (`&{[customer]}` for a missing required
  property) instead of the kind's localized message (`missing property
  'customer'`). The garbage text also flowed into
  `ValidationFailureDetails.Failures[].Message` — the wire-crossing details
  payload consumers render per-field, where the TS SDK already produced
  readable messages. Leaves now render via `ErrorKind.LocalizedString`.

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

- **Configurable delivery-unit bound.** `BindingInvocationArgs.MaxDeliveryUnitBytes`
  bounds ONE DELIVERY UNIT — the bytes materialized to produce one emitted
  output value (a unary response body, one SSE event, one streaming envelope,
  one WebSocket message, one captured stdout). `OperationInvoker` gains the
  matching public policy field, stamped into args exactly like the hook
  fields; direct binding-layer callers set it per invocation on args. Zero or
  negative selects the exported `DefaultMaxDeliveryUnitBytes` (10 MiB —
  byte-identical to the per-lane constants it replaces; no unlimited
  sentinel, set an explicitly huge bound instead);
  `BindingInvocationArgs.DeliveryUnitLimit()` is the single resolution point
  formats call. Overflow error identity per lane is unchanged — same codes,
  same message templates, the value is now dynamic. Read sites: openapi
  (unary body), asyncapi (unary reply, SSE per-event, WebSocket per-message),
  connect (unary body, per-envelope), graphql (response body, subscription
  per-message), usage (stdout capture). Named exclusions, documented in each
  format README's "Resource bounds" note: grpc (message size rides grpc-go's
  native `MaxCallRecvMsgSize` via the `WithDialOptions` pass-through), mcp
  (no read-bound seam in the official MCP Go SDK), operationgraph doc-fetch
  (artifact guard). Diagnostics and artifact-side bounds (error-body
  captures, stderr tail, SSE line-scanner guards, artifact fetches, routing
  caps) stay fixed by design.

- **Transforms compile-lane conformance gate**: the spec
  `conformance/transforms` agreement corpus runs through `gnata.Compile` — the
  exact parse surface `Validate` ships.

- **CI corpus gating (`OB_CORPUS_REQUIRED`)**: CI checks out both corpus roots
  (spec + interfaces) and every corpus locator fails loudly when a corpus is
  required and absent; local skip-if-absent behavior is unchanged.

- **Conformance-runner `requiresSupports` annotation**: the corpus harness
  honors the per-test `requiresSupports: "X.Y.Z"` annotation — the test is
  administered only when this SDK's OBI-T-04 version-acceptance predicate
  (`IsSupportedVersion`) accepts X.Y.Z; otherwise it is skipped and reported
  as a skip, never a failure, alongside the existing `requiresMaxTested` /
  `requiresMinSupported` gates. A corpus without the annotation runs
  unchanged.

- **`Invocation.InputClosed()`** — a channel closed once the invocation's input side has closed: by the caller's `Close`, by the binding from below (a unary binding after its first read), or by a terminal transition. Lets consumers that pipe a stream into an invocation (the operation-graph conduit) observe non-acceptance without probing with a failing `Write`. Implemented by `InvocationImpl` and forwarded by `TypedInvocation`.

- **`ErrTransformUndefined`** sentinel — evaluators return it (possibly wrapped) when an expression yields no result, since Go's `any` cannot distinguish JSONata's undefined from JSON null. The operation-graph engine maps it to the spec's `TRANSFORM_UNDEFINED` node failure; null flows downstream normally.

- **`ErrCodeUnsupportedFormatVersion`** (`ERR_UNSUPPORTED_FORMAT_VERSION`) for format-version refusal (e.g. operation-graph OG-T-02 mirroring OBI-T-04). `ErrCodeMapNotArray` is removed: per-node graph failure identifiers (`TIMEOUT_EXCEEDED`, `WRITE_REJECTED`, `MAP_NOT_ARRAY`, `TRANSFORM_UNDEFINED`) are format error identifiers and live in `formats/operationgraph`.

- **URI helpers** `CanonicalizeLocation` and `ResolveRef` per spec §10 (Location Equality) and §12 (Reference Resolution). `CanonicalizeLocation` lifts bare absolute paths to `file://`, lowercases scheme and host, IDN-punycodes via `golang.org/x/net/idna`, strips the default port and fragment, removes dot-segments, and normalizes percent-encoding of unreserved characters; reassembly is manual to preserve encoded reserved characters (e.g., `%2F`) that `url.URL.String()` would otherwise discard. `ResolveRef` is a thin wrapper over `url.URL.ResolveReference` with the spec-required guards for empty/non-absolute bases.

- **`drainStream` helper** extracted in `operation_invoker.go` so the producer-drain pattern used by transform short-circuits and stream cancellation is named once and reused.

- **`SynthesizeInput.OnWarning` handler and the `SynthesizerWarning` type**
  (folded from the unpublished 0.1.1, where they were introduced as
  `CreateInput.OnWarning` and `CreatorWarning`, before the
  Creator → Synthesizer rename) for surfacing non-fatal limitations
  encountered during interface construction (e.g., a source-side feature the
  schema profile cannot fully express). Synthesizers that hit such a
  limitation still produce a valid `Interface`; the warning describes what
  was lost or approximated. The handler is optional; when nil, warnings are
  dropped silently, preserving prior behavior for callers who do not opt in.

### Format submodules

- **`formats/grpc` and `formats/connect` migrated to protobuf v2.** Direct dependencies on `github.com/jhump/protoreflect` (v1) and `github.com/golang/protobuf/jsonpb` are gone; both modules now consume `github.com/jhump/protoreflect/v2/grpcdynamic`, `github.com/jhump/protoreflect/v2/grpcreflect`, `google.golang.org/protobuf/types/dynamicpb`, `google.golang.org/protobuf/encoding/protojson`, and `google.golang.org/protobuf/reflect/protoreflect` directly. `formats/connect` additionally moved off `jhump/protoreflect/desc/protoparse` to `github.com/bufbuild/protocompile`. Behavior is preserved across the change; the two integration suites pass against the same fixtures.

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
