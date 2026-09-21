# Optional operation preparation: implementation qualification

Implemented locally on 2026-09-21 over SDK baseline
`09bad457a5363bca90cf507a7acded7bcb966494`, branch
`feat/value-migration-rulings`. The accepted [preparation contract](../../PREPARATION.md)
keeps the existing public signatures and automatic preparation/resolution lane.
No release, merge, module-manifest or evaluator changes are included.

## Design decision and review

The application may signal that an operation is likely; core routes the call;
the binding chooses useful setup and owns its reusable state. Preparation may
perform I/O, but never executes the requested operation or spends its approval.
Nil reports no known unmet requirement rather than readiness. Required failures
surface; optional acceleration uses the binding's valid normal fallback.
Invocation works without an early call. No token, scheduler, new lifetime, core
cache or readiness flag was added.

The design panel began with independent cold readers for responsibility
boundaries, Go API/ownership, and minimality/performance. Initial grades were
A/A/B+. One revision resolved optional-optimization failure coupling, grounded
reuse in the existing embedded-source cache, and clarified stale-result handling.
Focused rereads by the same readers yielded A/A/A, with no remaining material
design issue. These are design judgments, not implementation review grades or
performance proof. The full local design history remains in the workspace's
`design/sdk-preparation` directory.

## Implementation and checks

The functional correction is in OpenAPI preparation: required load, edition,
selector and analysis failures now return errors; cancelled description loading
returns the cancellation error. Previously those paths could return `nil, nil`.
Early preparation never calls the application's context resolver or dispatches
the selected operation. The routing and ownership mechanisms already existed.

New regressions cover:

- failed retrieval, invalid document and unknown selector (observed failing
  before the fix, passing afterward), plus edition mismatch;
- cancellation while another invocation using the same adapter proceeds
  independently (the cancelled call previously returned successful unknown);
- repeated early preparation retaining embedded analysis, no request before
  invocation, reuse afterward, and normal fallback after that cache is cleared;
- concurrent calls with distinct supplied credential contexts;
- explicit preparation reporting requirements without resolving or invoking,
  followed by normal automatic preparation/resolution;
- required preparation failure preventing execution without permanently
  disabling a later invocation.

Existing tests cover absent preparers, alias and pinned-binding selection,
changed source revisions, bounded native-client caching, prepared-route
forwarding, and live challenge/no-replay behavior.

Go 1.25.13, darwin/arm64, readonly modules:

- Root SDK and OpenAPI: `go test -mod=readonly -race -short -count=1 ./...`
  passed with spec and interface corpora required.
- Root SDK and OpenAPI `go vet -mod=readonly ./...`: passed.
- Focused new/existing preparation regressions: passed with the race detector.
- Interfaces: both revised documents validate against the OBI schema; canonical
  ordering, shared-schema consistency and all eight contract profile checks pass.
- Spec: binding-spec conformance inventory and publication verification pass.
- Project: coordination policy validation passes. Formatting/diff checks pass.

The first local HTTP test attempt was blocked by the filesystem/network sandbox's
socket restriction; rerunning with localhost access exposed the real regressions
above. That infrastructure failure is not counted as a behavioral result.

Qualification used corpus bases `d7fba38d8c389b165fdeb980ab4b28ea4a72cfde`
(spec) and `fe526cc5c750a21f4dd61efaa758c35c6af91192` (interfaces), with
the preparation wording changes in isolated worktrees. Fixtures are unchanged.
OpenAPI used the existing client at `7b15d57e960faa6a9e010eb9cbea80fd83f0d02e`
through a temporary workspace. TypeScript and other bindings were not qualified.

## Small performance comparison

The checked-in `BenchmarkOperationPreparation` compares a fresh adapter per
iteration and the same tiny embedded OpenAPI description plus localhost JSON
operation. Three sequential samples of 500 invocations per lane, without the
race detector, on Apple M1 Max. [Raw output](benchmark.txt).

Medians of the three sample means, in microseconds:

| Lane | Advance work | Click to completion | Combined measured work |
| --- | ---: | ---: | ---: |
| Ordinary invocation | 0.06 | 631.28 | 631.34 |
| Equivalent early native-client setup | 226.99 | 418.35 | 649.95 |
| Early `PrepareOperation` | 235.28 | 414.60 | 640.44 |

Every lane executed exactly one operation request per iteration and no document
retrieval request (the source was embedded). Median columns are calculated
independently and need not sum. The native-setup control deliberately calls the
adapter's private loader to measure equivalent work; it is not another public API.
The component timers exclude adapter construction and fixture creation; Go's
ordinary `ns/op` includes per-iteration construction. The shared default HTTP
transport is reused; this is an analysis-reuse experiment, not a cold TLS test.

This sample supports moving existing analysis work ahead of the click, not a
claim of less total work or superiority over equivalent native setup. Total
work is similar at this scale and the sequential local timing is not a stable
performance guarantee. A generic application gains a common way to request that
advance work without reaching into the adapter's private loader.

Each adapter uses its existing cache bound of 64 client revisions. The tested
source occupies one entry; it is an entry-count policy, not a byte-memory bound.
Resources follow the existing adapter/client lifetime. Location-only sources
still load separately during preparation and invocation; the concurrency test
records those loads explicitly. No new cache policy was introduced to manufacture
a speedup.

## Coordinated contract edits

Companion worktrees on `codex/operation-preparation`:

- `interfaces-preparation`: binding/operation preparation descriptions and
  requested-operation refusal wording; removes unconditional preparation
  idempotency claims. The refreshed branch is pre-launch and has no `LAUNCHED`
  marker, so its version files remain mutable working drafts.
- `spec-preparation`: the four working OpenAPI-family refusal paragraphs
  distinguish description/setup traffic from operation dispatch. No published
  bundles, released snapshots or conformance fixtures changed.
- `project-preparation`: records the accepted decision and supersedes the
  side-effect-free preparation premise of the historical cache ruling, without
  choosing a replacement cache policy.

Existing unrelated edits in other checkouts were not incorporated. This local
qualification does not establish that the coordinated branches have landed or
that a release/cohort has been activated.
