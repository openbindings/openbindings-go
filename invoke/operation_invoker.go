package invoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/bindingsupport"
	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
)

// BindingSelector determines which binding to use for an operation.
// Returns the binding key and the binding entry, or an error.
type BindingSelector func(iface *openbindings.Interface, opKey string) (string, *openbindings.BindingEntry, error)

// TransformEvaluator evaluates transform expressions (e.g., JSONata) against input data.
// Implementations are provided by callers to keep the core SDK dependency-free.
// ctx is per evaluation, never expression data. Implementations should check it
// before work and at cooperative checkpoints; it does not preempt host code.
// Evaluators must distinguish their engine's JSON values from functions and
// other internal values before conversion. A runtime value implementing
// json.Marshaler is not necessarily a JSONata JSON value. Return nil, nil for
// JSON null and ErrTransformUndefined for no result. Numerical conversion is
// the selected evaluator's documented responsibility, not an SDK-wide policy.
type TransformEvaluator interface {
	Evaluate(ctx context.Context, expression string, data any) (any, error)
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
	EvaluateWithBindings(ctx context.Context, expression string, data any, bindings map[string]any) (any, error)
}

// ContextResolver resolves a CONTEXT_REQUIRED challenge into context data.
// Returning (nil, nil) declines, at which point the challenge surfaces to
// the caller unchanged. Composition-time wiring: whether it consults a
// context store, reads an env var, prompts a keychain, or returns a
// hardcoded value is the resolver's business — invisible to the invoker and
// to bindings.
//
// A CONTEXT_REQUIRED challenge is a scope, not a hint. A resolver MUST return
// only the context needed for the selected alternative and MUST NOT return
// unrelated credentials. ScopeContext is the reference reduction.
type ContextResolver func(ctx context.Context, details *ContextRequiredDetails) (map[string]any, error)

// OperationInvoker is the operation-layer invoker: it resolves an OBI
// operation to a binding (OBI-T-12 name resolution; explicit caller choice or
// the operation-invoker contract's sole-candidate rule) and returns a cardinality-agnostic
// Invocation handle.
//
// Between the caller and the binding it enforces the operation contract
// (validation carries the core's claim semantics, OBI-T-16: complete
// statically reachable schema graph, `format` as annotation, per value;
// a mismatch is ERR_OPERATION_VALIDATION_FAILED, an unresolvable governing graph is
// ERR_SCHEMA_UNRESOLVED, never partial validation):
//   - every caller input validates against the operation's input schema
//     BEFORE the input transform; a failure is terminal and rejects the
//     offending Write with the same error.
//   - inputTransform / outputTransform evaluate per message (JSONata 2.1).
//   - each (transformed) output validates against the output schema before
//     it is emitted; a failure is terminal and the value is not emitted.
//     Callers that need to inspect unvalidated payloads call InvokeBinding
//     directly.
//   - CONTEXT_REQUIRED negotiation: challenges a binding can state before
//     the first attempt (its PreflightBinding answer) are resolved via
//     ContextResolver and the one attempt starts with the merged context. A
//     preflight that returns an error could not answer and predicts
//     nothing: the attempt proceeds as if preflight had reported no
//     requirement, and the outcome is the attempt's. Only the lane's own
//     facts stop it earlier (no invoker for the format, cancellation). A
//     live CONTEXT_REQUIRED raised by the binding during the attempt
//     terminates the invocation with its ContextRequiredDetails intact,
//     regardless of what was forwarded or produced; the invoker does not
//     consult the resolver for it and starts no second attempt. The caller
//     resolves the challenge and invokes again.
//
// Wiring errors (unknown operation, binding, or source; no invoker for the
// format) surface as an already-errored handle with a local wiring code
// (ErrCodeOperationNotFound, ErrCodeBindingNotFound, ErrCodeUnknownSource).
type OperationInvoker struct {
	ValueLimits        ValueLimits
	BindingSelector    func(*openbindings.Interface, string) (string, *openbindings.BindingEntry, error)
	TransformEvaluator TransformEvaluator
	// ContextResolver resolves CONTEXT_REQUIRED challenges raised by
	// bindings. When nil, or when it declines, the challenge surfaces to the
	// caller as an ordinary terminal *InvocationError.
	ContextResolver ContextResolver
	// OutputDecoder, ResultClassifier, and FieldRouter are invoker-level
	// consumer hooks (the middle precedence tier), consulted by format
	// invokers through the seam. Set before concurrent use, like the other
	// fields. Protocol-specific handling lives INSIDE the hook body
	// (switch on the exact site.BindingSpec); decline to fall through.
	OutputDecoder    OutputDecoder
	ResultClassifier ResultClassifier
	FieldRouter      FieldRouter
	// MaxDeliveryUnitBytes bounds one delivery unit — the bytes materialized
	// to produce one emitted output value — for every invocation this
	// invoker dispatches, stamped into BindingInvocationArgs like the hook
	// fields. Zero or negative selects DefaultMaxDeliveryUnitBytes; an
	// effectively-unlimited bound is set explicitly huge (no sentinel). Set
	// before concurrent use, like the other policy fields. The
	// per-invocation lever is BindingInvocationArgs.MaxDeliveryUnitBytes on
	// a direct binding-layer call.
	MaxDeliveryUnitBytes int64

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
	cp := *e // struct copy: hook fields and future fields ride automatically
	if resolver != nil {
		cp.ContextResolver = resolver
	}
	return &cp
}

