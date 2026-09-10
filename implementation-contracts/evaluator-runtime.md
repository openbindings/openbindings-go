# Evaluator runtime contract — official SDK implementation

TransformEvaluator.Evaluate accepts context.Context as its first argument. The
named-binding extension does too. This replaces the former context-free SDK
signature; there is no second legacy cancellation capability.

The operation invoker forwards attempt-scoped context to input transforms and
binding-attempt context to output transforms. Cancellation is host control, not
OBI data, a JSONata variable, or context-store state. Evaluators should refuse
pre-cancelled work and check context at cooperative evaluation checkpoints.
The SDK discards a result returned after its evaluation context was cancelled.

Cancellation of a retired input-transform attempt preserves its raw input for
retry. Successfully transformed replay values are not transformed again. An
internal retirement is not an operation transform failure. Existing first-terminal
arbitration and ERR_CANCELLED behavior are unchanged.

Context does not preempt arbitrary Go code. Compilation, codecs and synchronous
builtins may finish their current work before checking cancellation. A deadline
is not a hard CPU/memory sandbox. Native resource limits and caller cancellation
are distinct safeguards; hosts own their operational budgets.

This does not modify Core, any binding specification, JSONata syntax or numerical
policy. Graph's mechanical signature migration passes background context to
preserve its former behavior; Graph lifecycle redesign is explicitly deferred.
