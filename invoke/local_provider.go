package invoke

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

// LocalPreflight optionally reports current prerequisites for one native
// implementation. It must remain side-effect free.
type LocalPreflight func(context.Context, *BindingInvocationArgs) (*ContextRequiredDetails, error)

type localStreamHandler func(context.Context, BindingHandle[any, any], *BindingInvocationArgs)

// LocalBindingImplementation is a type-erased native implementation created
// by LocalUnary or LocalStream. Its address remains the OBI binding key.
type LocalBindingImplementation struct {
	handler   localStreamHandler
	preflight LocalPreflight
}

// LocalImplementationOption configures one native implementation.
type LocalImplementationOption func(*LocalBindingImplementation)

// WithLocalPreflight attaches a side-effect-free live prerequisite check.
func WithLocalPreflight(preflight LocalPreflight) LocalImplementationOption {
	return func(implementation *LocalBindingImplementation) {
		implementation.preflight = preflight
	}
}

// LocalUnary adapts the common exactly-one-input/one-output case. Generic
// JSON-domain maps and slices remain the same Go references end to end.
func LocalUnary[I, O any](
	handler func(context.Context, I) (O, error),
	opts ...LocalImplementationOption,
) LocalBindingImplementation {
	implementation := LocalBindingImplementation{
		handler: func(ctx context.Context, handle BindingHandle[any, any], _ *BindingInvocationArgs) {
			input, err := handle.ReadInput(ctx)
			if err == io.EOF {
				handle.FireError(NewInvocationError(ErrCodeMissingInput))
				return
			}
			if err != nil {
				handle.FireError(AsInvocationError(err))
				return
			}
			_ = handle.CloseInput()
			if _, err := handle.ReadInput(ctx); err == nil {
				handle.FireError(NewInvocationError(ErrCodeTooManyInputs))
				return
			} else if err != io.EOF {
				handle.FireError(AsInvocationError(err))
				return
			}
			typed, ok := localTypedValue[I](input)
			if !ok {
				handle.FireError(NewInvocationError(ErrCodeTypeMismatch))
				return
			}
			output, err := handler(ctx, typed)
			if err != nil {
				handle.FireError(NewInvocationError(ErrCodeExecutionFailed))
				return
			}
			generic, ok := localGenericValue(output)
			if !ok {
				handle.FireError(NewInvocationError(ErrCodeTypeMismatch))
				return
			}
			if err := handle.EmitOutput(generic); err == nil {
				handle.CloseOutput()
			}
		},
	}
	for _, opt := range opts {
		opt(&implementation)
	}
	return implementation
}

// LocalStream exposes the ordinary cardinality-neutral binding handle with
// typed native values. The handler owns normal CloseOutput termination.
func LocalStream[I, O any](
	handler func(context.Context, BindingHandle[I, O], *BindingInvocationArgs),
	opts ...LocalImplementationOption,
) LocalBindingImplementation {
	implementation := LocalBindingImplementation{
		handler: func(ctx context.Context, handle BindingHandle[any, any], args *BindingInvocationArgs) {
			handler(ctx, &typedLocalBindingHandle[I, O]{inner: handle}, args)
		},
	}
	for _, opt := range opts {
		opt(&implementation)
	}
	return implementation
}

type typedLocalBindingHandle[I, O any] struct{ inner BindingHandle[any, any] }

func (h *typedLocalBindingHandle[I, O]) ReadInput(ctx context.Context) (I, error) {
	var zero I
	value, err := h.inner.ReadInput(ctx)
	if err != nil {
		return zero, err
	}
	typed, ok := localTypedValue[I](value)
	if !ok {
		err := NewInvocationError(ErrCodeTypeMismatch)
		h.inner.FireError(err)
		return zero, err
	}
	return typed, nil
}

func (h *typedLocalBindingHandle[I, O]) CloseInput() error { return h.inner.CloseInput() }
func (h *typedLocalBindingHandle[I, O]) CloseOutput()      { h.inner.CloseOutput() }
func (h *typedLocalBindingHandle[I, O]) FireError(err *InvocationError) {
	h.inner.FireError(err)
}
func (h *typedLocalBindingHandle[I, O]) Done() <-chan struct{} { return h.inner.Done() }
func (h *typedLocalBindingHandle[I, O]) EmitOutput(output O) error {
	value, ok := localGenericValue(output)
	if !ok {
		err := NewInvocationError(ErrCodeTypeMismatch)
		h.inner.FireError(err)
		return err
	}
	return h.inner.EmitOutput(value)
}

func localTypedValue[T any](value any) (T, bool) {
	if typed, ok := value.(T); ok {
		return typed, true
	}
	var typed T
	data, err := json.Marshal(value)
	if err != nil || json.Unmarshal(data, &typed) != nil {
		return typed, false
	}
	return typed, true
}

func localGenericValue[T any](value T) (any, bool) {
	raw := any(value)
	if isNativeJSONValue(raw, nil, 0) {
		return raw, true
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var generic any
	if json.Unmarshal(data, &generic) != nil {
		return nil, false
	}
	return generic, true
}

type localBindingInvoker struct {
	spec            string
	implementations map[string]LocalBindingImplementation
}

func (i *localBindingInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: i.spec}}
}