// BindingSpecs returns all binding specifications registered with this
// invoker, by exact identifier. It is an aggregation convenience over the
// registered binding invokers; the openbindings.operation-invoker interface
// itself carries no listBindingSpecs operation (its reach is dynamic, e.g.
// via delegates).
func (e *OperationInvoker) BindingSpecs() []bindingsupport.BindingSpecInfo {
	return e.invoker.BindingSpecs()
}

// CheckBindingSpecs authoritatively checks exact binding-specification
// identifiers against the registered binding invokers.
func (e *OperationInvoker) CheckBindingSpecs(bindingSpecs []string) []bindingsupport.BindingSpecVerdict {
	return e.invoker.CheckBindingSpecs(bindingSpecs)
}

func (e *OperationInvoker) availableBindingSpecs(iface *openbindings.Interface, opKey string) map[string]bool {
	set := make(map[string]struct{})
	for _, binding := range iface.Bindings {
		if binding.Operation != opKey {
			continue
		}
		if source, ok := iface.Sources[binding.Source]; ok {
			set[source.BindingSpec] = struct{}{}
		}
	}
	bindingSpecs := make([]string, 0, len(set))
	for bindingSpec := range set {
		bindingSpecs = append(bindingSpecs, bindingSpec)
	}
	sort.Strings(bindingSpecs)

	available := make(map[string]bool, len(bindingSpecs))
	for _, verdict := range e.invoker.CheckBindingSpecs(bindingSpecs) {
		if verdict.Supported {
			available[verdict.BindingSpec] = true
		}
	}
	return available
}

// InvokeBinding routes a binding invocation directly to the matching
// BindingInvoker by source format, without operation-layer validation,
// transforms, or context negotiation.
func (e *OperationInvoker) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	e.fillBindingArgs(args)
	return e.invoker.InvokeBinding(ctx, args)
}

// snapshotHooks resolves the effective hook carrier for one Invoke: both
// tiers captured once at entry (immunity to later field mutation);
// precedence applies at consultation time by decline-chaining.
func (e *OperationInvoker) snapshotHooks(perInv hookSlots) *InvokeHooks {
	return newInvokeHooks(perInv, hookSlots{
		decode:   e.OutputDecoder,
		classify: e.ResultClassifier,
		route:    e.FieldRouter,
	})
}

// SnapshotHooks composes per-invocation hooks over this invoker's
// invoker-level hooks into the seam carrier a direct binding-layer call
// passes as BindingInvocationArgs.Hooks — the same both-tier snapshot
// Invoke takes at entry. Nil axes simply decline down the chain (per-
// invocation → invoker-level → builtin). This is how an embedder that
// drives the binding layer itself (fillBindingArgs's "direct callers who
// want different hooks pass their own") attaches per-invocation
// elections whose declines still reach its invoker-level table.
func (e *OperationInvoker) SnapshotHooks(decode OutputDecoder, classify ResultClassifier, route FieldRouter) *InvokeHooks {
	return e.snapshotHooks(hookSlots{decode: decode, classify: classify, route: route})
}

