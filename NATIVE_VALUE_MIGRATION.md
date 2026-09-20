# Native value migration plan

Status: planned, 20 September 2026. No runtime change is activated by this
document. Implementation begins with a bounded feasibility experiment.

Branch: `codex/native-value-migration`.
Base: `origin/release/0.2` at
`71cbd964a8df7a5d2b2981756d351d3a59b86c5d`.
The integration destination was checked against `openbindings/project` main
at `6eceb5dc1e8d0e4c1182f230b83fa2080a8e7b3f`. Its catalog and working-loop
records select `release/0.2`; the older `main` instructions in CONTRIBUTING.md
do not select this work's destination.

## Outcome

Make supported native Go operation values behave consistently across admission,
schema validation, transforms, typed calls and explicit JSON export, without
serializing and reparsing merely to cross an internal boundary. Preserve eligible
native leaves, including original image bytes, through shape-only transforms.

Keep the existing package architecture and application-supplied evaluator seam.
This is a migration of value handling, not a replacement SDK or a requirement
that applications adopt a policy engine, delegate manager or storage system.
Applications must remain able to implement authorization, approvals, delegation
and result disclosure around reusable invocation mechanics.

The preferred approach is bounded common logical access in existing packages.
Current codec-backed behavior remains the production baseline while it is
qualified. A projection into ordinary maps/slices retaining native leaves is a
serious comparison alternative. Do not build a general custom-type mapping
registry or a new JSON repository as a prerequisite.

## Responsibility boundaries

| Owner | Migration responsibility |
| --- | --- |
| Root Core package | Document models, references, document validation and schema facade. Preserve syntax validation without selecting an execution engine. |
| `jsonvalue` and private implementation helpers | One logical interpretation of supported carriers; read access, ordinary construction, detachment and explicit export. No dependency on invocation, binding modules or evaluators. |
| `invoke` | Admission order, typed/local bridges, transformation result gates, stable outcomes, ownership and bounded retention. Preserve the evaluator injection interface. |
| `formats/*` | Governing source correspondence, native client adaptation and protocol-specific value meanings. Preserve independent module usability and existing import paths. |
| Optional `sdk` facade | Assemble explicitly supplied providers and evaluator; no default engine or binding imports. |
| Application evaluator adapter | Engine-specific access/conversion, undefined/error translation, closed document-transform environment, result lifetime and integration qualification. |
| Consuming application | Engine choice, authorization, credential-use policy, persistence, UI/export and release of results. |
| Standalone native clients | Protocol loading, request/response and wire mechanics in their own vocabulary, without SDK invocation types. |

Keep ordinary Go caller shapes. A transform-free invocation still needs no
evaluator. Applications select an evaluator once when composing their runtime;
ordinary operation callers do not choose an engine for every invocation.

Start the access work in the existing `jsonvalue` area. Reflection plans,
traversal caches and ownership bookkeeping stay private. The minimal read-view
contract needed by independently versioned adapters will require public API
review before exposure; this plan deliberately does not invent its signatures.
The maintained schema backend and JSON codec remain dependencies, not rewrite
targets. Their adoption does not establish whole-pipeline native coverage.

## Baseline and change inventory

The base already preserves ordinary generic map/slice references in some local
calls. The earlier bounded public-API probe also observed two marshal and two
unmarshal hooks in its matched custom-codec typed fixture. Those counters are
evidence of that fixture's conversions, not a throughput measurement or proof
that its custom codecs can be ignored.