func (i *localBindingInvoker) CompileBinding(args *BindingInvocationArgs) (CompiledBindingInvoker, error) {
	if args == nil || args.Site == nil {
		return nil, fmt.Errorf("openbindings: local binding compiler requires an exact binding key")
	}
	implementation, ok := i.implementations[args.Site.BindingKey]
	if !ok {
		return nil, &RealizationNotFoundError{BindingKey: args.Site.BindingKey}
	}
	return &compiledLocalBinding{implementation: implementation}, nil
}

func (i *localBindingInvoker) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	compiled, err := i.CompileBinding(args)
	if err != nil {
		return NewErroredInvocation[any, any](NewInvocationError(ErrCodeBindingNotFound))
	}
	return compiled.InvokeBinding(ctx, args)
}

func (i *localBindingInvoker) PrepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	compiled, err := i.CompileBinding(args)
	if err != nil {
		return nil, err
	}
	return compiled.PrepareBinding(ctx, args)
}

type compiledLocalBinding struct{ implementation LocalBindingImplementation }

func (b *compiledLocalBinding) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	invocation := NewInvocationImpl[any, any](ctx)
	copyArgs := *args
	go func() {
		defer func() {
			if recover() != nil {
				invocation.FireError(NewInvocationError(ErrCodeExecutionFailed))
			}
		}()
		b.implementation.handler(ctx, invocation, &copyArgs)
	}()
	return invocation
}

func (b *compiledLocalBinding) PrepareBinding(ctx context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	if b.implementation.preflight == nil {
		return nil, nil
	}
	copyArgs := *args
	return b.implementation.preflight(ctx, &copyArgs)
}

type localProviderRuntime struct {
	invoker     *OperationInvoker
	implemented map[string]bool
	mu          sync.Mutex
	closed      bool
}

func (r *localProviderRuntime) BindingSpecs() []openbindings.BindingSpecInfo {
	return r.invoker.BindingSpecs()
}
func (r *localProviderRuntime) SupportsBinding(binding openbindings.PreparedBindingDescriptor) bool {
	return r.implemented[binding.Key]
}
func (r *localProviderRuntime) CompileRealization(ctx context.Context, prepared *openbindings.PreparedInterface, binding openbindings.PreparedBindingDescriptor) (CompiledRealizationBehavior, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("openbindings: local provider runtime is closed")
	}
	return r.invoker.CompileRealization(ctx, prepared, binding)
}
func (r *localProviderRuntime) CompileRealizationSnapshot(ctx context.Context, prepared *openbindings.PreparedInterface, snapshot *openbindings.Interface, binding openbindings.PreparedBindingDescriptor) (CompiledRealizationBehavior, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("openbindings: local provider runtime is closed")
	}
	return r.invoker.CompileRealizationSnapshot(ctx, prepared, snapshot, binding)
}
func (r *localProviderRuntime) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}

// PrepareLocalProviderOptions maps exact OBI binding keys to native code.
type PrepareLocalProviderOptions struct {
	Key               string
	Label             string
	Interface         *openbindings.PreparedInterface
	Implementations   map[string]LocalBindingImplementation
	SelectRealization RealizationSelector
}

// PrepareLocalProvider builds an in-process provider on the same prepared
// interface, policy, validation, transform, and invocation substrate as every
// protocol-backed provider.
func PrepareLocalProvider(options PrepareLocalProviderOptions) (*PreparedProvider, error) {
	if options.Interface == nil {
		return nil, fmt.Errorf("openbindings: prepared local provider interface is required")
	}
	bindingKeys := make([]string, 0, len(options.Implementations))
	for key := range options.Implementations {
		bindingKeys = append(bindingKeys, key)
	}
	sort.Strings(bindingKeys)
	bySpec := make(map[string]map[string]LocalBindingImplementation)
	implemented := make(map[string]bool, len(bindingKeys))
	for _, bindingKey := range bindingKeys {
		implementation := options.Implementations[bindingKey]
		if implementation.handler == nil {
			return nil, fmt.Errorf("openbindings: local implementation %q is invalid", bindingKey)
		}
		binding, ok := options.Interface.Binding(bindingKey)
		if !ok {
			return nil, fmt.Errorf("openbindings: local implementation references unknown binding %q", bindingKey)
		}
		entries := bySpec[binding.BindingSpec]
		if entries == nil {
			entries = make(map[string]LocalBindingImplementation)
			bySpec[binding.BindingSpec] = entries
		}
		entries[bindingKey] = implementation
		implemented[bindingKey] = true
	}
	specs := make([]string, 0, len(bySpec))
	for spec := range bySpec {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	invokers := make([]BindingInvoker, 0, len(specs))
	for _, spec := range specs {
		invokers = append(invokers, &localBindingInvoker{spec: spec, implementations: bySpec[spec]})
	}
	runtime := &localProviderRuntime{
		invoker:     NewOperationInvoker(invokers...),
		implemented: implemented,
	}
	return PrepareProvider(PreparedProviderOptions{
		Key:               options.Key,
		Label:             options.Label,
		Interface:         options.Interface,
		Runtime:           runtime,
		SelectRealization: options.SelectRealization,
	})
}

var _ BindingInvoker = (*localBindingInvoker)(nil)
var _ BindingPreparer = (*localBindingInvoker)(nil)
var _ BindingCompiler = (*localBindingInvoker)(nil)
var _ ProviderRuntime = (*localProviderRuntime)(nil)
var _ ProviderRuntimeSnapshotCompiler = (*localProviderRuntime)(nil)
var _ ProviderRuntimeBindingSupport = (*localProviderRuntime)(nil)
var _ ProviderRuntimeCloser = (*localProviderRuntime)(nil)
