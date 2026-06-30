package openbindings

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/formattoken"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// maxContextRounds caps CONTEXT_REQUIRED resolve-and-retry rounds per
// invocation. A binding that keeps challenging after resolution is either
// mis-declaring its requirements or being fed an insufficient resolver;
// surfacing beats looping.
const maxContextRounds = 3

// BindingSelector determines which binding to use for an operation.
// Returns the binding key and the binding entry, or an error.
type BindingSelector func(iface *Interface, opKey string) (string, *BindingEntry, error)

// TransformEvaluator evaluates transform expressions (e.g., JSONata) against input data.
// Implementations are provided by callers to keep the core SDK dependency-free.
type TransformEvaluator interface {
	Evaluate(expression string, data any) (any, error)
}

// ErrTransformUndefined is the sentinel evaluators return (possibly wrapped)
// when an expression evaluates to no result — JSONata's "undefined", which
// Go's any cannot distinguish from JSON null. Consumers that must tell the
// two apart (the operation-graph engine fails a node with TRANSFORM_UNDEFINED
// on an undefined result, while null flows downstream) detect it with
// errors.Is.
var ErrTransformUndefined = errors.New("transform produced no result (undefined)")

// TransformEvaluatorWithBindings extends TransformEvaluator with support for
// additional named bindings (e.g., $input in operation graph transforms).
// Invokers that need extra context check for this interface via type assertion.
type TransformEvaluatorWithBindings interface {
	TransformEvaluator
	EvaluateWithBindings(expression string, data any, bindings map[string]any) (any, error)
}

// ContextResolver resolves a CONTEXT_REQUIRED challenge into context data.
// Returning (nil, nil) declines, at which point the challenge surfaces to
// the caller unchanged. Composition-time wiring: whether it consults a
// context store, reads an env var, prompts a keychain, or returns a
// hardcoded value is the resolver's business — invisible to the invoker and
// to bindings.
//
// A CONTEXT_REQUIRED challenge is a scope, not a hint. A resolver MUST return
// only the context that satisfies the selected alternative — the credentials it
// names plus non-secret configuration — and MUST NOT return other stored
// credentials. ScopeContext is the reference reduction.
type ContextResolver func(ctx context.Context, details *ContextRequiredDetails) (map[string]any, error)

// OperationInvoker is the operation-layer invoker: it resolves an OBI
// operation to a binding (OBI-T-12 name resolution, OBI-T-09 selection) and
// returns a cardinality-agnostic Invocation handle.
//
// Between the caller and the binding it enforces the operation contract:
//   - OBI-T-07: every caller input validates against the operation's input
//     schema BEFORE the input transform; a failure is terminal and rejects
//     the offending Write with the same error.
//   - inputTransform / outputTransform evaluate per message (JSONata 2.0).
//   - OBI-T-08: each (transformed) output validates against the output
//     schema before it is emitted; a failure is terminal and the value is
//     not emitted. Callers that need to inspect unvalidated payloads call
//     InvokeBinding directly.
//   - CONTEXT_REQUIRED negotiation: challenges raised by the binding before
//     any input was consumed are resolved via ContextResolver and the
//     binding is re-driven against the same input buffer (the
//     already-forwarded prefix is replayed). Once the binding shows
//     observable progress (a first output), challenges surface instead.
//
// Wiring errors (unknown operation, binding, or source; no invoker for the
// format) surface as an already-errored handle with a local wiring code
// (ErrCodeOperationNotFound, ErrCodeBindingNotFound, ErrCodeUnknownSource).
type OperationInvoker struct {
	BindingSelector    func(*Interface, string) (string, *BindingEntry, error)
	TransformEvaluator TransformEvaluator
	// ContextResolver resolves CONTEXT_REQUIRED challenges raised by
	// bindings. When nil, or when it declines, the challenge surfaces to the
	// caller as an ordinary terminal *InvocationError.
	ContextResolver ContextResolver

	invoker *combinedInvoker
}

