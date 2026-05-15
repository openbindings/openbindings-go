package openbindings

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/openbindings/openbindings-go/formattoken"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// BindingSelector determines which binding to use for an operation.
// Returns the binding key and the binding entry, or an error.
type BindingSelector func(iface *Interface, opKey string) (string, *BindingEntry, error)

// TransformEvaluator evaluates transform expressions (e.g., JSONata) against input data.
// Implementations are provided by callers to keep the core SDK dependency-free.
type TransformEvaluator interface {
	Evaluate(expression string, data any) (any, error)
}

// TransformEvaluatorWithBindings extends TransformEvaluator with support for
// additional named bindings (e.g., $input in operation graph transforms).
// Invokers that need extra context check for this interface via type assertion.
type TransformEvaluatorWithBindings interface {
	TransformEvaluator
	EvaluateWithBindings(expression string, data any, bindings map[string]any) (any, error)
}

// OperationInvoker composites multiple BindingInvokers into a higher-level
// mux that can route by format and resolve OBI operations to bindings.
//
// When ContextStore is set, Store and Callbacks are propagated to the invoker
// so it can look up stored context and invoke platform interactions during
// invocation. Invokers derive context keys internally using NormalizeContextKey.
type OperationInvoker struct {
	BindingSelector    func(*Interface, string) (string, *BindingEntry, error)
	TransformEvaluator TransformEvaluator
	ContextStore       ContextStore
	PlatformCallbacks  *PlatformCallbacks

	invoker BindingInvoker
}

// NewOperationInvoker creates an OperationInvoker from one or more BindingInvokers.
// Registration order matters: first registration wins for a given format name.
func NewOperationInvoker(invokers ...BindingInvoker) *OperationInvoker {
	return &OperationInvoker{
		invoker: CombineInvokers(invokers...),
	}
}

// AddBindingInvoker registers an additional BindingInvoker after construction.
// This is useful when an invoker depends on the OperationInvoker itself,
// which creates a circular dependency that cannot be resolved at construction time.
// Must be called during initialization, before any concurrent use of the invoker.
func (e *OperationInvoker) AddBindingInvoker(invoker BindingInvoker) {
	e.invoker.(*combinedInvoker).add(invoker)
}