// fillBindingArgs completes a binding-layer call's args with the seam
// carrier and a stamped site when the caller supplied none — the
// in-process binding-layer path (delegate lane), which is what makes an
// embedder's invoker-level hook table reach delegate-dispatched
// invocations. Direct callers who want different hooks pass their own.
func (e *OperationInvoker) fillBindingArgs(args *BindingInvocationArgs) {
	if args == nil {
		return
	}
	args.ValueLimits = mergeValueLimits(e.ValueLimits, args.ValueLimits)
	if args.Hooks == nil {
		args.Hooks = e.snapshotHooks(hookSlots{})
	}
	if args.MaxDeliveryUnitBytes <= 0 {
		args.MaxDeliveryUnitBytes = e.MaxDeliveryUnitBytes
	}
	if args.Site == nil {
		site := &InvokeSite{
			BindingSpec: args.Source.BindingSpec,
			Selector:    cloneSelector(args.Selector),
		}
		if args.Binding != nil {
			site.Operation = args.Binding.Operation
			site.InvokedAs = args.Binding.Operation
		}
		if inv := e.invoker.findInvoker(args.Source.BindingSpec); inv != nil {
			stampSite(site, inv)
		} else {
			site.seamStamped = true
		}
		args.Site = site
	}
}

// PreflightBinding takes an already resolved binding in args and asks the selected
// binding which context requirements it can already identify. Supply
// context with WithContext; it is used for this call alone. A non-nil
// result has the shape a live CONTEXT_REQUIRED carries and may omit
// requirements; nil means none reported, not ready. An error means the
// binding could not answer and predicts nothing. Invoke preflights on its
// own before every attempt and consults ContextResolver then; this explicit
// call never does. Discard a result once the operation, binding or context
// changes.
func (e *OperationInvoker) PreflightBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	return e.invoker.preflightBinding(ctx, args)
}

// resolveBinding is the shared operation-layer resolution behind Invoke and
// PreflightOperation: it resolves operation against obi's flat key+alias namespace
// (OBI-T-12), resolves a binding (an explicit caller choice or the contract's
// sole-candidate rule),
// and looks up its source. A wiring failure returns a typed *InvocationError so
// each caller can surface it its own way (an errored handle for Invoke, a
// returned error for PreflightOperation).
func (e *OperationInvoker) resolveBinding(obi *openbindings.Interface, operation, pinnedBindingKey string, callerContext map[string]any) (
	op *openbindings.Operation, bindingKey string, binding *openbindings.BindingEntry, source *openbindings.Source, ierr *InvocationError,
) {
	if obi == nil {
		return nil, "", nil, nil, &InvocationError{Code: ErrCodeOperationValidationFailed}
	}

	// OBI-T-12: resolve against the flat key+aliases namespace. Bindings are
	// selected by the resolved canonical key, not the name the caller used.
	opKey, opVal, ok := openbindings.ResolveOperation(obi, operation)
	if !ok {
		return nil, "", nil, nil, &InvocationError{
			Code: ErrCodeOperationNotFound,
		}
	}
	op = &opVal
	availableSpecs := e.availableBindingSpecs(obi, opKey)

	// A pinned bindingKey narrows selection to one binding OF the resolved
	// operation; it never replaces the operation. Addressing a binding *without*
	// an operation (the contract's binding-alone form, which derives the
	// operation) is a wire/dynamic concern — a caller with only a key does
	// obi.Bindings[key].Operation and passes it here — so the native API stays
	// operation-keyed.
	if pinnedBindingKey != "" {
		b, ok := obi.Bindings[pinnedBindingKey]
		if !ok {
			return nil, "", nil, nil, &InvocationError{
				Code: ErrCodeBindingNotFound,
			}
		}
		// The explicit binding must belong to the resolved operation. Without
		// this guard a mismatched op/binding pair would validate input/output
		// against the resolved operation's contract while invoking a different
		// operation's binding.
		if b.Operation != opKey {
			return nil, "", nil, nil, &InvocationError{
				Code: ErrCodeBindingNotFound,
			}
		}
		bindingKey = pinnedBindingKey
		binding = &b
	} else if k, b, ok := selectionOverride(obi, opKey, contextSelectionOverride(callerContext), availableSpecs); ok {
		// The operation-invoker contract's consumer override
		// (context.configuration.selection): an ordered list of binding
		// keys, the first invocable entry winning. It displaces whatever
		// policy is in place. When no listed key is invocable, the
		// policy-neutral sole-candidate/ambiguity rule below applies.
		bindingKey = k
		binding = b
	} else {
		selector := e.BindingSelector
		if selector == nil {
			selector = func(iface *openbindings.Interface, opKey string) (string, *openbindings.BindingEntry, error) {
				return selectBinding(iface, opKey, availableSpecs)
			}
		}
		var err error
		bindingKey, binding, err = selector(obi, opKey)
		if err != nil {
			code := ErrCodeBindingNotFound
			if errors.Is(err, ErrBindingSelectionRequired) {
				code = ErrCodeBindingSelectionRequired
			}
			return nil, "", nil, nil, &InvocationError{Code: code}
		}
	}

	src, ok := obi.Sources[binding.Source]
	if !ok {
		return nil, "", nil, nil, &InvocationError{
			Code: ErrCodeUnknownSource,
		}
	}
	return op, bindingKey, binding, &src, nil
}

