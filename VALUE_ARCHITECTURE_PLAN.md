# Value architecture: destination and qualification plan

Status: resource policy refined for qualification, 20 September 2026.
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

P remains a leading hypothesis for its steady-state responsibility boundaries:
host container interpretation occurs at admission and typed construction, while
most consumers need not learn a universal native-object reader. The bounded
comparison now demonstrates improvements on eligible typed and byte paths, but
does not establish P as the best overall destination. B remains a serious
challenger, and A+ (targeted corrections with equivalent snapshot ownership)
deserves a stronger generic control. Choosing the ideal remains unresolved;
retaining current behavior during qualification does not declare A ideal.

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
| Public output `Read` | Successful reads deliver independently usable mutable results, detached from invocation state, producer storage and other delivered results. Results remain valid after completion, cancellation and later outputs. Typed recovery is a separately bounded, fallible caller conversion as specified below. Ordinary callers require no release call. | Public/typed delivery, using checked construction and detachment. |

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
The resource policy below additionally bounds SDK value work and retention.
Neither policy promises a whole-process heap bound. Comparisons still measure
copies, temporary peaks and retained heap rather than substituting quota units.

### Resource policy

Invocation owns a finite value-work budget, shared by its retries and internal
Graph descendants. It does not reset on an attempt, graph node or binding switch.
Independent application invocations have separate budgets; application-wide
concurrency and memory policy remain with the application. Value helpers receive
private accounting operations from their caller, without depending on invocation
types. This adds no public value wrapper, ownership mode or release obligation.

The invoker supplies process-local limits, fixed for an invocation; a direct
binding-layer invocation gets the same defaults. Optional positive overrides
select larger or smaller finite limits. Zero/unset selects defaults; invalid
negative or overflowing configuration fails before dispatch. No document or
binding can increase its caller's budget. Initial defaults are 64 MiB of work
units per logical value, 256 MiB of simultaneously live units per invocation,
and logical nesting depth 256. These are explicit SDK defaults to qualify, not
Core limits or a claim about allocated heap bytes. They are independent of the
existing transport delivery-unit limit, and all applicable limits must hold.

One value's cost is 64 units per logical node, plus the UTF-8 byte length of its
JSON-escaped string/key contents and its supported number tokens. A byte leaf
contributes its canonical Base64 length, computed without encoding. Counts use
checked arithmetic. Repeated occurrences count repeatedly even when they share
backing; null and empty values still have node cost. Field/tag and custom-codec
meaning are resolved before charging the logical value. The budget walk stops
at the first exceeded limit, respects the depth bound, and need not serialize.
The node allowance bounds structural work and bookkeeping; string accounting
also bounds the ordinary encoded form. Existing numeric-operation limits remain
in force. Cost is a conservative work measure, not an exact allocator model.

Public input and output handoffs acquire separate per-direction capture permits
before copying; each holds its permit through enqueue acceptance or rejection.
Other producers wait without making SDK snapshots, respecting their call context
and invocation termination. Input and output permits are independent and never
span transform or handler execution. This preserves bounded queue backpressure
without allowing arbitrary concurrent producers to build waiting snapshots.

The live ledger covers in-progress SDK capture/projection, admitted inputs,
transforms' admitted results, replay logs, queues, Graph events/root history/
buffers, and internal SDK construction or encoding scratch. Reserve before allocating
or retaining that work, incrementally if needed; a partial construction cannot
grow uncharged while waiting to enqueue. Scratch bookkeeping is bounded and
charged too. An independently retained root is charged its full logical cost;
two owners retaining a shared root both charge it. This conservative accounting
avoids a global alias registry. A transfer can move its reservation when the
previous owner stops retaining it. Release only after that owner's readers and
cleanup finish; cancellation releases rejected work after access quiesces.

Before accepting an output, reserve three times its expanded logical cost:
one share for the retained value, one for logical delivery and one for private
codec scratch. The escaped-string and node charges bound compact encoded work
without generating that encoding. Helpers must keep their charged scratch
within those reservations. Keep them with the queued output. New
input, replay and Graph work cannot spend them. Logical delivery uses that reserved
capacity, then releases SDK reservations when delivery finishes; values retained
by the caller thereafter are outside the invocation budget. This makes draining
accepted logical outputs possible after a resource failure. It does not promise
that every requested Go destination type can be constructed. The expanded-occurrence
charge rejects, for example, thousands of independent image copies before they
are queued, even if their internal source shares a single byte slice.

