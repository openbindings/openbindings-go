# Invocation value migration qualification

The migration is implemented on this branch and in the coordinated CLI branch
`codex/invocation-value-migration`. Local and Linux implementation checks and measurements
are recorded below. SDK PR: [#115](https://github.com/openbindings/openbindings-go/pull/115);
CLI PR: [#51](https://github.com/openbindings/ob/pull/51). **Release/cohort activation remains blocked by the existing
GraphQL corpus mismatch and the remaining candidate/CI gates.** This is not an
architecture grade or a claim that every workload becomes faster.

## Inputs

- SDK runtime baseline: `71cbd964a8df7a5d2b2981756d351d3a59b86c5d`.
- Plan: `60d2770fa771cc05abec1edb8ce6104ab5bd8a53`.
- Project policy: `6eceb5dc1e8d0e4c1182f230b83fa2080a8e7b3f`.
- Spec corpus: `d7fba38d8c389b165fdeb980ab4b28ea4a72cfde`.
- Interface corpus: `fe526cc5c750a21f4dd61efaa758c35c6af91192`.
- OpenAPI client: `7b15d57e960faa6a9e010eb9cbea80fd83f0d02e`.
- AsyncAPI client: `7326ce4e18c12e1dcb36c3587fbe0f7fe52ec749`.
- CLI baseline: `2a35e399e098c63a3cf75ff24cdb80990f971d3b`.
- Local qualification toolchain: Go 1.25.13, darwin/arm64. Temporary workspaces
  use version-pinned replacements; no module publication is inferred.

## Implementation status

The private value adapter, bounded codec support, shared scope/ownership bridge,
invocation queues, typed and local handoffs, replay retention, terminal data,
configuration entry points and explicit bounded export are implemented.
Operation Graph shares the scope with descendants, charges roots/queued events/
buffer/combine retention, detaches evaluator inputs and named bindings, and uses
the maintained schema compiler. Its FIFO scheduler and completion rules remain.
All eight format constructors propagate limits. Protocol-owned codecs remain at
wire boundaries. Application evaluator injection remains required.

## Verification

- Required-corpus local normal/race runs and the [Linux CI matrix](https://github.com/openbindings/openbindings-go/actions/runs/35603594733) pass for root and seven format modules:
  AsyncAPI, Connect, gRPC, MCP, OpenAPI, Operation Graph and Usage. Corpora are
  required, not silently skipped. All nine modules pass `go vet`.
- GraphQL has exactly the same **162 failing test/package entries** on the
  unchanged runtime baseline and candidate with the pinned corpus: no candidate-
  only or baseline-only failure. The mismatch is existing introspection content
  expectations. The new HTTP exact-number result test passes under the race
  detector. No corpus was edited or disabled to obtain a green result.
- The CLI passes its full race suite and vet with the candidate SDK modules;
  frame/export checks pass after the final CLI changes. Its application-selected
  evaluator wiring is unchanged.
- The real OpenAPI fixture retrieves a PNG and arbitrary octets through the
  default client and raw decode hook, moves the value through an injected
  JSONata transform, recovers identical typed bytes, exports JSON, and uploads
  those bytes again. The existing zero-length HTTP-body rule remains unchanged.
- Snapshot tests cover producer reuse, local/public mutation isolation, duplicate
  leaves, error-data presence/isolation, custom foreign overrides, construction
  refusal followed by a readable next output, terminal draining and Stop.
  Retry and Graph exhaustion tests demonstrate finite retention and zero charged
  owners after workers retire. Input/output evaluator panics also release owners.
- Twenty race-detector repetitions pass for producer reuse, panic retirement,
  finite replay, capture-limit draining and Stop. Ten repetitions of the
  accepted-output/terminal stress test pass in `./invoke`. The preexisting CI
  command incorrectly targeted root, where it ran zero tests; the workflow now
  targets `./invoke` and includes the new ownership tests. Both lanes pass in
  the root Linux CI job.
- Two 15-second differential fuzz runs pass: 329,290 logical-value cases and
  124,875 typed projection/construction cases. These are bounded sampling runs,
  not exhaustive proofs. The maintained codec's race suite and repository vet
  gates pass on Go 1.25.13.

## Measurements

The [reproduction script](scripts/benchmark-value-migration.py) creates disposable
baseline, owned-codec-control and candidate copies. The control keeps generic
owned copying but forces typed projection/construction through the bounded
codec; it uses the candidate's same queues, snapshots, ledger and limits. There
is no production strategy toggle. Default limits are enabled. Run order was
baseline, owned codec, candidate; three 300ms samples per case, Go 1.25.13 on
Apple M1 Max, darwin/arm64. These sequential local samples are directional
measurements, not statistical performance guarantees. Correctness/race runs were
separate; local source hashes and raw samples are in
[`qualification/value-migration`](qualification/value-migration).

Median latency:

| Workload | Old baseline | Owned codec | Candidate |
| --- | ---: | ---: | ---: |
| Small typed local call | 36.7 µs | 47.8 µs | 29.5 µs |
| Small generic local call | 19.4 µs | 34.2 µs | 31.4 µs |
| 1,024 typed records | 11.71 ms | 17.13 ms | 6.18 ms |
| 1 MiB typed byte local call | 30.07 ms | 39.11 ms | 2.06 ms |
| HTTP image + injected text-API JSONata adapter | 46.50 ms | 56.90 ms | 45.97 ms |
| Same HTTP path, raw decode hook | 45.51 ms | 61.62 ms | 41.18 ms |
| Graph fan-out 1 | 10.4 µs | 27.0 µs | 27.0 µs |
| Graph fan-out 8 | 29.6 µs | 75.1 µs | 74.8 µs |

For 1,024 typed records, allocation falls from 9.19 MB to 3.08 MB per call; for
1 MiB typed bytes, from 31.04 MB to 9.81 MB. Small generic allocation grows from
4.45 KB to 11.75 KB; Graph fan-out 8 grows from 16.99 KB to 50.29 KB. The latter
paths are approximately tied with the equivalently owned/accounted control.
The old generic/Graph paths had weaker ownership and no comparable retention
ledger. The measured ownership cost is material and disclosed, not optimized
away. The retained projection advantage is on ordinary typed/byte conversion.

The HTTP transform still goes through the application's selected evaluator's
text API, so its end-to-end result is near baseline. The large prototype HTTP
speedup is **not** reproduced or claimed for this adapter. An alternative
application adapter can remove that engine-specific bridge without an SDK API
change. No universal speedup follows from these results.

The opt-in heap probe holds four 1 MiB results. Queued heap delta is 4.20 MB for
the candidate versus 5.61 MB for the owned codec. After reads, both retain about
4.2 MB of independent public results. Sampled peak deltas are 5.25 MB versus
12.99 MB. These are one GC-assisted observation per lane with 1ms sampling,
not exact peak bounds. The old baseline reuses the same mutable backing buffer
and therefore retains almost no additional heap in this probe; that is a
different ownership guarantee. Retirement drops the allocations; the negative
post-retirement delta also includes release of the original 1 MiB input.

Replay and Graph pressure are qualified by bounded-termination/release tests,
not comparative throughput claims. Broader production traces, larger configured
payloads and stable per-workload tolerances remain release qualification work;
no threshold was selected retrospectively to declare these samples a pass.

## Activation gates

1. Resolve or explicitly qualify the pinned GraphQL spec/implementation mismatch.
   Baseline equivalence establishes attribution, not conformance.
2. The SDK Linux matrix and corrected repeated race lanes have run on SDK
   candidate `9a69a38acb5e14bf7724350ef1db6f233f13ad88`: core and seven formats
   pass; GraphQL fails with the same introspection mismatch. Preserve these
   checks for any runtime revision made to resolve the remaining gates.
3. `verify-openapi-candidate.sh` passes for OpenAPI and Usage with workspaces
   disabled and read-only temporary module files. Core candidate is
   `v0.1.1-0.20260921130534-9a69a38acb5e`; OpenAPI client candidate is
   `v0.0.0-20260918184113-7b15d57e960f`. The coordinated CLI consumer verifier
   also selects all eight format modules at that same SDK candidate and the
   AsyncAPI client at `v0.0.0-20260917182212-7326ce4e18c1`. Its build, vet and
   full short race suite pass without local replacements, recorded in the CLI
   migration report. A final CLI opening-context refusal fix passes the targeted
   frame race suite. Candidate verification does not
   establish a tagged release or a compatible promoted cohort.
4. Review the completed snapshot, finite-accounting and failure/drain contracts
   and workload tradeoffs before merge/release. No release tags, cohort promotion
   or TypeScript parity changes are part of this branch.


## Remaining serialization inventory

| Location | Purpose and disposition |
| --- | --- |
| Core typed/local handoffs and retry packets | Removed ordinary whole-value JSON text bridges; snapshots and direct checked construction replace them. |
| Private value adapter | Whole-value bounded codec fallback only when custom codec behavior or unsupported direct semantics require it; callbacks run once per capture/conversion. |
| Invocation error data | Bounded snapshot and detached observations; portable error encoding is still explicit JSON export. |
| Operation Graph map/results/queues | Ordinary admitted trees and private snapshots; removed `toSlice` marshal/unmarshal fallback. |
| OpenAPI | Native client owns HTTP request/response encoding and binding correspondence; successful byte outputs enter snapshot admission directly. Context/artifact conversion is separate from invocation carriage. |
| AsyncAPI | Message content-type, envelope and native-client wire codecs remain authoritative. |
| GraphQL | HTTP/subscription JSON remains wire encoding; generic result decoding retains number tokens. Introspection artifact conversions are outside invocation carriage. |
| gRPC and Connect | ProtoJSON/protobuf descriptor mapping remains authoritative for presence, bytes, int64, enums and special values. Generic decoding of resulting JSON retains number tokens. |
| MCP | Transport JSON-RPC encoding remains. Removed typed-result marshal/decode bridge; result admission owns the typed fallback. Raw transport capture still preserves extension members and presence. |
| Usage | Flags, arguments, stdin/files and stdout are actual process boundaries. Count flags accept the admitted exact number carrier. |
| CLI | Output formatting is explicit export. The application still injects its evaluator. Its existing evaluator adapters use their engines' encoded-input APIs; changing that engine/adapter contract is outside this migration. |