// PreflightOperation resolves operation as Invoke would and asks the selected
// binding which context requirements it can already identify. Supply
// context with WithContext; it is used for this call alone. A non-nil
// result has the shape a live CONTEXT_REQUIRED carries and may omit
// requirements; nil means none reported, not ready. An error means the
// binding could not answer and predicts nothing. Invoke preflights on its
// own before every attempt and consults ContextResolver then; this explicit
// call never does. Discard a result once the operation, binding or context
// changes.
func (e *OperationInvoker) PreflightOperation(ctx context.Context, obi *openbindings.Interface, operation string, opts ...InvokeOption) (*ContextRequiredDetails, error) {
	var cfg invokeConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	op, _, binding, source, ierr := e.resolveBinding(obi, operation, cfg.bindingKey, cfg.context)
	if ierr != nil {
		return nil, ierr
	}
	return e.PreflightBinding(ctx, &BindingInvocationArgs{
		Source: InvocationSource{
			BindingSpec: source.BindingSpec,
			Location:    sourceLocation(source.Location),
			Content:     source.Content,
		},
		Selector:    cloneSelector(binding.Selector),
		Binding:     binding,
		Context:     cfg.context,
		Interface:   obi,
		InputSchema: op.Input,
	})
}

// run drives the one binding-layer invocation behind a caller-facing handle:
// preflight context resolution, an input pump forwarding (transformed) caller
// inputs, and an output loop forwarding (transformed, T-16-validated) binding
// outputs.
func (e *OperationInvoker) run(
	ctx context.Context,
	caller *InvocationImpl[any, any],
	iface *openbindings.Interface,
	op *openbindings.Operation,
	binding *openbindings.BindingEntry,
	bindingKey string,
	source *openbindings.Source,
	initialContext map[string]any,
	invokedAs string,
	hooks *InvokeHooks,
	diagnostics *DiagnosticCollector,
) {
	e.runCompiled(ctx, caller, iface, op, binding, bindingKey, source, initialContext, invokedAs, hooks, diagnostics, nil, false, nil)
}

