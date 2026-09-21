# Value architecture: destination and qualification plan

Status: ownership refined and cold review complete, 20 September 2026.
**Projected containers with retained native leaves (P) are the leading
destination to qualify.** No runtime migration is selected for activation.
This is a design judgment, not measured proof of performance superiority.

Branch: `codex/native-value-migration`.
Base: `origin/release/0.2` at
`71cbd964a8df7a5d2b2981756d351d3a59b86c5d`.
The integration destination was checked against `openbindings/project` main
at `6eceb5dc1e8d0e4c1182f230b83fa2080a8e7b3f`. Its catalog and working-loop
records select `release/0.2`; CONTRIBUTING.md's older `main` wording does not
select this work's destination. The branch name records the original planning
task; it does not require a particular implementation.

## Goal and decision

Choose the ideal architecture for operation data, not the cheapest next patch.
Keep natural Go calls, exact supported logical meaning, reliable results and
convenient explicit JSON export. Remove serialization used merely to cross
internal boundaries. Necessary protocol encoding, custom codec interpretation,
requested string observations and ownership copies remain legitimate work.

The current SDK already supports exact PNG recovery through a real transform,
typed invocation, export and reverse upload without caller-written invocation
serialization. A migration must improve the resulting architecture, not claim
to invent that existing usability. Preserving a particular backing buffer is
an optimization; exact content recovery is a requirement.

| Candidate | Architectural assessment |
| --- | --- |
| A: current machinery with targeted improvements | Maintained codec compatibility and useful generic fast lanes. Local consistency defects are repairable, but ordinary typed bridges and eager byte/string conversion remain. It has not been established as the ideal. |
| B: shared logical reader over native containers | Can avoid projected shells and unify access to durable native views. Requires container-reader integration throughout consumers, and may still require a full admission walk. Remains a serious challenger. |
| P: project ordinary container shells and retain eligible native leaves | Removes ordinary text bridges while keeping familiar object/array structures for consumers. Requires finite scalar integration, checked typed construction, faithful codec fallback and ownership. Leading destination to qualify. |

P is preferred for its steady-state responsibility boundaries, not because it
would be cheapest to implement. Host container interpretation occurs at admission
and typed construction. Most consumers need not learn a universal native-object
reader. B can overturn the choice if it simplifies total integration and removes
meaningful shell cost. A can win if native handling's permanent obligations
outweigh its architectural benefit. Retaining current behavior while qualifying
the choice is an operational decision, not a verdict that A is ideal.

## Intended data flow

1. An application supplies ordinary Go input or a binding produces a value in
   its governing correspondence. Protocol decoding remains with that protocol.
2. Admission establishes one logical meaning in SDK-owned storage. Snapshot
   caller-owned mutable containers and leaves; reuse already admitted SDK-owned
   data internally. Project eligible typed objects/arrays into generic shells
   without JSON text. Recognize codec-defined types before ordinary reflection
   and produce one stable codec-defined snapshot instead.
3. Validation, transforms and other consumers observe those containers and one
   finite scalar contract. Eligible bytes can remain bytes with canonical Base64
   string meaning. Consumer-specific guesses about the carrier are forbidden.
4. Construct detached ordinary typed outputs directly with checked assignments.
   Honor custom output decoders through faithful fallback. An existing generic
   caller's promised string representation may require compatibility
   materialization; count that work rather than silently returning a wrapper.
5. Encode JSON for explicit export or a real protocol/storage need. Returned
   values remain usable for the promised lifetime after invocation termination.

Snapshots and projection allocate storage and can visit branches later discarded. Complete
admission may require B to visit those branches too, but without shell allocation.
Compare that difference with repeated field access, reflection plans, caches,
typed construction and retained storage. Neither fewer codec calls nor a
field-selection microbenchmark establishes whole-path superiority.

## Responsibility boundaries