// NewOperationInvoker creates an OperationInvoker from one or more BindingInvokers.
// Registration order matters: first registration wins for a given format name.
func NewOperationInvoker(invokers ...BindingInvoker) *OperationInvoker {
	return &OperationInvoker{
		invoker: CombineInvokers(invokers...).(*combinedInvoker),
	}
}

// AddBindingInvoker registers an additional BindingInvoker after construction.
// This is useful when an invoker depends on the OperationInvoker itself,
// which creates a circular dependency that cannot be resolved at construction time.
// Must be called during initialization, before any concurrent use of the invoker.
func (e *OperationInvoker) AddBindingInvoker(invoker BindingInvoker) {
	e.invoker.add(invoker)
}

// WithRuntime returns a shallow copy of the invoker with the given
// ContextResolver. The copy shares the underlying combined invoker with the
// original but has an independent resolver. A nil argument inherits the
// original's resolver.
func (e *OperationInvoker) WithRuntime(resolver ContextResolver) *OperationInvoker {
	cp := &OperationInvoker{
		BindingSelector:    e.BindingSelector,
		TransformEvaluator: e.TransformEvaluator,
		ContextResolver:    resolver,
		invoker:            e.invoker,
	}
	if cp.ContextResolver == nil {
		cp.ContextResolver = e.ContextResolver
	}
	return cp
}

// Formats returns all formats registered with this invoker.
func (e *OperationInvoker) Formats() []FormatInfo {
	return e.invoker.Formats()
}

func (e *OperationInvoker) availableFormats() map[string]bool {
	m := make(map[string]bool)
	for _, f := range e.invoker.Formats() {
		m[f.Token] = true
	}
	return m
}

// InvokeBinding routes a binding invocation directly to the matching
// BindingInvoker by source format, without operation-layer validation,
// transforms, or context negotiation.
func (e *OperationInvoker) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	return e.invoker.InvokeBinding(ctx, args)
}

// PrepareBinding is the side-effect-free preflight for a resolved binding
// (binding-invoker role prepareBinding).
func (e *OperationInvoker) PrepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	return e.invoker.prepareBinding(ctx, args)
}