// runCompiled drives one already-selected binding. When outputPrepared is
// true, compiledOutput is the complete prepared-interface validator (or nil
// because the operation declares no output schema); no document work occurs.
func (e *OperationInvoker) runCompiled(
	ctx context.Context,
	caller *InvocationImpl[any, any],
	iface *openbindings.Interface,
	op *openbindings.Operation,
	binding *openbindings.BindingEntry,
	bindingKey string,
	source *openbindings.Source,
	initialContext map[string]any,
	invokedAs string,
	hooks *InvokeHooks,
	diagnostics *DiagnosticCollector,
	compiledOutput *openbindings.CompiledSchema,
	outputPrepared bool,
	compiledBinding CompiledBindingInvoker,
) {
	if (binding.InputTransform != nil || binding.OutputTransform != nil) && e.TransformEvaluator == nil {
		caller.FireError(&InvocationError{
			Code: ErrCodeTransformError,
		})
		return
	}

	// Compile the output schema once per invocation at its canonical address
	// inside the OBI document, preserving the complete statically reachable
	// schema graph. A graph that cannot be established is ERR_SCHEMA_UNRESOLVED —
	// the claim could not be evaluated — never partial validation
	// (OBI-T-16).
	if !outputPrepared && op.Output != nil {
		compiled, err := openbindings.CompileOperationSchema(iface, binding.Operation, "output")
		if err != nil {
			caller.FireError(&InvocationError{
				Code: ErrCodeSchemaUnresolved,
			})
			return
		}
		compiledOutput = compiled
	}

	contextData := initialContext
	bindingArgs := func() *BindingInvocationArgs {
		a := &BindingInvocationArgs{
			Source: InvocationSource{
				BindingSpec: source.BindingSpec,
				Location:    sourceLocation(source.Location),
				Content:     source.Content,
			},
			Selector:    cloneSelector(binding.Selector),
			Binding:     binding,
			Context:     contextData,
			Interface:   iface,
			InputSchema: op.Input,
		}
		a.ValueLimits = valueLimitsOf(caller.limits)
		a.Hooks = hooks
		a.MaxDeliveryUnitBytes = e.MaxDeliveryUnitBytes
		site := &InvokeSite{
			Operation:   binding.Operation,
			InvokedAs:   invokedAs,
			BindingKey:  bindingKey,
			BindingSpec: source.BindingSpec,
			Selector:    cloneSelector(binding.Selector),
		}
		if inv := e.invoker.findInvoker(source.BindingSpec); inv != nil {
			stampSite(site, inv)
		} else {
			site.seamStamped = true
		}
		a.Site = site
		return a
	}
	mergeResolved := func(resolved map[string]any) bool {
		merged := make(map[string]any, len(contextData)+len(resolved))
		for k, v := range contextData {
			merged[k] = v
		}
		for k, v := range resolved {
			// The binding-invoker contract starts the attempt with the
			// *augmented* context, not a replaced one. Top-level credential fields are
			// leaf values, so an overwrite is correct — but `configuration`
			// is a map keyed by configuration point, and a resolved
			// config.value (R1a) names one point; overwriting the whole map
			// would clobber sibling points the caller already supplied. Merge
			// it point-wise so a resolved `server` value does not drop an
			// existing `decode` override.
			if k == "configuration" {
				rc, rok := v.(map[string]any)
				ec, eok := merged[k].(map[string]any)
				if rok && eok {
					points := make(map[string]any, len(ec)+len(rc))
					for pk, pv := range ec {
						points[pk] = pv
					}
					for pk, pv := range rc {
						if currentPoint, ok := points[pk].(map[string]any); ok {
							if resolvedPoint, ok := pv.(map[string]any); ok {
								point := make(map[string]any, len(currentPoint)+len(resolvedPoint))
								for key, value := range currentPoint {
									point[key] = value
								}
								for key, value := range resolvedPoint {
									point[key] = value
								}
								points[pk] = point
								continue
							}
						}
						points[pk] = pv
					}
					merged[k] = points
					continue
				}
			}
			if k == "credentials" || k == "apiKeys" {
				rc, rok := v.(map[string]any)
				ec, eok := merged[k].(map[string]any)
				if rok && eok {
					named := make(map[string]any, len(ec)+len(rc))
					for name, value := range ec {
						named[name] = value
					}
					for name, value := range rc {
						named[name] = value
					}
					merged[k] = named
					continue
				}
			}
			merged[k] = v
		}
		changed := !reflect.DeepEqual(contextData, merged)
		contextData = merged
		return changed
	}

	// Preflight (the binding-invoker contract's preflightBinding): collapse
	// knowable-upfront context challenges into the clean no-input-consumed
	// case before anything is forwarded.
	// Preflight and resolution share the attempt's cancellation lifetime.
	innerCtx, innerCancel := DoneContext(ctx, caller.Done())
	defer innerCancel()
	var details *ContextRequiredDetails
	var err error
	if compiledBinding != nil {
		details, err = compiledBinding.PreflightBinding(innerCtx, bindingArgs())
	} else {
		details, err = e.invoker.preflightBinding(innerCtx, bindingArgs())
	}
	if err != nil {
		// An unsuccessful preflight means the binding could not answer and
		// carries no prediction (PREFLIGHT.md), so it does not stop the
		// attempt: proceed as if preflight reported nothing, without
		// consulting the resolver. The lane's own facts still terminate
		// here: no invoker handles the format (ERR_BINDING_NOT_FOUND), and
		// cancellation.
		wired := wireError(err)
		if wired.Code == ErrCodeBindingNotFound || innerCtx.Err() != nil {
			caller.FireError(wired)
			return
		}
		details = nil
	}
	if details != nil {
		if !ValidContextRequiredDetails(details) {
			caller.FireError(NewInvocationError(ErrCodeRuntime))
			return
		}
		resolved, resolveErr := e.resolveContext(innerCtx, details)
		if resolveErr != nil {
			caller.FireError(NewInvocationError(ErrCodeRuntime))
			return
		}
		if resolved == nil {
			challenge := NewContextRequiredError(details)
			caller.FireError(challenge)
			return
		}
		if !mergeResolved(resolved) {
			caller.FireError(NewContextRequiredError(details))
			return
		}
	}

	// One attempt. innerCtx bounds the binding: it cancels when the caller
	// handle terminates (cancel propagation) and when the attempt is retired.
	if innerCtx.Err() != nil {
		return
	}
	var inner Invocation[any, any]
	if compiledBinding != nil {
		inner = compiledBinding.InvokeBinding(innerCtx, bindingArgs())
	} else {
		inner = e.invoker.InvokeBinding(innerCtx, bindingArgs())
	}
	pumpDone := make(chan struct{})
	// Retire the attempt on every exit, including a foreign evaluator or
	// provider panic that the caller of runCompiled recovers: unpark and join
	// the pump (the caller's input buffer has exactly one reader), then make
	// sure the binding is terminal.
	retire := func() {
		innerCancel()
		<-pumpDone
		inner.Cancel()
	}
	defer retire()
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
					Code: ErrCodeRuntime,
				})
			}
		}()
		e.pumpInputs(innerCtx, caller, inner, binding, bindingKey, iface)
	}()

	surface := e.runOutputs(innerCtx, caller, inner, binding, bindingKey, iface, compiledOutput, diagnostics)

	// Join the pump before the caller's terminal: a terminal the pump raised
	// (a failed input transform) must not lose to the inner cancellation it
	// caused. A live CONTEXT_REQUIRED surfaces here unchanged, details intact.
	retire()
	if surface != nil {
		caller.FireError(surface)
	} else {
		caller.CloseOutput()
	}
}