| Owner | Responsibility |
| --- | --- |
| Root Core package | Document models, references, document validation and schema facade; syntax checking without selecting an execution engine. |
| Existing `jsonvalue` area and private helpers | Bounded projection, common scalar meaning, codec fallback, ordinary construction, detachment and export. No invocation, binding or evaluator dependency. Public helper names remain undecided. |
| `invoke` | Admission and delivery boundaries, transform/validation order, outcomes, retries and lifetime. Preserve evaluator injection. |
| `formats/*` | Binding-defined correspondence and protocol adaptation; no blanket mapping that overrides a family's rules. |
| Optional `sdk` facade | Assemble explicitly supplied providers/evaluator without choosing defaults. |
| Application evaluator adapter | Engine-specific adaptation, undefined/error translation, closed document-expression semantics and result lifetime. |
| Standalone protocol clients | Independently useful protocol APIs and wire mechanics, without SDK carrier or invocation types. |
| Consuming application | Evaluator choice, authorization, approvals, delegation, persistence and UI/export policy. |

No new JSON repository, general mapping registry or arbitrary constructor
framework is required. Keep current package boundaries and independently usable
modules. A transform-free invocation requires no evaluator.

## Semantics that qualification must demonstrate

- A supported value means the same thing in admission, schemas, expressions,
  equality, typed recovery and export. Distinguish absent, null, undefined and
  failure. Inability to validate is not an ordinary schema non-match.
- Nil ordinary host containers follow the selected codec-compatible null
  meaning; nonnil empty containers remain containers. A binding's zero raw
  octets still denote its empty logical string, even if stored in a nil buffer.
- Byte leaves preserve canonical Base64 string observations, including length,
  equality, substring, patterns and `$base64encode`. The latter encodes the
  logical characters, not a host engine's alternate raw-byte extension.
  Observation may materialize text without destroying the eligible carried leaf.
- Preserve exact supported integers, valid number tokens and codec-compatible
  float32 meaning. Invalid descendants, cycles and nonfinite values do not pass
  merely because a transform never reads them. Custom encoders define the
  logical content to inspect; private host internals do not replace that content.
- Native eligibility preserves supported field selection and tags. Custom or
  unusual types use the maintained codec once per admitted snapshot, not once
  per field read. Honor custom output decoding; failures publish no partial
  destination. A narrower prototype is a coverage deficit, not full parity.
- Preserve input validation before input transforms and output validation after
  output transforms. Preserve outcome classification, replay of accepted
  post-transform inputs, retry eligibility, backpressure and accepted delivery.
- Apply the ownership policy below to mutable containers and leaves together.
  Measure its necessary copies and retained backing, including selected children
  and small slices. Logical size alone is not a heap bound.

The [codec maintenance record](internal/thirdparty/jsoncodec/MAINTENANCE.md)
governs the adopted codec. Preserve its semantics; do not replace it with
`encoding/json` based on stale experimental wording in a helper README.

## Ownership and mutation policy

Ordinary operation values cross public handoffs as stable snapshots. Inside
an invocation, admitted values are read-only and may share storage. This single
default applies to typed and generic operation values, local calls and protocol
calls, including published error-data snapshots under their existing admission
rules. It does not clone contexts, transports, configuration handles or unrelated
application resources. No public borrowing mode, lease, reference counter or
ownership-selection flag is added.