// run drives the binding-layer invocation(s) behind one caller-facing
// handle: an input pump forwarding (transformed) caller inputs, an output
// loop forwarding (transformed, T-08-validated) binding outputs, and the
// CONTEXT_REQUIRED resolve-replay-retry machinery between attempts.
func (e *OperationInvoker) run(
	ctx context.Context,
	caller *InvocationImpl[any, any],
	iface *Interface,
	op *Operation,
	binding *BindingEntry,
	bindingKey string,
	source *Source,
	initialContext map[string]any,
) {
	if (binding.InputTransform != nil || binding.OutputTransform != nil) && e.TransformEvaluator == nil {
		caller.FireError(&InvocationError{
			Code:    ErrCodeTransformError,
			Message: fmt.Sprintf("%v: binding %q has a transform", ErrNoTransformEvaluator, bindingKey),
		})
		return
	}

	// OBI-T-08: compile the output schema once per invocation.
	var compiledOutput *jsonschema.Schema
	if op.Output != nil {
		defs := buildSchemaDefs(iface.Schemas)
		compiled, err := compileExampleSchema(op.Output, defs)
		if err != nil {
			caller.FireError(&InvocationError{
				Code:    ErrCodeValidationFailed,
				Message: fmt.Sprintf("openbindings: output schema compilation failed for %q: %v", bindingKey, err),
			})
			return
		}
		compiledOutput = compiled
	}

	contextData := initialContext
	bindingArgs := func() *BindingInvocationArgs {
		a := &BindingInvocationArgs{
			Source: InvocationSource{
				Format:   source.Format,
				Location: source.Location,
			},
			Ref:         binding.Ref,
			Binding:     binding,
			Context:     contextData,
			Interface:   iface,
			InputSchema: op.Input,
		}
		if source.Content != nil {
			a.Source.Content = source.Content
		}
		return a
	}
	mergeResolved := func(resolved map[string]any) {
		merged := make(map[string]any, len(contextData)+len(resolved))
		for k, v := range contextData {
			merged[k] = v
		}
		for k, v := range resolved {
			merged[k] = v
		}
		contextData = merged
	}

	// Preflight (binding-invoker role prepareBinding): collapse
	// knowable-upfront context challenges into the clean no-input-consumed
	// case before anything is forwarded.
	details, err := e.invoker.prepareBinding(ctx, bindingArgs())
	if err != nil {
		caller.FireError(wireError(err))
		return
	}
	if details != nil {
		resolved, rerr := e.resolveContext(ctx, details)
		if resolved == nil {
			challenge := NewContextRequiredError("openbindings: binding requires context", details)
			if rerr != nil {
				// Distinguish "no credentials" from a broken resolver.
				challenge.Message = fmt.Sprintf("openbindings: binding requires context (resolver failed: %v)", rerr)
			}
			caller.FireError(challenge)
			return
		}
		mergeResolved(resolved)
	}

	// Inputs already forwarded to the binding, post-transform, recorded for
	// replay while the retry window is open. The window closes at the
	// binding's first output (observable progress: by the binding contract,
	// CONTEXT_REQUIRED precedes any side effect, so a challenge after
	// output cannot be retried safely).
	var (
		retryMu       sync.Mutex
		replayLog     []any
		retryEligible = true
	)
	closeRetryWindow := func() {
		retryMu.Lock()
		retryEligible = false
		replayLog = nil
		retryMu.Unlock()
	}
	recordIfEligible := func(v any) {
		retryMu.Lock()
		if retryEligible {
			replayLog = append(replayLog, v)
		}
		retryMu.Unlock()
	}
	snapshotReplay := func() []any {
		retryMu.Lock()
		defer retryMu.Unlock()
		out := make([]any, len(replayLog))
		copy(out, replayLog)
		return out
	}

	headerForwarded := false
	forwardHeader := func(inner Invocation[any, any]) {
		if headerForwarded {
			return
		}
		headerForwarded = true
		// Bounded by the invocation ctx: our impl returns a settled header
		// even under a cancelled ctx (non-blocking-first), and a foreign impl
		// that never settles cannot hang the run loop.
		if md, err := inner.Header(ctx); err == nil {
			_ = caller.SetHeader(md)
		}
	}
	forwardTrailer := func(inner Invocation[any, any]) {
		// Every call site cancels (or has observed the terminal of) the
		// inner first; the recover guards foreign Invocation impls whose
		// Trailer panics on a not-yet-settled terminal race.
		defer func() { _ = recover() }()
		if t := inner.Trailer(); len(t) > 0 {
			caller.SetTrailer(t)
		}
	}

	rounds := 0
	for {
		// innerCtx bounds this attempt's binding: it cancels when the caller
		// handle terminates (cancel propagation) and when the attempt ends.
		innerCtx, innerCancel := DoneContext(ctx, caller.Done())
		inner := e.invoker.InvokeBinding(innerCtx, bindingArgs())

		// The input pump reads the caller's buffer under attemptCtx so a
		// retry swap can unpark it WITHOUT consuming an in-flight input.
		// The replay prefix is snapshotted BEFORE the attempt runs: this
		// attempt's own first output closes the retry window and clears the
		// shared log, which must not race the replay.
		attemptCtx, attemptCancel := context.WithCancel(innerCtx)
		replay := snapshotReplay()
		pumpDone := make(chan struct{})
		go func() {
			defer close(pumpDone)
			// The pump calls foreign code (transform evaluators, third-party
			// inner Invocation impls) on its own goroutine; the run
			// goroutine's recover cannot reach it. Same no-process-kill
			// promise applies here.
			defer func() {
				if r := recover(); r != nil {
					inner.Cancel()
					caller.FireError(&InvocationError{
						Code:    ErrCodeRuntime,
						Message: fmt.Sprintf("openbindings: invoker panic: %v", r),
					})
				}
			}()
			e.pumpInputs(attemptCtx, innerCtx, caller, inner, binding, bindingKey, iface,
				replay, recordIfEligible)
		}()

		surface, retryChallenge := e.runOutputs(
			innerCtx, caller, inner, binding, bindingKey, iface,
			compiledOutput, closeRetryWindow, forwardHeader,
			func() bool { retryMu.Lock(); defer retryMu.Unlock(); return retryEligible },
		)
		var retryDetails *ContextRequiredDetails
		if retryChallenge != nil {
			retryDetails = ContextRequiredFrom(retryChallenge)
		}

		// Retire this attempt's pump before deciding next steps: the
		// caller's input buffer must have exactly one reader at a time.
		attemptCancel()
		<-pumpDone

		if retryDetails != nil && rounds < maxContextRounds {
			resolved, _ := e.resolveContext(ctx, retryDetails)
			if resolved != nil {
				mergeResolved(resolved)
				rounds++
				innerCancel()
				continue
			}
		}
		if retryDetails != nil && surface == nil {
			// Decline or cap exhausted: surface the binding's ORIGINAL
			// challenge (its message is the human-readable part).
			surface = retryChallenge
		}

		// Ensure the inner is terminal before metadata reads (idempotent
		// no-op on the clean-close and already-errored paths).
		inner.Cancel()
		forwardHeader(inner)
		forwardTrailer(inner)
		if surface != nil {
			caller.FireError(surface)
		} else {
			caller.CloseOutput()
		}
		innerCancel()
		return
	}
}