| Current location | Current behavior to address | Intended change and deletion condition |
| --- | --- | --- |
| [typed invocation](invoke/invocation.go), [local providers](invoke/local_provider.go) | Codec bridges outside the generic fast path; output assignment may use host type alone. | Direct read/construction for qualified carriers; native aliasing only when logical interpretation also agrees. Remove the replaced round trips only after caller compatibility tests pass. |
| [transform admission](invoke/operation_invoker.go), [invocation data and errors](invoke/invoker_types.go) | Separate classifiers/codec checks can disagree with schema validation; errors use text for snapshots. | Common complete admission and native detachment. Preserve error isolation and public codes when removing text conversion. |
| [schema facade](compiled_schema.go), [backend](internal/thirdparty/jsonschema/validator.go) | Concrete carrier assumptions throughout validation. | Shared logical access in traversal, scalar constraints, equality, sets and evaluated-member tracking. A front-door conversion or type switch alone is insufficient. |
| [value helpers](jsonvalue/equality.go), [membership](jsonvalue/set.go) | Codec-based detachment and existing exact-number rules. | Preserve exact semantics and no-verdict errors while using the common reader where qualified. Canonical identity remains a different operation. |
| [OpenAPI adaptation](formats/openapi/native_adapter.go) | Concrete input containers and eager byte/string adaptation. | Read qualified inputs natively; expose raw bytes with the binding's logical string interpretation. Cut eager Base64 only when the complete path is qualified. |
| [Graph execution](formats/operationgraph/engine.go), [state](formats/operationgraph/state.go) | Separate validation, truthiness/container assumptions and conversion paths. | Migrate all observations coherently in a later slice, preserving graph semantics and bounded retention. |
| [optional JSONata adapter](invoke/jsonata/evaluator.go) | Existing engine-specific text bridge. | Keep available during migration. Qualify application-owned adaptation separately; retire or relocate this package only with consumer migration and compatibility policy. |

The [codec maintenance record](internal/thirdparty/jsoncodec/MAINTENANCE.md)
governs the adopted implementation. Do not replace it with `encoding/json`
based on the stale experimental wording in the helper README. Correct that
documentation in the relevant implementation slice without expanding its claim
to unqualified evaluator or protocol paths.

## Fixed initial domain

This is the private experiment's domain, narrower than the existing public
codec surface. It is not permission to narrow today's production API silently.

| Carrier | Pilot interpretation or refusal |
| --- | --- |
| Nil interface, pointer, map or slice | Logical null; preserve distinction from missing and from a nonnil empty collection. |
| Nonnil bytes | Canonical padded Base64 logical string. Retain eligible backing; materialize characters when an actual string observation requires them. |
| Nil bytes / nonnil empty bytes | Null / empty string. A binding raw-octet value always follows the binding's string correspondence, including zero octets; physical nil storage must not accidentally turn it into null. |
| Boolean and valid UTF-8 string | Corresponding logical scalar. Other string coverage is refused in the pilot, never silently repaired. |
| Integer, finite float, valid `json.Number` | Current codec-compatible logical value, including float32's own-width representation and exact numeric tokens. No recovery of already-lost caller precision. |
| String-keyed maps, slices, ordinary pointers and structs | Native logical access. Exported fields, exact JSON rename/ignore and `omitempty` match current codec observation. |
| Embedded/conflicting fields, `,string`, custom codec types, `json.RawMessage`, `time.Time`, `big.Int` | Recognize before ordinary reflection and refuse in the pilot. Explicit application conversion is the initial recovery. Broader support requires its own compatibility decision. |
| Engine or binding result view | Explicit durable read access over the six logical kinds, valid at nested edges. An opaque or expired handle is not a successful result. |

Initial typed construction uses fresh structs/maps/slices/pointers with exact
field names and no extra members. Missing leaves zero values; null follows the
documented codec-compatible zero/nil behavior. Check numeric ranges before
assignment. Refuse unknown members, case-only field matches, fixed arrays,
float narrowing and implicit string/number coercion in the pilot. Failure must
not publish a partial destination.

Freeze fixtures to tagged Request/Response with nested ordinary structs,
`[]int`, `map[string]string`, matching generic containers, PNG bytes and the
equivalent Base64 string, nil/empty controls, exact numbers, one explicit result
view, and negative custom-codec/cycle/function/lifetime/resource cases. Use a
nested custom Money example to demonstrate clear refusal before dispatch and
successful explicit caller conversion. Do not expand host-type coverage merely
to make the pilot look complete.