Live reservations distinguish capacity committed to publicly drainable outputs
from persistent replay, Graph and active work. If the next bounded reservation
fits after those public outputs drain, wait before allocating more, under the
call context and invocation termination. The consumer can drain them using
their already reserved delivery capacity. If draining them cannot make it fit,
fail immediately. Internal Graph queues are not presumed independently drainable;
do not wait on retention whose release may depend on this same blocked work.
Thus a slow public consumer produces backpressure, while an ever-growing replay
or Graph history reaches an explicit failure. No replay eviction is permitted.

An exhausted per-value/depth limit, or live limit that cannot be relieved as
above, terminates the invocation through the existing ERR_RUNTIME channel.
The failing Write/EmitOutput does not accept
its value, no partial result escapes, and resource exhaustion does not trigger
context-resolution retry. The established terminal-race and call-context rules
still apply. Stop new work, quiesce readers and release rejected/internal work;
previously accepted logical outputs drain before the terminal error. A local
resource diagnostic identifies the stage, limit kind and configured allowance;
it contains no payload and is not portable InvocationError.Data. Queue-full
backpressure is unchanged.

### Bounded host construction

Logical-size accounting does not bound a Go destination's storage. A thousand
empty objects can target a slice of structs containing large ignored fixed-size
fields. Every SDK-controlled ordinary construction therefore checks destination
layout and cardinalities before allocating: complete struct/array size, including
ignored fields and array zero-fill; slice backing; map/key/value storage under a
conservative host allocation estimate; pointed-to objects; and temporary encoded
input. Checked arithmetic and the depth limit apply. The construction charge is
the greater of logical work and that host-storage/work estimate, plus separately
live scratch. It uses the configured per-value ceiling. Uncertain ordinary codec
construction requires a safe preflight/bounded decoder or a loud refusal before
allocation; an unchecked codec call is not a resource-limit fallback. Fresh
outer destinations for custom decoders are checked too; allocations performed
inside the supplied callback remain its responsibility.

Internal construction for a local handler or integration runs under the invocation
ledger and fails the invocation before that handoff if its charge cannot fit.
Public typed recovery instead belongs to the caller adapter. Each Read gets a
separate bounded construction allowance using the same configured per-value
ceiling; it does not compete with the invocation's already committed output
reservations. Its source snapshot remains reserved until conversion finishes.
Output streams remain single-consumer, so this does not create unlimited parallel
SDK conversions on one stream. Caller-requested export is likewise a separate
bounded conversion; ordinary caller-owned results need no persistent reservation.

Acceptance guarantees an available stable logical output, not successful decoding
into every Go type. A type mismatch or construction-limit failure consumes that
one logical output and returns no partial destination. It is a distinguishable
local conversion error (with cause/stage available through Go error inspection),
not a retroactive operation failure or retry trigger. Type mismatch keeps its
existing classification; construction exhaustion uses ERR_RUNTIME as its cause.
The caller may continue reading subsequent outputs and their eventual invocation
terminal. This follows the existing typed-wrapper boundary, which already decodes
after removing a logical output and may return a per-value type mismatch. It
does not require destination types to be registered with bindings or transports.

Arbitrary user codec/handler allocations, external-engine internals, Go runtime
overhead and caller-retained results are not sandboxed by this ledger. Adapters
apply their library's own resource and cancellation controls; SDK-controlled
copies, returned logical values and retention still pass the limits above.
Custom decoding receives private bounded encoded input, while its chosen private
host allocation remains the callback's responsibility. No hard-preemption or
process-wide heap claim follows from accepting an application callback.

Admission uses the same policy regardless of container strategy. A codec-backed
alternative can adopt it too. Qualification must cover a long stream with no
early output, simultaneous blocked writers, Graph fan-out/root retention,
duplicate-output expansion, exhaustion during capture, and draining accepted
outputs after failure, slow public output consumers, padded Go destination types,
and conversion failure followed by another logical output. A numeric default may
be tuned using evidence; silent
eviction of accepted replay data or a change in failure behavior is a contract
change, not tuning.

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

The new finite value/retention/depth limits can reject work that previously grew
without an aggregate bound. Document their defaults, overrides, resource error
and accepted-output behavior in the same migration material.