// pumpInputs forwards caller inputs to the current binding attempt: first
// the replayed prefix, then live messages. It exits when the caller closes
// input (forwarding the close), when the attempt ends (attemptCtx), or when
// the inner invocation terminates.
func (e *OperationInvoker) pumpInputs(
	attemptCtx, innerCtx context.Context,
	caller *InvocationImpl[any, any],
	inner Invocation[any, any],
	binding *BindingEntry,
	bindingKey string,
	iface *Interface,
	replay []any,
	record func(any),
) {
	writeInner := func(v any) (stop bool) {
		if err := inner.Write(innerCtx, v); err != nil {
			var ie *InvocationError
			if asIE(err, &ie) && ie.Code == ErrCodeInputClosed {
				// The binding closed its input side deliberately (no-input /
				// unary / read-enough): propagate so further caller writes
				// reject, and stop forwarding. Outputs continue to flow.
				_ = caller.CloseInput()
			}
			// Inner terminal: if a retry follows, the value is in the
			// replay log; the output loop owns reporting.
			return true
		}
		return false
	}

	for _, v := range replay {
		if writeInner(v) {
			return
		}
	}

	for {
		// Read from the caller's binding-side buffer. attemptCtx cancellation
		// unparks WITHOUT consuming, so no input is lost across a retry swap.
		v, err := caller.ReadInput(attemptCtx)
		if err == io.EOF {
			_ = inner.Close()
			return
		}
		if err != nil {
			return // attempt retired, or caller terminal (output loop reports)
		}

		if binding.InputTransform != nil {
			transformed, terr := applyTransformRef(e.TransformEvaluator, iface.Transforms, binding.InputTransform, v)
			if terr != nil {
				inner.Cancel()
				caller.FireError(&InvocationError{
					Code:    ErrCodeTransformError,
					Message: fmt.Sprintf("openbindings: input transform failed for %q: %v", bindingKey, terr),
				})
				return
			}
			v = transformed
		}

		record(v)
		if writeInner(v) {
			return
		}
	}
}

