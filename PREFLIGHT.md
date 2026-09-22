# Preflighting an operation

Call `PreflightOperation` when an operation becomes likely to be used, for
example when its button appears, to learn which context is still missing
before the user acts. Supply the context you would supply to `Invoke`.

```go
details, err := invoker.PreflightOperation(screenCtx, iface, sig.Key(),
    invoke.WithContext(given))
```

A non-nil `details` is the same shape a live `CONTEXT_REQUIRED` carries.
Resolve it the way your application resolves challenges: select the satisfied
alternative with `MatchContextAlternative`, scope with `ScopeContext`, and merge
the result into the context you pass to `Invoke`. Merging is application code;
see `invoke/context_recovery_example_test.go`. A nil result means the binding
reported nothing missing; it is not readiness and not a promise that invocation
will succeed. An error means the binding could not answer and predicts nothing
about invocation, so do not disable the control on it; invoke and let the
outcome decide.

Results can be stale. One can arrive after you cancelled `screenCtx`, or after
the selection or context changed; discard it in those cases. Repeated or
overlapping calls are valid; debounce as you would any query.

Explicit preflight never consults your `ContextResolver`, never prompts, and
never retries. Ordinary invocation preflights again before its attempt and does
consult the resolver; a preflight error does not stop that invocation, which
proceeds to its attempt as if preflight had reported nothing, and the outcome
is the attempt's. What preflight does for a given binding, and what it
retains, is in that adapter's README.

A live `CONTEXT_REQUIRED` during invocation ends that invocation with its
details; the caller-owned redo loop is in the README's context section.