// pumpInputs forwards caller inputs to the binding: it reads the caller's
// buffer, applies the input transform, and writes to the inner invocation. It
// exits when the caller closes input (forwarding the close), when the attempt
// is retired (innerCtx), or when the inner invocation terminates.
func (e *OperationInvoker) pumpInputs(
	innerCtx context.Context, caller *InvocationImpl[any, any], inner Invocation[any, any],
	binding *openbindings.BindingEntry, bindingKey string, iface *openbindings.Interface,
) {
	writeInner := func(p *valueio.Packet) bool {
		var err error
		if ep := valueio.From(inner); ep != nil {
			err = ep.SendInput(innerCtx, p)
		} else {
			// Foreign implementations own their internal storage and lifetime.
			var raw any
			raw, err = valueio.Construct[any](innerCtx, p, caller.limits)
			if err == nil {
				err = inner.Write(innerCtx, raw)
			}
		}
		if err != nil {
			var ie *InvocationError
			if asIE(err, &ie) && ie.Code == ErrCodeInputClosed {
				_ = caller.CloseInput()
			}
			return true
		}
		return false
	}
	for {
		p, err := caller.readInputPacket(innerCtx)
		if err == io.EOF {
			_ = inner.Close()
			return
		}
		if err != nil {
			return
		}
		if binding.InputTransform != nil {
			transformed, err := transformPacket(innerCtx, e.TransformEvaluator, iface.Transforms, binding.InputTransform, p, caller.limits)
			if err != nil {
				if innerCtx.Err() != nil {
					return
				}
				inner.Cancel()
				ie := &InvocationError{Code: ErrCodeTransformError}
				if resourceFailure(err) {
					ie = valueError(err, "input transform")
				}
				caller.FireError(ie)
				return
			}
			p = transformed
		}
		if writeInner(p) {
			return
		}
	}
}

