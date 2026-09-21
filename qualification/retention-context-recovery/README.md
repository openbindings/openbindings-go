# Retention and caller-owned context recovery qualification

Completed 2026-09-21 on `feat/value-migration-rulings`, following SDK baseline
`61ab7d8a63f74f2228034feda5092b240ad637c2`. This record covers the focused
retention and context-recovery changes; it does not qualify the entire migration,
deferred bindings, another language implementation, or a release.

## Result and responsibility boundaries

Application → core invocation → binding adapter → protocol client → service.

Core owns stable value handoffs, its bounded queues, and cancellation of its
cooperative invocation work. Bounded queues do not establish a total memory
bound: pending captures, blocked producers, stage-held values, intermediates,
scratch and terminal records also retain memory. Bindings and protocol clients
own their additional buffering and resource policies. Accepted output and
terminal records remain readable according to the existing outcome contract;
reclamation follows reachability rather than an immediate-release promise.

Output submitted after a known terminal state now returns the existing outcome
before invoking its codec. The existing check after capture handles termination
that races with a running codec. No aggregate ledger, capture reservation,
producer restriction or public queue-capacity policy was added.

The runnable [recovery example](../../invoke/context_recovery_example_test.go)
keeps one redo under application control. It handles early and late challenges,
reads the authoritative outcome after input closure, and retires every attempt.
The application retains the index returned by `MatchContextAlternative`, scopes
the resolution, and replaces each requested configuration value or credential
whole while preserving unrelated context. Retained leaves may remain shared.

Matching, scoping and credential extraction now agree about valid named and flat
representations. Restricted store eligibility retains the complete challenge's
credential-identity rules. Store-backed resolution preserves original alternative
order across storage keys and caches reads. Invocation cancellation now reaches
cooperative preparation and preflight context resolution as well as execution.
Core gained no automatic live replay or application merge policy.

## Review evidence

Three rounds of independent readers found defects that were corrected before
the final assessment, including whole-object replacement, credential fallbacks,
matching alternatives outside the complete challenge, Basic representation
consistency, preparation cancellation, and storage-key grouping that reordered
alternatives. The array-element path limitation below was documented explicitly.

| Final reader | Method | Grade |
| --- | --- | --- |
| Documentation | Cold read of revision 3, then focused reread of excerpt and path-scope clarifications | A |
| Go correctness | Cold read of revision 3, then focused review of the store-order fix and regression | A |
| Context and responsibilities | New cold read of the final revision | A |

Readers were not given earlier grades or an expected verdict. Follow-up reads
are identified above rather than presented as new cold reads. The final context
reader inspected source and tests; the Go reader also ran focused regressions.
No reader found a remaining material issue within the documented scope. These
grades are scoped judgments, not proof of complete SDK correctness.

## Verification

Go 1.25.13, darwin/arm64, with readonly module dependencies:

- Root SDK and OpenAPI module: `go test -mod=readonly -race -short -count=1 ./...`
  passed with the spec and interface corpora required. Both suites were rerun
  after the final store-order correction.
- Root SDK vet passed after the store-order correction. OpenAPI vet passed
  after the matching, Basic credential and cancellation changes.
- Focused retention and accepted-output race tests passed ten repetitions.
  Recovery, scoping, representation, matching, order and cancellation regressions
  passed. The terminal-codec, invalid-named-credential and preflight-cancellation
  regressions were observed failing before their respective fixes.
- Formatting and diff whitespace checks passed. The final post-review source
  change only corrected a comment to say credential ambiguity is assessed across
  the complete challenge.

Verification inputs:

- Spec corpus: `d7fba38d8c389b165fdeb980ab4b28ea4a72cfde`.
- Interface corpus: `fe526cc5c750a21f4dd61efaa758c35c6af91192`.
- OpenAPI client: `7b15d57e960faa6a9e010eb9cbea80fd83f0d02e`.
- OpenAPI used a temporary workspace selecting this SDK and the existing
  standalone client. No module manifests were changed by this focused pass.

## Remaining work

1. **Preflight contract.** `BindingPreparer` currently prohibits network I/O,
   while OpenAPI preparation can retrieve a remote description for a cold,
   location-only source. Decide the binding-neutral boundary between preparation
   and execution of the requested operation, then align contracts, documentation
   and the OpenAPI implementation. The cancellation correction does not settle
   this policy.
2. **Array-element configuration paths.** Default context helpers and this
   example traverse object members. Whole arrays are supported as values, but
   paths through an array element, such as `/0/url`, require application-specific
   handling. Such pointers pass challenge syntax validation but are not resolved
   by the default helpers. An extension needs coordinated traversal, scoped
   projection and application of selected elements, preserving unrelated caller
   elements without releasing unrelated stored elements. This limitation does
   not restrict invocation value carriage.