## Work sequence and exit gates

Each numbered stage should produce a reviewable diff and evidence. A gate is
not passed because its API exists or because a test double returned the desired
value. Stage 0 can begin immediately. Stage 1 decides whether the native slice
should proceed; it does not require completion or adoption of a new evaluator.

### 0. Freeze the baseline and fixture ledger

Record exact SDK, native-client, application/evaluator and corpus revisions,
module replacements and toolchain. Bring the existing scenario evidence into
a reproducible implementation fixture lane, retaining original baseline output
separately. Separate five measurements: internal JSON encode/decode, Base64
materialization, required ownership copies, container allocation and real wire
encoding. Preserve a plain logical-value control for each native fixture.

Acceptance: the baseline is reproducible without relying on ambient `go.work`;
generic and typed fixtures represent the same values; unsupported cases and
existing behavior are explicitly recorded. Prior cached Go 1.27.1 results do
not replace verification on the modules' declared toolchain.

### 1. Qualify one real evaluator adapter before broader SDK work

Owner: application/integration fixture, using a pinned existing candidate engine
and minimal experimental value views. Do not commit an engine dependency into
the SDK to run the experiment. Test the existing evaluator seam with the real
adapter; a small standalone fixture can precede full reader implementation.

For bytes `{1, 2}`, require all of these together:

- A field move and nested duplication preserve eligible original backing.
- Logical type is string, length is 4, and equality with `"AQI="` is true.
- Inspecting the string and subsequently moving the value preserves both
  correct observations and eligible backing.
- Missing is distinct from present null; undefined is translated correctly.
- Cancellation is respected at the promised boundary and returned views remain
  usable after evaluation ends.

Compare with ordinary logical-value controls. Record the engine's actual limits
and environment; do not expose host functions to document expressions or infer
byte provenance by matching Base64 strings back to input buffers.

Acceptance: every required behavior passes together. Incorrect or unsupported
behavior stops stages 2 through 4; unavailable evidence leaves the gate open.
Investigate another adapter/engine or reconsider the native target explicitly.
Do not start building a new evaluator to clear this gate.

### 2. Build common access, construction and ownership in isolation

Owner: root SDK, after stage 1. Implement the fixed domain's shared access,
complete admission, ordinary typed construction, detachment and explicit export.
Evaluate minimal public read interfaces with real binding and evaluator callers
before freezing them. No process-global registration or arbitrary constructor
framework.

An application-private experimental assembly selects the native reader,
validator, wrappers and providers together before preparing calls. This is a
private test setup, not a new public `RuntimeOptions` flag. Baseline callers
retain today's behavior. No silent per-value codec fallback or automatic
activation on a provider/evaluator upgrade.

Shared graphs are read-only while retained; detachment produces independent
storage. Integration-owned buffers/views must report a conservative retained
backing charge. A tiny slice into a large buffer or a child retaining an engine
arena is charged for its backing, or detached within bounds, or refused.
Caller-borrowed Go memory has limits the SDK cannot infer from slice headers;
do not claim a heap bound from logical byte/node counters. Count observation,
retention and encoded-output budgets separately.

Acceptance: fixed positive/negative fixtures pass, nested interpretation survives,
aliasing cannot bypass a different logical mapping, failed construction is atomic,
and lifetime/resource behavior is demonstrated. Original-byte retrieval is a
separate explicit operation from requesting a logical string or typed assignment.

### 3. Integrate validation and one typed/local invocation path

Owner: root SDK. Route admission, compiled validation, transform result checks,
typed calls, local providers and error snapshots through the same interpretation.
Preserve input validation before input transform and output transform before
output validation. Complete admission still runs when a schema is absent.

Validation includes all governing keywords for admitted carriers: string length
and pattern, exact equality, `const`, `enum`, `uniqueItems`, object/array traversal
and evaluated-member bookkeeping. An inability to observe/validate is not a
negative instance verdict. Keep schema reference closure and `format` annotation
behavior. Necessary error snapshots become native copies, not shared mutable
references.

