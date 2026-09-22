# Invocation data flow across languages

This document records the behavior implemented by the Go invocation value
migration. It is a model for future language implementations, not a change to
Core or any binding specification. It supersedes the earlier workspace proposal
about generic readers and configurable mapping registries.

## Responsibilities

Applications select providers and transform evaluators and own policy,
authorization, delegation, persistence and presentation. The SDK owns
OpenBindings documents, operation selection, preflight, invocation lifecycle,
validation order, context preflight and binding contracts. Binding adapters preserve
their governing protocol correspondence. Protocol implementations own actual
wire encoding and decoding. A private value implementation supplies logical
meaning and ownership within these boundaries; callers need no new value wrapper.
For service-backed operations the flow is application → core invocation →
binding adapter → protocol client → service. Shared SDK handoff machinery does
not prescribe a binding's internal buffering or protocol resource policy.

```mermaid
flowchart LR
    App[Application values] --> Capture[Snapshot and admit]
    Capture --> Input[Input validation]
    Input --> Transform[Application-supplied transform]
    Transform --> Binding[Binding adapter]
    Binding --> Protocol[Protocol client and wire codec]
    Protocol --> Response[Admit response value]
    Response --> OutTransform[Application-supplied output transform]
    OutTransform --> Validate[Output validation]
    Validate --> Result[Detached application result]
    Result --> Export[Optional JSON export]
```

## Observable contracts

1. A successful public handoff records a stable logical value before returning.
   Producers can then reuse mutable storage. They must not mutate that storage
   concurrently with admission. Internal immutable storage can be shared.
   Concurrent producers are allowed; successful sequential submissions from one
   producer preserve delivery order, while cross-producer and overlapping-call
   order is unspecified. Acceptance does not guarantee delivery or success.
2. Public results, local handlers and application callbacks receive detached
   mutable values. They remain valid after invocation retirement. Duplicate
   mutable occurrences do not expose internal aliasing.
3. Validation, field access, transforms, construction and export agree on logical
   meaning. Preserve missing versus null, empty values and supported exact
   numeric/string meaning. Protocol rules decide how wire values enter that
   domain. A byte value can have Base64 string meaning while retaining native
   byte storage privately; field relocation must still allow exact byte recovery.
4. Custom host codecs are interpreted once at admission when needed. Do not
   guess an incompatible reflection mapping or silently bypass user methods.
   Ordinary values need no intermediate JSON text. Wire codecs, custom codec
   interpretation, explicit export and evaluator-specific text APIs may encode.
5. Evaluator selection belongs to the application. A missing evaluator for a
   required transform fails explicitly. Hooks receive ordinary logical values;
   the SDK never depends on a particular evaluator's private representation.
6. Per-value and depth allowances constrain SDK-controlled capture, delivery,
   construction and export; they are not exact heap-byte limits or controls on
   arbitrary user-code allocation. SDK handoff queues are bounded and park
   producers when full. Queue bounds do not bound total invocation retention:
   pending handoffs, pipeline values, scratch and terminal records also retain
   data. There is no aggregate live budget. Bindings and applications own any
   additional buffering and resource policies; core handoffs introduce no
   unbounded internal producer queue or growing value history.
7. The accepted output prefix drains before terminal failure. Explicit consumer
   abandonment cancels the invocation without discarding its terminal. A value
   a caller handed over is accepted into exactly one attempt and never replayed;
   a live `CONTEXT_REQUIRED` terminates the invocation with its details for the
   caller to resolve and invoke again.
   Applicable cancellation or termination wakes queue waits. A known closed or
   terminal state prevents unnecessary capture before user codecs run, without
   holding a state lock across capture or callbacks. This does not preempt a
   codec already running. Completion and cancellation do not discard accepted
   outputs or terminal data; retained handles may keep them reachable.
8. Public typed conversion validates shape/range and constructs a fresh result.
   Failure exposes no partial object, consumes that output only, and leaves
   subsequent output/terminal reads usable. Portable invocation errors remain
   separate from process-local resource and conversion detail.
9. Explicit JSON presentation stays straightforward. Invocation never requires
   the application to pre-serialize inputs or decode intermediate JSON text.

Go reflection, queue capacities, container projection and private byte types
are implementation choices. Another language can use its native mechanisms if
it establishes the same observable guarantees. No TypeScript implementation or
cross-SDK cohort qualification is implied by the Go work.