func (e *OperationInvoker) runOutputs(
	innerCtx context.Context, caller *InvocationImpl[any, any], inner Invocation[any, any],
	binding *openbindings.BindingEntry, bindingKey string, iface *openbindings.Interface,
	compiledOutput *openbindings.CompiledSchema, diagnostics *DiagnosticCollector,
) *InvocationError {
	ep := valueio.From(inner)
	var out OutputStream[any]
	if ep != nil {
		ep.ClaimOutput()
		defer ep.Stop()
	} else {
		out = inner.Outputs()
		defer out.Stop()
	}
	for {
		var p *valueio.Packet
		var err error
		if ep != nil {
			p, err = ep.ReadOutput(innerCtx)
		} else {
			var raw any
			raw, err = out.Read(innerCtx)
			if err == nil {
				p, err = valueio.Capture(innerCtx, caller.limits, raw)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// A live CONTEXT_REQUIRED is an ordinary terminal here: it
			// propagates unchanged so the caller can resolve and invoke again.
			if resourceFailure(err) {
				return valueError(err, "binding output")
			}
			return AsInvocationError(err)
		}
		if binding.OutputTransform != nil {
			transformed, err := transformPacket(innerCtx, e.TransformEvaluator, iface.Transforms, binding.OutputTransform, p, caller.limits)
			if err != nil {
				if innerCtx.Err() != nil {
					return AsInvocationError(innerCtx.Err())
				}
				inner.Cancel()
				if resourceFailure(err) {
					return valueError(err, "output transform")
				}
				return &InvocationError{Code: ErrCodeTransformError}
			}
			p = transformed
		}
		if compiledOutput != nil {
			logical, err := p.View(innerCtx, caller.limits, false)
			if err != nil {
				inner.Cancel()
				return valueError(err, "output validation")
			}
			if verr := compiledOutput.Validate(logical); verr != nil {
				inner.Cancel()
				diagnostics.recordValidation(ValidationPhaseOutput, binding.Operation, bindingKey, verr)
				return validationInvocationError(verr)
			}
		}
		if err := caller.emitPacket(p); err != nil {
			inner.Cancel()
			return nil
		}
	}
}

func transformPacket(ctx context.Context, eval TransformEvaluator, transforms map[string]openbindings.Transform, tor openbindings.TransformOrRef, p *valueio.Packet, limits value.Limits) (*valueio.Packet, error) {
	logical, err := p.View(ctx, limits, true)
	if err != nil {
		return nil, err
	}
	result, err := evaluateTransform(ctx, eval, transforms, tor, logical)
	if err != nil {
		return nil, err
	}
	out, err := valueio.Capture(ctx, limits, result)
	if err != nil {
		return nil, err
	}
	if !validInvocationValue(reflect.ValueOf(result), map[visit]bool{}) {
		return nil, fmt.Errorf("openbindings: transform result is not a JSON value")
	}
	return out, nil
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

// makeInputValidator builds the write-validation hook for an operation,
// compiling lazily on the first write (concurrency-safe). Validation
// carries the core's claim semantics (OBI-T-16): the complete statically
// reachable schema graph, `format` as annotation, applied per value — a
// mismatch is ERR_OPERATION_VALIDATION_FAILED; a graph that cannot be established is
// ERR_SCHEMA_UNRESOLVED, never partial validation.
func makeInputValidator(op *openbindings.Operation, iface *openbindings.Interface, operationName, bindingKey string, diagnostics *DiagnosticCollector) func(any) *InvocationError {
	if op.Input == nil {
		return nil
	}
	var (
		once         sync.Once
		compiled     *openbindings.CompiledSchema
		compileError *InvocationError
	)
	return func(input any) *InvocationError {
		once.Do(func() {
			c, err := openbindings.CompileOperationSchema(iface, operationName, "input")
			if err != nil {
				compileError = &InvocationError{
					Code: ErrCodeSchemaUnresolved,
				}
				return
			}
			compiled = c
		})
		if compileError != nil {
			return compileError
		}
		if verr := compiled.Validate(input); verr != nil {
			diagnostics.recordValidation(ValidationPhaseInput, operationName, bindingKey, verr)
			return validationInvocationError(verr)
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
		return &InvocationError{Code: ErrCodeBindingNotFound}
	}
	return AsInvocationError(err)
}

// selectionOverride resolves the operation-invoker contract's
// context.configuration.selection override: the first listed binding key
// that is invocable — defined on the interface, targeting the resolved
// operation, and (when availableSpecs is non-nil) governed by a
// specification the invoker can act on — wins. ok is false when no listed
// key is invocable, in which case the policy-neutral sole-candidate or
// ambiguity rule applies.
func selectionOverride(iface *openbindings.Interface, opKey string, keys []string, availableSpecs map[string]bool) (string, *openbindings.BindingEntry, bool) {
	if iface == nil {
		return "", nil, false
	}
	for _, k := range keys {
		b, ok := iface.Bindings[k]
		if !ok || b.Operation != opKey {
			continue
		}
		if availableSpecs != nil {
			if src, ok := iface.Sources[b.Source]; ok && !availableSpecs[src.BindingSpec] {
				continue
			}
		}
		entry := b
		return k, &entry, true
	}
	return "", nil, false
}

// DefaultBindingSelector resolves the only binding for an operation. It does
// not invent a choice from preference, deprecation, key order, source order,
// or map iteration: several candidates return ErrBindingSelectionRequired.
//
// Returns ErrBindingNotFound if no binding matches the operation.
func DefaultBindingSelector(iface *openbindings.Interface, opKey string) (string, *openbindings.BindingEntry, error) {
	return selectBinding(iface, opKey, nil)
}

// selectBinding is the internal implementation of binding selection. When
// availableSpecs is non-nil, bindings whose governing binding specification
// is not in the set (by exact identifier) are skipped.
func selectBinding(iface *openbindings.Interface, opKey string, availableSpecs map[string]bool) (string, *openbindings.BindingEntry, error) {
	if iface == nil || len(iface.Bindings) == 0 {
		return "", nil, fmt.Errorf("%w: %s", ErrBindingNotFound, opKey)
	}

	var candidateKey string
	var candidate *openbindings.BindingEntry
	candidateCount := 0
	// Bindings that matched the operation but were skipped because no
	// registered invoker handles their governing binding specification. The
	// distinction is load-bearing for the error: "the document has no
	// binding" sends the user to audit the OBI; "the binding needs a spec
	// you didn't register" sends them to their own NewOperationInvoker
	// call.
	specSkipped := map[string]string{} // binding key -> required binding spec

	for k, b := range iface.Bindings {
		if b.Operation != opKey {
			continue
		}

		// Skip bindings whose source format the invoker can't handle.
		if availableSpecs != nil {
			src, ok := iface.Sources[b.Source]
			if ok && !availableSpecs[src.BindingSpec] {
				specSkipped[k] = src.BindingSpec
				continue
			}
		}

		candidateCount++
		if candidate == nil {
			candidateKey = k
			entry := b
			candidate = &entry
		}
	}

	if candidate == nil {
		if len(specSkipped) > 0 {
			keys := make([]string, 0, len(specSkipped))
			for k := range specSkipped {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var needs []string
			for _, k := range keys {
				needs = append(needs, fmt.Sprintf("%q requires binding spec %s", k, specSkipped[k]))
			}
			registered := make([]string, 0, len(availableSpecs))
			for f := range availableSpecs {
				registered = append(registered, f)
			}
			sort.Strings(registered)
			return "", nil, fmt.Errorf("%w: %s — binding %s; registered binding specs: [%s] (did you register the spec's invoker with NewOperationInvoker?)",
				ErrBindingNotFound, opKey, strings.Join(needs, ", "), strings.Join(registered, ", "))
		}
		return "", nil, fmt.Errorf("%w: %s", ErrBindingNotFound, opKey)
	}
	if candidateCount > 1 {
		return "", nil, fmt.Errorf("%w: operation %q has %d invocable bindings; choose one with an explicit binding key or configuration.selection",
			ErrBindingSelectionRequired, opKey, candidateCount)
	}
	return candidateKey, candidate, nil
}

// applyTransformRef resolves a TransformOrRef and evaluates it.
func evaluateTransform(ctx context.Context, eval TransformEvaluator, transforms map[string]openbindings.Transform, tor openbindings.TransformOrRef, data any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tor == nil {
		return data, nil
	}

	expr, ok := tor.Resolve(transforms)
	if !ok {
		ref := ""
		if reference, isRef := tor.(*openbindings.TransformReference); isRef && reference != nil {
			ref = reference.Ref
		}
		return nil, fmt.Errorf("%w: %q", ErrTransformRefNotFound, ref)
	}

	if expr == "" {
		return nil, ErrEmptyTransformExpression
	}

	result, err := eval.Evaluate(ctx, expr, data)
	if cancelled := ctx.Err(); cancelled != nil {
		return nil, cancelled
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

func applyTransformRef(ctx context.Context, eval TransformEvaluator, transforms map[string]openbindings.Transform, tor openbindings.TransformOrRef, data any) (any, error) {
	if tor == nil {
		return data, nil
	}
	limits := defaultValueLimits()
	p, err := valueio.Capture(ctx, limits, data)
	if err != nil {
		return nil, err
	}
	out, err := transformPacket(ctx, eval, transforms, tor, p, limits)
	if err != nil {
		return nil, err
	}
	return valueio.Construct[any](ctx, out, limits)
}