Acceptance: the ordinary typed/local fixture completes without conversion-only
JSON round trips; logical controls agree across each consumer; no-schema invalid
values fail; validation and error lifecycle regressions stay green. Show the
caller code, including custom-type refusal and explicit recovery. Do not claim
Graph or protocol-wide coverage from this slice.

### 4. Qualify the real OpenAPI PNG path and compare total cost

Owner: Go OpenAPI adapter plus separately coordinated native-client/application
work where needed. Inspect the actual client result and request surfaces first;
an adapter cannot restore backing after a lower layer discarded it. Any needed
client change must remain useful in its native API and must not import SDK view
or invocation types.

Exercise actual HTTP raw PNG response, binding correspondence, real JSONata
field movement, schema validation, retained byte retrieval, explicit JSON export
after termination and a reverse raw-byte request. Include empty bodies, literal
Base64 controls, incompatible mappings and response-buffer reuse. Required JSON
request/response encoding remains at genuine protocol boundaries.

Measure matched complete paths under the current implementation, the proposed
reader and container projection retaining leaves. Include validation, typed
construction, adaptation, retention and requested export; report latency,
allocation, peak/retained memory and conversion counts. Use small ordinary values,
large images and nested values, with no-transform and string-observing transforms.
Freeze fixture sizes and measurement procedure before comparing candidates.

Acceptance: faithful complete behavior and a documented caller/performance
benefit that justifies the added maintenance. No universal speed claim follows
from fewer codec calls. If projection is simpler and meets actual requirements
at better total cost, reconsider the strategy before expansion. Performance
thresholds and supported workload claims are chosen from this evidence, not
invented as unmeasured release promises.

### 5. Expand lifecycle and protocol coverage in separate slices

Owner: affected SDK modules, only after stage 4 justifies continuation.

| Slice | Required evidence before enabling its native path |
| --- | --- |
| Context retries and replay | Accepted post-transform values are not transformed twice; replay is complete; finite byte/node retention; cancellation and terminal races preserve delivery semantics. |
| Streams | Stable output after subsequent buffer reuse and termination; backpressure, cancellation, bounded aggregate retention and no silent drops/early flushes. |
| Operation Graph | All graph reads, filters, equality, validation and nested dispatch use the declared meanings; no-verdict is not false; cancellation propagation and intermediate retention are qualified. |
| Usage and MCP | Native text/JSON decode boundaries and declared correspondence remain intact; native access does not eliminate required decoding. |
| gRPC and Connect | Preserve incorporated ProtoJSON meanings, including numeric host carriers that logically represent strings. |
| AsyncAPI and GraphQL | Preserve each supported family/encoding's own correspondence, framing, outcomes and resource behavior; no blanket byte/string conversion rule. |

Keep a per-path coverage ledger. An unmigrated path remains explicitly legacy;
there is no promise of a globally native SDK until it is true. Do not re-open
accepted cache or import-path decisions as incidental cleanup.

Application extensibility is a compatibility check, not a new SDK feature list.
Verify that protected dispatch and result exposure can be wrapped and that
governed Graph child calls can reach application control. Effective native
request enforcement and Graph's concrete child-invoker dependency may need
narrow seams when a real caller demonstrates the need. Keep policy semantics,
approvals, credential authority and durable job management outside this migration.

### 6. Choose the public migration and activate a qualified version

Owner: SDK API/release work, with downstream consumers. The private pilot cannot
become the production default while its narrower coverage silently breaks
existing custom codecs, struct rules, string values or protocol mappings.

Publish a before/after compatibility matrix covering observable carrier types,
custom codec invocation, tags and field selection, null/missing, numbers/strings,
mutation/lifetime, validation outcomes, errors and explicit export. Decide the
minimal public access API and the final construction behavior from real caller
examples. Reuse current invocation signatures wherever possible.