| Handoff | Contract | Responsible layer |
| --- | --- | --- |
| Caller `Write` | Capture the complete logical value into owned storage before enqueue acceptance. The caller keeps it stable while `Write` runs and may reuse or mutate it after the method returns, on success or failure. No snapshot reader continues accessing caller storage afterward. Success still means acceptance, not delivery or operation success. | `invoke`, using value-support snapshot/projection helpers. |
| Internal validation, transforms, Graph and replay | Read admitted values without mutation. Construct new containers for changed structure; share immutable admitted leaves where their logical interpretation agrees. Replay retains the accepted post-transform snapshot, without re-running its transform or custom input encoder. | Invocation/Graph lifecycle and common value helpers. |
| Binding `EmitOutput` | Secure stable data before queue acceptance. The producer keeps its value stable during the call and may reuse its buffers after it returns, including on failure. An admitted internal snapshot can be forwarded without another copy. | Binding-facing invocation handoff; adapters detach protocol-owned storage when necessary. |
| Evaluator or native-client integration | Borrow admitted input read-only for the documented call or attempt lifetime. If the library mutates or retains input beyond that interval, its adapter supplies a detached copy. Before returning a result, the adapter detaches reusable/expiring storage or hands over independently valid owned data. References to existing immutable SDK leaves may survive in results. | The integration adapter, preserving its library's independent API. |
| Ordinary local application handler | Give the handler its own mutable input value. A unary return supplies independently valid result storage without producer-retained writable aliases; a handler keeping reusable source storage returns a detached copy. Stream emission follows `EmitOutput` capture-before-return. Mutating handler arguments cannot alter a retry log or another Graph branch. | The local-provider adapter and the handler's return contract, using ordinary construction and snapshot helpers. |
| Public output `Read` | Deliver an independently usable mutable result, detached from invocation state, producer storage and other delivered results. It remains valid after completion, cancellation and later outputs. Ordinary callers require no release call. | Public/typed delivery, using checked construction and detachment. |

These are operation-data guarantees, not a new synchronous validation API.
Keep schema checks, transforms and their failure ordering at their existing
stages. Capture or construction failures use the owning handoff's failure
channel and never publish a partial value. Cancellation does not permit a
background copier or callback to keep reading a caller buffer after the handoff
returns. It also does not imply hard preemption of arbitrary codec or engine
callbacks; the adapter must quiesce access before completing the handoff.

### Aliasing and custom codecs

For ordinary Go construction, public results are logical value trees: mutable
maps, slices, pointers and byte leaves are independent between delivered values
and between repeated field occurrences within one result. If a transform puts
one image into two fields, mutating either returned byte slice does not alter
the other. Internal immutable storage can remain shared until delivery. Immutable
strings and scalar values can be shared freely. Exact buffer identity is not
promised at public handoffs.

Custom input codecs determine one logical snapshot; their original host object
need not be cloned. A custom output decoder receives a fresh destination and
private encoded input under the maintained codec's rules. Its chosen host
representation and any deliberate private sharing remain that codec's contract.
The SDK does not reflectively clone the resulting private object or promise to
isolate arbitrary global state managed by user callbacks. It never supplies
those callbacks with writable internal snapshot storage. Callback implementations
remain responsible for their own concurrent activity and retained references.

SDK read-only sharing is an implementation contract, not a claim that Go maps
and slices are immutable types. Only already admitted SDK-owned values qualify
for internal reuse. A raw Go type assertion or an unknown buffer's apparent
uniqueness is insufficient. Adapters must isolate mutating libraries. Internal
exclusive ownership may avoid a copy at a handoff only when the same mutation,
lifetime, compactness and no-alias guarantees remain true; otherwise copy.

### Retention and release

At public capture and delivery, copy only visible slice elements and byte ranges
into fresh storage; do not retain a caller's oversized capacity or an integration
arena through a tiny result. Unknown backing ownership or extent requires
detachment. Before a derived subrange enters a replay log, Graph buffer or output
queue, compact it if it would otherwise retain an oversized backing allocation
for unrelated content. Whole, compact SDK-owned leaves can be shared internally.
Necessary copies establish ownership directly; they require neither JSON text
nor Base64 materialization.

Each queue, replay log and Graph state releases its references when its logical
retention ends. Retirement waits for readers that still use a snapshot; closing
the retry window releases its log, while accepted queued outputs stay valid
until drained or explicitly abandoned. Invocation shutdown drops only internal
references, never caller-owned outputs. Go-owned results follow ordinary garbage
collection. Adapters release native handles/leases after detachment or the last
permitted internal use; no integration-owned lease escapes in an ordinary result.
This is a storage-lifetime policy, not a numerical heap guarantee. Existing flow
limits still apply; comparisons charge copies, temporary peaks and retained heap.

