# Optional operation preparation

`PrepareOperation` gives the selected binding an opportunity to get ready for
a possible invocation and report known missing context. An application can call
it when an operation becomes likely, such as when displaying its button.

The application chooses the timing. Core resolves and routes the operation.
The binding and its protocol client decide what work is useful and own reusable
state. Preparation can load information, establish connections, analyze a
description, or do nothing. It must not execute the requested operation, consume
its input stream, or spend an approval for that operation.

```go
// iface and given are stable snapshots; sig is the operation's typed signature.
details, err := invoker.PrepareOperation(screenCtx, iface, sig.Key(),
    invoke.WithContext(given))
// Handle err or display details using the application's own UI policy.

// Later, when the user chooses to run it:
call := invoke.Invoke(runCtx, invoker, iface, sig, invoke.WithContext(given))
defer call.Cancel()
// Use the normal write, close and output-reading pattern.
```

An explicit early call is optional. Ordinary invocation uses the same
preparation hook, resolves known requirements through its configured resolver,
and then starts one execution attempt. An error returned by that preparation
ends invocation before execution. A live context challenge ends the attempt;
the application owns any redo. Explicit preparation itself never calls the
context resolver, prompts, or automatically retries.

The result has the existing shape: `(*ContextRequiredDetails, error)`. Details
describe known missing context. `nil, nil` reports no known missing requirement;
it can mean nothing was knowable or useful to prepare. It is not a readiness or
future-success guarantee. Required setup failures return an error. Optional
acceleration uses the binding's valid normal fallback, so failure to warm an
optional cache does not add a prerequisite to an otherwise usable operation.
Core does not classify or suppress adapter errors.

Preparation may perform I/O and is not a promise of no external effects or
idempotency. The binding documents its work and observes application-configured
client policies. Calling it repeatedly, concurrently, or not at all must remain
valid. Invocation rechecks what it needs; preparation does not reserve a call,
pin future binding selection, or guarantee cache residency.

Use the preparation call's context for its lifetime. Cancellation is cooperative
and must not cancel a later independent invocation. Completed reusable state
belongs to the adapter/client's existing bounded retention and cleanup policy;
there is no preparation handle or detached per-call task. A binding lacking an
appropriate resource owner leaves that acquisition to invocation. Existing
shared connection management may continue under its ordinary client lifetime.

Keep supplied context stable during the call. Preparers must not mutate or
retain caller-owned mutable containers. Retained state must respect source,
target and context distinctions, and must not reuse non-durable context across
attempts. If an early result belongs to an obsolete selection or context,
discard it before updating the UI or resolving requirements. Cancellation alone
does not prevent a late result. Do not turn a non-durable approval into stored
context for a later click.

`PrepareBinding` exposes the same capability for a resolved binding. Existing
`Preflight` methods on prepared routes and `LocalPreflight` callbacks follow
the same contract. Static `PreparedInterface` and provider compilation remain
separate; this change adds no lifecycle, core cache or scheduler.

The OpenAPI adapter can reuse the existing native-client cache for qualifying
self-contained embedded JSON descriptions. Location-only descriptions still
load afresh during preparation and invocation. Required retrieval, edition and
selection failures are returned. An early call never sends the selected
operation request. Reusing work may reduce the wait after a click, but does not
establish lower total work or a universal performance improvement.