// WithRuntime returns a shallow copy of the invoker with the given
// ContextStore and PlatformCallbacks. The copy shares the underlying
// combined invoker with the original but has independent runtime fields.
// Nil arguments inherit the original's values.
func (e *OperationInvoker) WithRuntime(store ContextStore, callbacks *PlatformCallbacks) *OperationInvoker {
	cp := &OperationInvoker{
		BindingSelector:    e.BindingSelector,
		TransformEvaluator: e.TransformEvaluator,
		ContextStore:       store,
		PlatformCallbacks:  callbacks,
		invoker:            e.invoker,
	}
	if cp.ContextStore == nil {
		cp.ContextStore = e.ContextStore
	}
	if cp.PlatformCallbacks == nil {
		cp.PlatformCallbacks = e.PlatformCallbacks
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

// InvokeBinding routes a binding invocation to the appropriate BindingInvoker
// by source format and returns a stream of events. Store and Callbacks are
// propagated from the invoker when not already set on the input. Invokers
// are responsible for looking up stored context internally using
// NormalizeContextKey.
func (e *OperationInvoker) InvokeBinding(ctx context.Context, in *BindingInvocationInput) (<-chan InvocationOutput, error) {
	return e.invoker.InvokeBinding(ctx, e.withRuntime(in))
}

// withRuntime returns a shallow copy of in with Store and Callbacks filled
// from the invoker when the input doesn't already have them. The caller's
// original input is never mutated.
func (e *OperationInvoker) withRuntime(in *BindingInvocationInput) *BindingInvocationInput {
	if (in.Store != nil || e.ContextStore == nil) && (in.Callbacks != nil || e.PlatformCallbacks == nil) {
		return in
	}
	cp := *in
	if cp.Store == nil && e.ContextStore != nil {
		cp.Store = e.ContextStore
	}
	if cp.Callbacks == nil && e.PlatformCallbacks != nil {
		cp.Callbacks = e.PlatformCallbacks
	}
	return &cp
}

// Invoke resolves an OBI operation to a binding and returns a stream
// of events. Every operation is a stream — unary calls produce a single event.
//
// The invoker's InvokeBinding returns a channel of InvocationOutput. Output
// transforms are applied per event.
//
// Input transforms are applied once before invocation.
func (e *OperationInvoker) Invoke(ctx context.Context, in *OperationInvocationInput) (<-chan InvocationOutput, error) {
	if in.Interface == nil {
		return nil, ErrNilInterface
	}
	op, ok := in.Interface.Operations[in.Operation]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrOperationNotFound, in.Operation)
	}

	var bindingKey string
	var binding *BindingEntry

	if in.BindingKey != "" {
		b, ok := in.Interface.Bindings[in.BindingKey]
		if !ok {
			ch := make(chan InvocationOutput, 1)
			ch <- InvocationOutput{Error: &InvocationError{
				Code:    ErrCodeBindingNotFound,
				Message: fmt.Sprintf("binding %q is not defined on this interface", in.BindingKey),
			}}
			close(ch)
			return ch, nil
		}
		bindingKey = in.BindingKey
		binding = &b
	} else {
		selector := e.BindingSelector
		if selector == nil {
			selector = func(iface *Interface, opKey string) (string, *BindingEntry, error) {
				return selectBinding(iface, opKey, e.availableFormats())
			}
		}

		var err error
		bindingKey, binding, err = selector(in.Interface, in.Operation)
		if err != nil {
			return nil, err
		}
	}

	source, ok := in.Interface.Sources[binding.Source]
	if !ok {
		return nil, fmt.Errorf("%w: binding %q references %q", ErrUnknownSource, bindingKey, binding.Source)
	}

	// OBI-T-07: Validate input against the operation's input schema before transform.
	if op.Input != nil {
		defs := buildSchemaDefs(in.Interface.Schemas)
		compiled, err := compileExampleSchema(op.Input, defs)
		if err != nil {
			ch := make(chan InvocationOutput, 1)
			ch <- InvocationOutput{Error: &InvocationError{
				Code:    ErrCodeValidationFailed,
				Message: fmt.Sprintf("openbindings: input schema compilation failed for %q: %v", in.Operation, err),
			}}
			close(ch)
			return ch, nil
		}
		if verr := compiled.Validate(in.Input); verr != nil {
			ch := make(chan InvocationOutput, 1)
			lines := splitSchemaError(verr)
			ch <- InvocationOutput{Error: &InvocationError{
				Code:    ErrCodeValidationFailed,
				Message: fmt.Sprintf("openbindings: input validation failed for %q: %s", in.Operation, strings.Join(lines, "; ")),
				Details: ValidationFailureDetails{Failures: collectValidationFailures(verr)},
			}}
			close(ch)
			return ch, nil
		}
	}

	invokeInput := in.Input
	if binding.InputTransform != nil {
		if e.TransformEvaluator == nil {
			ch := make(chan InvocationOutput, 1)
			ch <- InvocationOutput{Error: &InvocationError{
				Code:    ErrCodeTransformError,
				Message: fmt.Sprintf("%v: binding %q has inputTransform", ErrNoTransformEvaluator, bindingKey),
			}}
			close(ch)
			return ch, nil
		}
		transformed, err := applyTransformRef(e.TransformEvaluator, in.Interface.Transforms, binding.InputTransform, invokeInput)
		if err != nil {
			ch := make(chan InvocationOutput, 1)
			ch <- InvocationOutput{Error: &InvocationError{
				Code:    ErrCodeTransformError,
				Message: fmt.Sprintf("openbindings: input transform failed for %q: %v", bindingKey, err),
			}}
			close(ch)
			return ch, nil
		}
		invokeInput = transformed
	}

	bindingIn := &BindingInvocationInput{
		Source: BindingInvocationSource{
			Format:   source.Format,
			Location: source.Location,
		},
		Ref:         binding.Ref,
		Input:       invokeInput,
		InputSchema: op.Input,
		Context:     in.Context,
		Interface:   in.Interface,
	}
	if source.Content != nil {
		bindingIn.Source.Content = source.Content
	}
	if binding.Security != "" && in.Interface.Security != nil {
		if methods, ok := in.Interface.Security[binding.Security]; ok {
			bindingIn.Security = methods
		}
	}

	src, err := e.InvokeBinding(ctx, bindingIn)
	if err != nil {
		return nil, err
	}
	return e.transformStream(ctx, src, binding, in.Interface.Transforms, bindingKey, op.Output, in.Interface.Schemas), nil
}