// runOutputs consumes one binding attempt's outputs, forwarding them
// (transformed, T-08-validated) to the caller. Returns a terminal error to
// surface, or retry details for a resolvable CONTEXT_REQUIRED challenge.
func (e *OperationInvoker) runOutputs(
	innerCtx context.Context,
	caller *InvocationImpl[any, any],
	inner Invocation[any, any],
	binding *BindingEntry,
	bindingKey string,
	iface *Interface,
	compiledOutput *jsonschema.Schema,
	closeRetryWindow func(),
	forwardHeader func(Invocation[any, any]),
	retryEligible func() bool,
) (surface, retryChallenge *InvocationError) {
	out := inner.Outputs()
	for {
		v, err := out.Read(innerCtx)
		if errors.Is(err, io.EOF) {
			return nil, nil // clean end
		}
		if err != nil {
			ie := AsInvocationError(err)
			if ContextRequiredFrom(ie) != nil && retryEligible() && e.ContextResolver != nil {
				return nil, ie
			}
			return ie, nil
		}

		closeRetryWindow()

		data := v
		if binding.OutputTransform != nil {
			transformed, terr := applyTransformRef(e.TransformEvaluator, iface.Transforms, binding.OutputTransform, data)
			if terr != nil {
				inner.Cancel()
				return &InvocationError{
					Code:    ErrCodeTransformError,
					Message: fmt.Sprintf("openbindings: output transform failed for %q: %v", bindingKey, terr),
				}, nil
			}
			data = transformed
		}

		// OBI-T-08: an invalid output is not emitted; the invocation
		// terminates. Per-item for streaming bindings.
		if compiledOutput != nil {
			if verr := compiledOutput.Validate(data); verr != nil {
				inner.Cancel()
				lines := splitSchemaError(verr)
				return &InvocationError{
					Code:    ErrCodeValidationFailed,
					Message: fmt.Sprintf("openbindings: output validation failed for %q: %s", bindingKey, strings.Join(lines, "; ")),
					Details: ValidationFailureDetails{Failures: collectValidationFailures(verr)},
				}, nil
			}
		}

		forwardHeader(inner)
		if err := caller.EmitOutput(data); err != nil {
			// Caller-side terminal (cancel / abandoned stream): tear down
			// the binding and stop. Nothing to report — the caller handle
			// is already terminal.
			inner.Cancel()
			return nil, nil
		}
	}
}

func (e *OperationInvoker) resolveContext(ctx context.Context, details *ContextRequiredDetails) (map[string]any, error) {
	if e.ContextResolver == nil {
		return nil, nil
	}
	resolved, err := e.ContextResolver(ctx, details)
	if err != nil || len(resolved) == 0 {
		return nil, err
	}
	return resolved, nil
}

// makeInputValidator builds the OBI-T-07 write-validation hook for an
// operation, compiling lazily on the first write (concurrency-safe).
func makeInputValidator(op *Operation, iface *Interface, operationName string) func(any) *InvocationError {
	if op.Input == nil {
		return nil
	}
	var (
		once         sync.Once
		compiled     *jsonschema.Schema
		compileError *InvocationError
	)
	return func(input any) *InvocationError {
		once.Do(func() {
			defs := buildSchemaDefs(iface.Schemas)
			c, err := compileExampleSchema(op.Input, defs)
			if err != nil {
				compileError = &InvocationError{
					Code:    ErrCodeValidationFailed,
					Message: fmt.Sprintf("openbindings: input schema compilation failed for %q: %v", operationName, err),
				}
				return
			}
			compiled = c
		})
		if compileError != nil {
			return compileError
		}
		if verr := compiled.Validate(input); verr != nil {
			lines := splitSchemaError(verr)
			return &InvocationError{
				Code:    ErrCodeValidationFailed,
				Message: fmt.Sprintf("openbindings: input validation failed for %q: %s", operationName, strings.Join(lines, "; ")),
				Details: ValidationFailureDetails{Failures: collectValidationFailures(verr)},
			}
		}
		return nil
	}
}

// asIE unwraps err into *InvocationError (including wrapped ones — a foreign
// Invocation impl may wrap the SDK error while preserving its load-bearing
// Code).
func asIE(err error, target **InvocationError) bool {
	return errors.As(err, target)
}