### Compatibility decision

The destination applies the snapshot default to generic maps/slices as well as
typed values. It intentionally replaces current end-to-end mutable reference
identity, including the documented generic `LocalUnary` fast path. Input mutation
after `Write`, and handler/result mutation, no longer communicate through shared
caller storage. Logical values, custom codec meanings, invocation signatures and
outcome ordering remain the compatibility targets; shared mutable identity does
not. Document this in Changed/Removed entries and the migration matrix and
activate it only in a permitted breaking version, never as a silent patch or
provider upgrade. No parallel public legacy-ownership mode is introduced.

Already owned internal generic values still use direct container access and can
be forwarded read-only. The removal of public aliasing has real copy costs,
including small generic calls; report those against the current baseline rather
than claiming the old zero-copy behavior survives unchanged.

## Ownership review and remaining decision

Six fresh readers assessed this ownership revision using the same grade anchors
as the preceding assessment, without prior grades or feedback between panels.
All regard the ownership and mutation contract as resolved. Overall grades were
one A, two A- and three B+. Original reports and frozen inputs are recorded in
the coordination workspace at `design/sdk-value-grading/candidate-2/RESULT.md`.

The remaining material concern is resource containment: existing queue and
protocol-delivery limits do not bound accumulated replay history, simultaneous
snapshot captures, or expansion when shared internal leaves become independent
mutable output occurrences. Resolve invocation-owned accounting and exhaustion
behavior before broad implementation. This policy remains an explicit open
decision; neither the lifetime rules nor the favorable ownership review resolves
it. Numerical limits require qualification. Smaller deductions concern eager
container allocation, codec-parity maintenance and reusable unary-handler results.
No comparative performance or production-readiness claim follows from the grades.

## Evidence already collected

Three rounds of three fresh reviewers compared the options using frozen inputs.
Round 1 preferred bounded A work as an investment. After the user clarified the
ideal-destination goal, round 2 split provisionally between B and P. The final
panel independently preferred P, subject to qualification. Those reviews are
design judgments, not nine performance validations; earlier A grades establish
neither comparative superiority nor implementation readiness.

The coordination workspace records the briefs, original reports, dispositions,
source pins, executable probes and raw output under `design/sdk-value-comparison`.
Its `RESULT.md` distinguishes findings, hypotheses and remaining qualification.

- A valid 88-byte PNG passed real HTTP, the existing optional SDK evaluator,
  schema checks, exact typed recovery, export after termination and reverse
  upload. The SDK/adapter was this branch's base; native OpenAPI client was
  `7b15d57e960faa6a9e010eb9cbea80fd83f0d02e`. The evaluator was the SDK's existing
  `github.com/openbindings/jsonata/go` dependency at `e2a5e518e6b5`.
- A disposable archive patch returned the normalized transform result already
  computed by current admission. It corrected three schema inconsistencies in
  38 cases without a shared reader. Observable result carriers changed, so this
  is not production qualification. Its evaluator was a test double.
- Eight further controls confirmed that patch leaves nil generic container
  interpretation inconsistent with export. A small correction is not a complete
  architectural answer.
- Source inspection locates initial raw-response Base64 encoding in the native
  client's runtime, before SDK streaming adaptation. Request-side repeated
  conversion is separately improvable under any candidate.

No B/P implementation, comparative timing, allocation or retained-heap result
is claimed. Experiments used pinned temporary assemblies and Go 1.27.1; this is
not a project-wide cohort or declared-toolchain release qualification.

## Next: one bounded comparison before migration

Freeze exact revisions, payloads and measurement procedure using the ownership
policy above before implementing or comparing candidates. Include a corrected A control
with its actual proposed improvements, not a deliberately weak baseline.