If necessary differences remain, use a documented versioned migration with
explicit application conversion or a separately selected legacy configuration.
Do not hide a semantic choice in per-message fallback. Compatibility changes
must fit the pre-1.0 version policy; no version number is selected by this plan.

Acceptance: affected root/format modules and exact downstream application
cohort pass; documentation and Changed/Removed entries explain all breakage;
the chosen default and previous supported release are unambiguous. Release
tags, publication and cohort promotion remain separate actions.

## Independent cleanup and cross-repository dependencies

These are separable work items, not gates requiring a larger rewrite:

- The standalone OpenAPI client currently imports SDK JSON helpers for a small
  amount of generic encoding/number handling. Investigate narrow local helpers
  that preserve the maintained behavior. Extract a neutral shared dependency
  only if demonstrated reuse outweighs the maintenance cost. This belongs in
  that client's own branch, not a silent SDK-side replacement.
- The existing optional evaluator adapter can stay while applications migrate.
  If moving its used bridge into ob, preserve current engine and Graph routing,
  test there, then retire the SDK package under normal compatibility rules.
  Adapter packaging does not decide evaluator selection ownership.
- Document parsing, explicit JSON transport/export, canonical hashing and
  persistence have legitimate encoding needs. Their code is not removed merely
  because it calls a codec.
- TypeScript implementation is deferred. Record logical outcomes and boundary
  cases in reusable fixtures so a later implementation can follow them without
  copying Go reflection, pointer or concurrency mechanisms.

The evaluator work proceeding elsewhere is not an all-or-nothing prerequisite.
The exact requirement is a qualified real adapter for stage 1. Baseline planning,
fixture capture and independent boundary cleanup can proceed without it. Shared
reader rollout and byte-preserving transform claims cannot bypass that gate.

## Verification and rollout discipline

Use increasing scope as each slice warrants it:

1. Focused semantic, lifetime, caller-recovery and negative-control tests for
   the changed boundary, with instrumented conversion counters where relevant.
2. Root module `go vet ./...` and `go test -race -short ./...`, using the declared
   toolchain and required spec/interface corpora. Preserve the dedicated
   accepted-output/terminal race lane from [CI](.github/workflows/ci.yml).
3. The same checks in each affected format module, then all eight modules before
   a shared value-path activation. Use explicit workspace/module selections;
   the root test command does not include nested Go modules.
4. Exact source-pinned native-client and application integration, including real
   evaluator/HTTP examples and comparison measurements. Corpus locators use
   `OB_CORPUS_REQUIRED=1`; missing corpora are failures, not skipped evidence.
5. Before release, perform the independent candidate and external-consumer
   checks in [RELEASING.md](RELEASING.md), in the documented dependency order.

SDK tests may inject small evaluators to isolate SDK mechanics. They must label
that evidence accordingly. Language fidelity and engine integration are tested
with the actual application-selected engine, without creating a default engine
dependency in the root SDK. A supplied context is not proof that arbitrary
foreign callbacks are hard-preemptible.

Keep experimental changes isolated until a complete path passes. Land additive
helpers only when they have a demonstrated consumer and preserve baseline
behavior; otherwise keep them on the experiment branch. Delete an old conversion
path in the same reviewed activation slice that supplies its qualified
replacement. Before activation, stopping the experiment leaves production
unchanged. After activation, recovery uses a previously qualified package/cohort
or a documented configuration, never an automatic retry of a possibly executed
operation.

## First implementation checkpoint

- [x] Create isolated branch from the declared integration line.
- [x] Record architecture, fixed pilot scope, code seams and stage gates.
- [ ] Freeze implementation fixtures and exact baseline dependencies (stage 0).
- [ ] Qualify one pinned engine/adapter against the complete native observation
  and carriage gate (stage 1).
- [ ] Proceed to the isolated reader/validation/local/OpenAPI slice only on pass.

This plan has not received the earlier architecture decision's cold-review
grade. No runtime implementation, production activation, benchmark result or
engine selection is claimed by these checked planning items.
