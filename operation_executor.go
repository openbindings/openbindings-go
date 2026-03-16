package openbindings

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
)

// BindingSelector determines which binding to use for an operation.
// Returns the binding key and the binding entry, or an error.
type BindingSelector func(iface *Interface, opKey string) (string, *BindingEntry, error)

// TransformEvaluator evaluates transform expressions (e.g., JSONata) against input data.
// Implementations are provided by callers to keep the core SDK dependency-free.
type TransformEvaluator interface {
	Evaluate(expression string, data any) (any, error)
}

// OperationExecutor composites multiple BindingExecutors into a higher-level
// mux that can route by format and resolve OBI operations to bindings.
//
// OperationExecutor itself satisfies BindingExecutor, enabling nested composition.
type OperationExecutor struct {
	BindingSelector    BindingSelector
	TransformEvaluator TransformEvaluator

	mu        sync.RWMutex
	executors map[string]BindingExecutor
	creators  map[string]InterfaceCreator
	streamers map[string]BindingStreamHandler
	formats   []string
}

// NewOperationExecutor creates an OperationExecutor from one or more BindingExecutors.
// Each provider is type-asserted for InterfaceCreator and BindingStreamHandler support.
// Registration order matters: first registration wins for a given format name.
func NewOperationExecutor(providers ...BindingExecutor) *OperationExecutor {
	e := &OperationExecutor{
		executors: make(map[string]BindingExecutor),
		creators:  make(map[string]InterfaceCreator),
		streamers: make(map[string]BindingStreamHandler),
	}
	for _, p := range providers {
		e.register(p)
	}
	return e
}

// register adds a provider for each of its declared formats.
// First registration wins for a given format name.
func (e *OperationExecutor) register(provider BindingExecutor) {
	e.mu.Lock()
	defer e.mu.Unlock()

	creator, isCreator := provider.(InterfaceCreator)
	streamer, isStreamer := provider.(BindingStreamHandler)

	for _, token := range provider.Formats() {
		name := formatName(token)
		if name == "" {
			continue
		}
		if _, exists := e.executors[name]; !exists {
			e.executors[name] = provider
			e.formats = append(e.formats, token)
		}
		if isCreator {
			if _, exists := e.creators[name]; !exists {
				e.creators[name] = creator
			}
		}
		if isStreamer {
			if _, exists := e.streamers[name]; !exists {
				e.streamers[name] = streamer
			}
		}
	}
}

// Formats returns all format tokens registered with this executor.
func (e *OperationExecutor) Formats() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.formats
}

// ExecuteBinding routes a binding execution to the appropriate BindingExecutor by source format.
// Satisfies the BindingExecutor interface.
func (e *OperationExecutor) ExecuteBinding(ctx context.Context, in *BindingExecutionInput) (*ExecuteOutput, error) {
	e.mu.RLock()
	exec, ok := e.executors[formatName(in.Source.Format)]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoProvider, in.Source.Format)
	}
	return exec.ExecuteBinding(ctx, in)
}

// ExecuteOperation resolves an OBI operation to a binding, selects the appropriate
// BindingExecutor, applies transforms, and executes.
func (e *OperationExecutor) ExecuteOperation(ctx context.Context, in *OperationExecutionInput) (*ExecuteOutput, error) {
	if in.Interface == nil {
		return nil, ErrNilInterface
	}
	if _, ok := in.Interface.Operations[in.Operation]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrOperationNotFound, in.Operation)
	}

	selector := e.BindingSelector
	if selector == nil {
		selector = DefaultBindingSelector
	}

	bindingKey, binding, err := selector(in.Interface, in.Operation)
	if err != nil {
		return nil, err
	}

	source, ok := in.Interface.Sources[binding.Source]
	if !ok {
		return nil, fmt.Errorf("%w: binding %q references %q", ErrUnknownSource, bindingKey, binding.Source)
	}

	execInput := in.Input
	if binding.InputTransform != nil {
		if e.TransformEvaluator == nil {
			return nil, fmt.Errorf("%w: binding %q has inputTransform", ErrNoTransformEvaluator, bindingKey)
		}
		transformed, err := applyTransformRef(e.TransformEvaluator, in.Interface.Transforms, binding.InputTransform, execInput)
		if err != nil {
			return nil, fmt.Errorf("openbindings: input transform failed for %q: %w", bindingKey, err)
		}
		execInput = transformed
	}

	bindingIn := &BindingExecutionInput{
		Source: ExecuteSource{
			Format:   source.Format,
			Location: source.Location,
		},
		Ref:     binding.Ref,
		Input:   execInput,
		Context: in.Context,
	}
	if source.Content != nil && source.Location == "" {
		bindingIn.Source.Content = source.Content
	}

	result, err := e.ExecuteBinding(ctx, bindingIn)
	if err != nil {
		return nil, err
	}

	if binding.OutputTransform != nil && result.Error == nil {
		if e.TransformEvaluator == nil {
			return result, fmt.Errorf("%w: binding %q has outputTransform", ErrNoTransformEvaluator, bindingKey)
		}
		transformed, err := applyTransformRef(e.TransformEvaluator, in.Interface.Transforms, binding.OutputTransform, result.Output)
		if err != nil {
			return result, fmt.Errorf("openbindings: output transform failed for %q: %w", bindingKey, err)
		}
		result.Output = transformed
	}

	return result, nil
}

// CreateInterface routes an interface creation request to the appropriate InterfaceCreator
// by source format. Routing is based on the first source's format; all sources should
// share the same format.
func (e *OperationExecutor) CreateInterface(ctx context.Context, in *CreateInput) (*Interface, error) {
	if len(in.Sources) == 0 {
		return nil, ErrNoSources
	}
	e.mu.RLock()
	cr, ok := e.creators[formatName(in.Sources[0].Format)]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoProvider, in.Sources[0].Format)
	}
	return cr.CreateInterface(ctx, in)
}

// SubscribeBinding routes a streaming subscription to the appropriate BindingStreamHandler
// by source format.
func (e *OperationExecutor) SubscribeBinding(ctx context.Context, in *BindingExecutionInput) (<-chan StreamEvent, error) {
	e.mu.RLock()
	s, ok := e.streamers[formatName(in.Source.Format)]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoProvider, in.Source.Format)
	}
	return s.SubscribeBinding(ctx, in)
}

// DefaultBindingSelector picks the best binding for an operation. Non-deprecated
// bindings are preferred over deprecated ones. Within the same deprecation status,
// lower priority values win. Ties are broken alphabetically by key.
//
// Returns ErrBindingNotFound if no binding matches the operation.
func DefaultBindingSelector(iface *Interface, opKey string) (string, *BindingEntry, error) {
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
		bPri := math.MaxFloat64
		if b.Priority != nil {
			bPri = *b.Priority
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

// formatName extracts the lowercase name portion from a format token ("openapi@3.1" → "openapi").
func formatName(token string) string {
	at := strings.LastIndexByte(token, '@')
	if at <= 0 {
		return strings.ToLower(strings.TrimSpace(token))
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