// wireError converts wiring errors raised mid-run into terminal invocation errors.
func wireError(err error) *InvocationError {
	var ie *InvocationError
	if errors.As(err, &ie) {
		return ie
	}
	if errors.Is(err, ErrNoInvoker) || strings.Contains(err.Error(), ErrNoInvoker.Error()) {
		return &InvocationError{Code: ErrCodeBindingNotFound, Message: err.Error()}
	}
	return AsInvocationError(err)
}

// DefaultBindingSelector picks the best binding for an operation. Non-deprecated
// bindings are preferred over deprecated ones. Within the same deprecation status,
// higher preference values win (binding preference overrides source preference; an
// absent preference is the neutral baseline of 0). Ties are broken alphabetically
// by key.
//
// Returns ErrBindingNotFound if no binding matches the operation.
func DefaultBindingSelector(iface *Interface, opKey string) (string, *BindingEntry, error) {
	return selectBinding(iface, opKey, nil)
}

// selectBinding is the internal implementation of binding selection. When
// availableFormats is non-nil, bindings whose source format is not in the set
// are skipped.
func selectBinding(iface *Interface, opKey string, availableFormats map[string]bool) (string, *BindingEntry, error) {
	if iface == nil || len(iface.Bindings) == 0 {
		return "", nil, fmt.Errorf("%w: %s", ErrBindingNotFound, opKey)
	}

	var bestKey string
	var best *BindingEntry
	bestPref := math.Inf(-1)
	bestDeprecated := true

	for k, b := range iface.Bindings {
		if b.Operation != opKey {
			continue
		}

		// Skip bindings whose source format the invoker can't handle.
		if availableFormats != nil {
			src, ok := iface.Sources[b.Source]
			if ok && !formatMatches(src.Format, availableFormats) {
				continue
			}
		}

		// Binding preference overrides source preference; absent on both is
		// the neutral baseline of 0.
		bPref := 0.0
		if b.Preference != nil {
			bPref = *b.Preference
		} else if src, ok := iface.Sources[b.Source]; ok && src.Preference != nil {
			bPref = *src.Preference
		}

		betterDeprecation := bestDeprecated && !b.Deprecated
		sameTier := b.Deprecated == bestDeprecated
		if best == nil || betterDeprecation || (sameTier && bPref > bestPref) || (sameTier && bPref == bestPref && k < bestKey) {
			bestKey = k
			entry := b
			best = &entry
			bestPref = bPref
			bestDeprecated = b.Deprecated
		}
	}

	if best == nil {
		return "", nil, fmt.Errorf("%w: %s", ErrBindingNotFound, opKey)
	}
	return bestKey, best, nil
}

// formatMatches checks whether a source format token satisfies one of the
// invoker-advertised format tokens/ranges.
func formatMatches(sourceFormat string, available map[string]bool) bool {
	if available[sourceFormat] {
		return true
	}
	for f := range available {
		vr, err := formattoken.ParseRange(f)
		if err != nil {
			continue
		}
		if formattoken.Matches(vr, sourceFormat) {
			return true
		}
	}
	return false
}

// formatName extracts the lowercase name portion from a format token ("openapi@3.1" → "openapi").
func formatName(token string) string {
	token = strings.TrimSpace(token)
	at := strings.LastIndexByte(token, '@')
	if at <= 0 {
		return strings.ToLower(token)
	}
	return strings.ToLower(token[:at])
}

// applyTransformRef resolves a TransformOrRef and evaluates it.
func applyTransformRef(eval TransformEvaluator, transforms map[string]Transform, tor *TransformOrRef, data any) (any, error) {
	if tor == nil {
		return data, nil
	}

	expr, ok := tor.Resolve(transforms)
	if !ok {
		if tor.IsRef() {
			return nil, fmt.Errorf("%w: %q", ErrTransformRefNotFound, tor.Ref)
		}
		return nil, fmt.Errorf("openbindings: invalid transform: neither ref nor inline")
	}

	if expr == "" {
		return nil, ErrEmptyTransformExpression
	}

	return eval.Evaluate(expr, data)
}