Already owned internal generic values still use direct container access and can
be forwarded read-only. The removal of public aliasing has real copy costs,
including small generic calls; report those against the current baseline rather
than claiming the old zero-copy behavior survives unchanged.

## Design review and current decision

The ownership revision received one A, two A- and three B+ overall from six
fresh readers. All regarded the ownership contract as resolved; aggregate
resource containment remained material. Six readers then assessed the first
resource refinement, giving three A- and three B+. They identified destination
storage expansion and pressure from independently drainable outputs.

The policy above incorporates one corrective refinement for those two findings.
Three new readers assessed that final design together with frozen comparative
evidence. All gave semantics A, Go callers A, boundaries A-, runtime A- and
**overall A-**. None found a material unresolved contract. Remaining deductions
concern projected container costs and maintaining codec-compatible construction.
The review loop stops here rather than treating those tradeoffs as prose defects.

All three find objective mechanism improvements on the tested typed/byte paths,
while leaving overall superiority over stronger A+ and B unresolved. They
support bounded integration, not broad implementation. Evaluator completion or
selection is not a gate; applications supply an adapter through the SDK hook.
Original inputs, reports and grades are preserved in the coordination workspace
at `design/sdk-value-grading/candidate-3/RESULT.md` and
`design/sdk-value-grading/candidate-4/RESULT.md`.

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

The subsequent bounded comparison adds disposable A+/P/B value implementations,
with source frozen before two serial timing runs. The current SDK's typed wrapper
runs against a synchronous in-memory spy. Medians across the two runs were:

| Mechanism | Current A | Owned codec A+ | Projected P | Native reader B |
| --- | --- | --- | --- | --- |
| Small typed capture/recovery | 7.37–7.39 µs | 7.40–7.62 µs | 3.42–3.43 µs | 4.16–4.23 µs |
| Small generic capture/recovery | 0.54–0.55 µs | 4.49–4.53 µs | 3.85–3.87 µs | 4.51–4.60 µs |
| Wide capture/narrow selection | 2.52–2.53 ms | 2.51–2.55 ms | 1.56 ms | 1.62 ms |
| 1 MiB capture/move/recovery | 13.97–13.99 ms | 13.93–14.00 ms | 0.23 ms | 0.22–0.23 ms |

The byte path allocates about 2.10 MB in P/B versus 13.83 MB in A/A+, including
necessary ownership copies. P allocates about 895 KB for the wide fixture versus
B's 639 KB; P has faster ordinary container access. Current A's generic identity
path is much faster than owned capture, a genuine compatibility/performance cost.
A+ can improve by fusing generic classification and snapshotting; its measured
small generic disadvantage is not uniquely architectural.

These are mechanism results, not full invocation results. Timed fixtures pass
their semantic controls and race checks, but both native prototypes have known
embedded-field and case-folding coverage failures outside those fixtures.
No real schema, evaluator, protocol, retry/Graph lifecycle or final ledger is
integrated into the timings. Resource models exercise selected reservation,
destination-layout and pressure rules, not complete lifecycle enforcement.
The earlier engine-specific probe is historical capability evidence only.

The method, raw samples, source audit, counterexamples and final synthesis are
recorded at `design/sdk-value-qualification/RESULT.md`. Experiments used one
host, warmed plans, fixed ordering, pinned temporary assemblies and Go 1.27.1.
There is no retained-heap measurement, project-wide cohort or declared-toolchain
release qualification. The bounded mechanism stage is complete; the integrated
comparison described next remains outstanding.

## Next: one bounded comparison before migration

Freeze exact revisions, payloads and measurement procedure using the ownership
policy above before implementing or comparing candidates. Include a corrected A control
with its actual proposed improvements, not a deliberately weak baseline.

Qualify the SDK's hook contract and actual schema handling: supported logical
values, undefined/error translation, cancellation and result lifetime. Applications
supply evaluator adapters; choosing or completing an engine is not a prerequisite
for this architecture decision. Accommodate both adapters that materialize
logical JSON values and adapters that preserve eligible native leaves. Count each
selected adapter's actual work in invocation measurements. Engine-specific native
optimizations require their own evidence, without becoming mandatory SDK features
or a reason to bundle an evaluator.

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
5. Resource exhaustion in captures, replay, Graph retention and duplicated
   delivery, including preservation of already accepted outputs. Compare like
   budgets and charge their accounting overhead separately from representation.

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
