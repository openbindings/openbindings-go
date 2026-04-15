package openbindings

import (
	"context"
	"fmt"
	"math"
	"strings"
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
// Executors that need extra context check for this interface via type assertion.
type TransformEvaluatorWithBindings interface {
	TransformEvaluator
	EvaluateWithBindings(expression string, data any, bindings map[string]any) (any, error)
}

// OperationExecutor composites multiple BindingExecutors into a higher-level
// mux that can route by format and resolve OBI operations to bindings.
//
// When ContextStore is set, Store and Callbacks are propagated to the executor
// so it can look up stored context and invoke platform interactions during
// execution. Executors derive context keys internally using NormalizeContextKey.
type OperationExecutor struct {
	BindingSelector    func(*Interface, string) (string, *BindingEntry, error)
	TransformEvaluator TransformEvaluator
	ContextStore       ContextStore
	PlatformCallbacks  *PlatformCallbacks

	executor BindingExecutor
}

// NewOperationExecutor creates an OperationExecutor from one or more BindingExecutors.
// Registration order matters: first registration wins for a given format name.
func NewOperationExecutor(executors ...BindingExecutor) *OperationExecutor {
	return &OperationExecutor{
		executor: CombineExecutors(executors...),
	}
}

// AddBindingExecutor registers an additional BindingExecutor after construction.
// This is useful when an executor depends on the OperationExecutor itself,
// which creates a circular dependency that cannot be resolved at construction time.
// Must be called during initialization, before any concurrent use of the executor.
func (e *OperationExecutor) AddBindingExecutor(exec BindingExecutor) {
	e.executor.(*combinedExecutor).add(exec)
}

// WithRuntime returns a shallow copy of the executor with the given
// ContextStore and PlatformCallbacks. The copy shares the underlying
// combined executor with the original but has independent runtime fields.
// Nil arguments inherit the original's values.
func (e *OperationExecutor) WithRuntime(store ContextStore, callbacks *PlatformCallbacks) *OperationExecutor {
	cp := &OperationExecutor{
		BindingSelector:    e.BindingSelector,
		TransformEvaluator: e.TransformEvaluator,
		ContextStore:       store,
		PlatformCallbacks:  callbacks,
		executor:           e.executor,
	}
	if cp.ContextStore == nil {
		cp.ContextStore = e.ContextStore
	}
	if cp.PlatformCallbacks == nil {
		cp.PlatformCallbacks = e.PlatformCallbacks
	}
	return cp
}

// Formats returns all formats registered with this executor.
func (e *OperationExecutor) Formats() []FormatInfo {
	return e.executor.Formats()
}

func (e *OperationExecutor) availableFormats() map[string]bool {
	m := make(map[string]bool)
	for _, f := range e.executor.Formats() {
		m[f.Token] = true
	}
	return m
}

// ExecuteBinding routes a binding execution to the appropriate BindingExecutor
// by source format and returns a stream of events. Store and Callbacks are
// propagated from the executor when not already set on the input. Executors
// are responsible for looking up stored context internally using
// NormalizeContextKey.
func (e *OperationExecutor) ExecuteBinding(ctx context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
	return e.executor.ExecuteBinding(ctx, e.withRuntime(in))
}

// withRuntime returns a shallow copy of in with Store and Callbacks filled
// from the executor when the input doesn't already have them. The caller's
// original input is never mutated.
func (e *OperationExecutor) withRuntime(in *BindingExecutionInput) *BindingExecutionInput {
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

// ExecuteOperation resolves an OBI operation to a binding and returns a stream
// of events. Every operation is a stream — unary calls produce a single event.
//
// The executor's ExecuteBinding returns a channel of StreamEvent. Output
// transforms are applied per event.
//
// Input transforms are applied once before execution.
func (e *OperationExecutor) ExecuteOperation(ctx context.Context, in *OperationExecutionInput) (<-chan StreamEvent, error) {
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
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Error: &ExecuteError{
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

	execInput := in.Input
	if binding.InputTransform != nil {
		if e.TransformEvaluator == nil {
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Error: &ExecuteError{
				Code:    ErrCodeTransformError,
				Message: fmt.Sprintf("%v: binding %q has inputTransform", ErrNoTransformEvaluator, bindingKey),
			}}
			close(ch)
			return ch, nil
		}
		transformed, err := applyTransformRef(e.TransformEvaluator, in.Interface.Transforms, binding.InputTransform, execInput)
		if err != nil {
			ch := make(chan StreamEvent, 1)
			ch <- StreamEvent{Error: &ExecuteError{
				Code:    ErrCodeTransformError,
				Message: fmt.Sprintf("openbindings: input transform failed for %q: %v", bindingKey, err),
			}}
			close(ch)
			return ch, nil
		}
		execInput = transformed
	}

	bindingIn := &BindingExecutionInput{
		Source: BindingExecutionSource{
			Format:   source.Format,
			Location: source.Location,
		},
		Ref:         binding.Ref,
		Input:       execInput,
		InputSchema: op.Input,
		Context:     in.Context,
		Options:     in.Options,
		Interface:   in.Interface,
	}
	if source.Content != nil && source.Location == "" {
		bindingIn.Source.Content = source.Content
	}
	if binding.Security != "" && in.Interface.Security != nil {
		if methods, ok := in.Interface.Security[binding.Security]; ok {
			bindingIn.Security = methods
		}
	}

	src, err := e.ExecuteBinding(ctx, bindingIn)
	if err != nil {
		return nil, err
	}
	return e.transformStream(ctx, src, binding, in.Interface.Transforms, bindingKey), nil
}

// transformStream wraps a source stream, applying outputTransform to each
// event's Data. If no outputTransform is configured, returns src directly.
// The context is used to cancel drain goroutines when the parent is cancelled.
func (e *OperationExecutor) transformStream(ctx context.Context, src <-chan StreamEvent, binding *BindingEntry, transforms map[string]Transform, bindingKey string) <-chan StreamEvent {
	if binding.OutputTransform == nil {
		return src
	}
	if e.TransformEvaluator == nil {
		out := make(chan StreamEvent, 1)
		out <- StreamEvent{Error: &ExecuteError{
			Code:    ErrCodeTransformError,
			Message: fmt.Sprintf("%v: binding %q has outputTransform", ErrNoTransformEvaluator, bindingKey),
		}}
		close(out)
		// Drain src so the producer goroutine is not leaked.
		// Respect context cancellation to avoid blocking forever on slow sources.
		go func() {
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
		}()
		return out
	}

	out := make(chan StreamEvent)
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
				if ev.Error != nil || ev.Data == nil {
					out <- ev
					continue
				}
				transformed, err := applyTransformRef(e.TransformEvaluator, transforms, binding.OutputTransform, ev.Data)
				if err != nil {
					out <- StreamEvent{Error: &ExecuteError{
						Code:    ErrCodeTransformError,
						Message: fmt.Sprintf("openbindings: output transform failed for %q: %v", bindingKey, err),
					}}
					continue
				}
				out <- StreamEvent{Data: transformed, Status: ev.Status, DurationMs: ev.DurationMs}
			}
		}
	}()
	return out
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

		// Skip bindings whose source format the executor can't handle.
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

// formatMatches checks whether a source format token matches any in the set.
// Handles versioned tokens: "mcp@2025-11-25" matches if the set contains
// "mcp" or "mcp@2025-11-25".
func formatMatches(sourceFormat string, available map[string]bool) bool {
	if available[sourceFormat] {
		return true
	}
	bare := sourceFormat
	if idx := strings.Index(sourceFormat, "@"); idx >= 0 {
		bare = sourceFormat[:idx]
	}
	for f := range available {
		fBare := f
		if idx := strings.Index(f, "@"); idx >= 0 {
			fBare = f[:idx]
		}
		if fBare == bare {
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

	t := tor.Resolve(transforms)
	if t == nil {
		if tor.IsRef() {
			return nil, fmt.Errorf("%w: %q", ErrTransformRefNotFound, tor.Ref)
		}
		return nil, fmt.Errorf("openbindings: invalid transform: neither ref nor inline")
	}

	if t.Expression == "" {
		return nil, ErrEmptyTransformExpression
	}

	return eval.Evaluate(t.Expression, data)
}