First qualify the finite byte/scalar contract with one real application-selected
evaluator and actual schema handling. Test movement, logical string observations,
undefined/error translation and durable output together. Current engine gaps are
implementation gaps, not architectural verdicts. Do not implement a new engine
to clear this gate or introduce an SDK default evaluator.

Then build a disposable P slice and a minimal B challenger through identical
admission, validation, evaluation and typed delivery. Keep their ordinary host
domain and stable codec fallback the same. Exercise:

1. Small generic/typed equivalents and a wide typed structure with narrow
   selection and repeated field reads.
2. Actual raw-image HTTP retrieval, movement and string observations, schema,
   typed byte recovery, explicit export and reverse raw request.
3. A business codec whose meaning differs from its fields, exact numbers,
   nil/empty/missing controls, and hidden invalid descendants without a schema.
4. Input mutation after submission, producer reuse after emission, duplicate
   output-field mutation, local handler mutation, retained Graph output and
   release after invocation ends, including a small leaf backed by a much larger
   allocation. Include failed/cancelled capture and custom-decoder controls.

The native client must expose a useful native response/request path in its own
vocabulary. An SDK adapter cannot restore backing discarded below it. All
governing schema keywords in the fixture must really run; stubs cannot qualify
an architecture. Preserve ordinary caller code and record any recovery burden.

Compare P and B under the same snapshot/aliasing promises. Keep today's A as a
compatibility/performance baseline and distinguish its generic reference behavior;
also charge any snapshots needed for an A variant offering the new guarantees.
Do not infer representation superiority by giving candidates different ownership
obligations or by treating a necessary ownership copy as serialization waste.

Measure total latency/CPU, allocations, peak/retained memory, traversals,
materializations, codec fallback and necessary copies. Enumerate lasting consumer
contracts too. P fails its premise if host-specific exceptions spread through
consumers or typed construction simply serializes everything again. B wins if
its broader reader gives a better total design, not just a faster isolated lookup.

Bound work to this slice, one corrective pass per implementation and a repeat
for reproducibility. Stop if credibility requires a broad framework, backend
rewrite or new evaluator implementation. Report the gap rather than building a
migration to justify itself. Semantic divergence stops a candidate's cost
comparison until explained; incomplete integration does not disprove its design.
Freeze any workload-derived materiality criteria before results. Reviewer-proposed
percentages and engineering-day budgets have not been adopted as user requirements.

Exit with P qualified, a concrete reversal to B or A, or unresolved evidence.
Only then select implementation scope. Broad migration is not the automatic
next step after a favorable design review.

## Conditional implementation and independent repairs

After qualification, integrate one reviewed path at a time: projection/admission,
complete scalar/schema behavior, checked typed/local construction, actual OpenAPI
carriage, then retries/streams/Graph and other protocol families. Record uncovered
paths explicitly. Preserve each family's correspondence, including ProtoJSON;
no single host byte or number rule governs every binding. TypeScript is deferred.

Publish a compatibility matrix covering caller-visible carriers, custom codecs,
field selection, null/missing, scalars, ownership, errors and export before
activation. Prefer current invocation signatures. Necessary public changes follow
the pre-1.0 version policy and require normal release documentation. Do not
activate a semantic change automatically because an evaluator/provider upgraded.

Transform normalization, nil consistency, Graph exact-number/error behavior and
raw-request conversion can be repaired independently. They do not settle the
destination or require completion of the new evaluator. The native client's SDK
helper dependency is also a separate dependency-direction concern. Adapter
packaging changes require their own consumer migration; they are not necessary
to establish application evaluator ownership, which already exists.

Validate changed slices with focused semantic and lifetime tests, then required
root and affected-module checks. Before shared activation, run all format modules
and exact downstream integration, including corpus-required and accepted-output
race lanes. Follow [RELEASING.md](RELEASING.md) for candidate/external-consumer
qualification. Root tests do not include nested modules. Publication, release
tags and project cohort promotion remain separate actions.