// transformStream wraps a source stream, applying outputTransform to each
// event's Data and validating against outputSchema (OBI-T-08). If neither
// outputTransform nor outputSchema is configured, returns src directly.
// The context is used to cancel drain goroutines when the parent is cancelled.
func (e *OperationInvoker) transformStream(ctx context.Context, src <-chan InvocationOutput, binding *BindingEntry, transforms map[string]Transform, bindingKey string, outputSchema JSONSchema, schemas map[string]JSONSchema) <-chan InvocationOutput {
	if binding.OutputTransform == nil && outputSchema == nil {
		return src
	}
	if binding.OutputTransform != nil && e.TransformEvaluator == nil {
		out := make(chan InvocationOutput, 1)
		out <- InvocationOutput{Error: &InvocationError{
			Code:    ErrCodeTransformError,
			Message: fmt.Sprintf("%v: binding %q has outputTransform", ErrNoTransformEvaluator, bindingKey),
		}}
		close(out)
		// Drain src so the producer goroutine is not leaked.
		// Respect context cancellation to avoid blocking forever on slow sources.
		go drainStream(ctx, src)
		return out
	}

	// Compile output schema once outside the loop.
	var compiledOutput *jsonschema.Schema
	if outputSchema != nil {
		defs := buildSchemaDefs(schemas)
		compiled, err := compileExampleSchema(outputSchema, defs)
		if err != nil {
			out := make(chan InvocationOutput, 1)
			out <- InvocationOutput{Error: &InvocationError{
				Code:    ErrCodeValidationFailed,
				Message: fmt.Sprintf("openbindings: output schema compilation failed for %q: %v", bindingKey, err),
			}}
			close(out)
			go drainStream(ctx, src)
			return out
		}
		compiledOutput = compiled
	}

	out := make(chan InvocationOutput)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				if ev.Error != nil {
					out <- ev
					continue
				}
				data := ev.Output
				if binding.OutputTransform != nil {
					transformed, err := applyTransformRef(e.TransformEvaluator, transforms, binding.OutputTransform, data)
					if err != nil {
						out <- InvocationOutput{Error: &InvocationError{
							Code:    ErrCodeTransformError,
							Message: fmt.Sprintf("openbindings: output transform failed for %q: %v", bindingKey, err),
						}}
						continue
					}
					data = transformed
				}
				// OBI-T-08: Validate output after transform. On failure, yield
				// the data alongside the error so callers can inspect or render
				// the underlying response while still being informed of the
				// schema mismatch. The spec describes a T-08 failure as an
				// "invocation error for that operation"; it does not prescribe
				// how the SDK surfaces that error, and it does not require
				// hiding the data.
				if compiledOutput != nil {
					if verr := compiledOutput.Validate(data); verr != nil {
						lines := splitSchemaError(verr)
						out <- InvocationOutput{
							Output: data,
							Error: &InvocationError{
								Code:    ErrCodeValidationFailed,
								Message: fmt.Sprintf("openbindings: output validation failed for %q: %s", bindingKey, strings.Join(lines, "; ")),
								Details: ValidationFailureDetails{Failures: collectValidationFailures(verr)},
							},
							Status:     ev.Status,
							DurationMs: ev.DurationMs,
						}
						continue
					}
				}
				out <- InvocationOutput{Output: data, Status: ev.Status, DurationMs: ev.DurationMs}
			}
		}
	}()
	return out
}

func drainStream(ctx context.Context, src <-chan InvocationOutput) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-src:
			if !ok {
				return
			}
		}
	}
}

// DefaultBindingSelector picks the best binding for an operation. Non-deprecated
// bindings are preferred over deprecated ones. Within the same deprecation status,
// lower priority values win (binding priority overrides source priority). Ties
// are broken alphabetically by key.
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
	bestPri := math.MaxFloat64
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

		// Binding priority overrides source priority.
		bPri := math.MaxFloat64
		if b.Priority != nil {
			bPri = *b.Priority
		} else if src, ok := iface.Sources[b.Source]; ok && src.Priority != nil {
			bPri = *src.Priority
		}

		betterDeprecation := bestDeprecated && !b.Deprecated
		sameTier := b.Deprecated == bestDeprecated
		if best == nil || betterDeprecation || (sameTier && bPri < bestPri) || (sameTier && bPri == bestPri && k < bestKey) {
			bestKey = k
			entry := b
			best = &entry
			bestPri = bPri
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
