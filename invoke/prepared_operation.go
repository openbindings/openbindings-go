package invoke

import (
	"context"
	"fmt"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CompiledRealizationBehavior is executable behavior for one exact
// SDK-selected binding descriptor. It carries no authoritative route metadata.
type CompiledRealizationBehavior interface {
	Invoke(context.Context, ...InvokeOption) Invocation[any, any]
	Preflight(context.Context, ...InvokeOption) (*ContextRequiredDetails, error)
}

// ProviderRuntime supplies behavior for SDK-selected bindings. BindingSpecs is
// capability discovery; CompileRealization receives the exact immutable OBI
// identity and may not replace it with runtime-authored metadata.
type ProviderRuntime interface {
	BindingSpecs() []openbindings.BindingSpecInfo
	CompileRealization(context.Context, *openbindings.PreparedInterface, openbindings.PreparedBindingDescriptor) (CompiledRealizationBehavior, error)
}

// ProviderRuntimeSnapshotCompiler is the shared-snapshot closure seam used by
// PreparedProvider. The snapshot is SDK-owned and shared by every realization
// in that provider revision; runtimes must treat it as read-only.
type ProviderRuntimeSnapshotCompiler interface {
	CompileRealizationSnapshot(context.Context, *openbindings.PreparedInterface, *openbindings.Interface, openbindings.PreparedBindingDescriptor) (CompiledRealizationBehavior, error)
}

// ProviderRuntimeBindingSupport optionally refines exact binding eligibility,
// which is useful for partial in-process registries sharing one binding spec.
type ProviderRuntimeBindingSupport interface {
	SupportsBinding(openbindings.PreparedBindingDescriptor) bool
}

// ProviderRuntimeCloser is the optional provider-runtime lifecycle seam.
type ProviderRuntimeCloser interface {
	Close() error
}

type compiledOperationBehavior struct {
	invoker         *OperationInvoker
	interface_      *openbindings.Interface
	operation       *openbindings.Operation
	binding         *openbindings.BindingEntry
	source          *openbindings.Source
	operationKey    string
	bindingKey      string
	inputValidator  *jsonschema.Schema
	outputValidator *jsonschema.Schema
	compiledBinding CompiledBindingInvoker
}

func (b *compiledOperationBehavior) Invoke(ctx context.Context, opts ...InvokeOption) Invocation[any, any] {
	var cfg invokeConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	caller := NewInvocationImpl[any, any](ctx)
	if b.inputValidator != nil {
		caller.validateInput = func(input any) *InvocationError {
			if err := b.inputValidator.Validate(input); err != nil {
				return NewInvocationError(ErrCodeOperationValidationFailed)
			}
			return nil
		}
	}
	go func() {
		defer func() {
			if recover() != nil {
				caller.FireError(&InvocationError{Code: ErrCodeRuntime})
			}
		}()
		b.invoker.runCompiled(
			ctx, caller, b.interface_, b.operation, b.binding, b.bindingKey,
			b.source, cfg.context, b.operationKey,
			b.invoker.snapshotHooks(cfg.hooks), b.outputValidator, true,
			b.compiledBinding,
		)
	}()
	return caller
}

func (b *compiledOperationBehavior) Preflight(ctx context.Context, opts ...InvokeOption) (*ContextRequiredDetails, error) {
	var cfg invokeConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	args := &BindingInvocationArgs{
		Source: InvocationSource{
			BindingSpec: b.source.BindingSpec,
			Location:    b.source.Location,
			Content:     b.source.Content,
		},
		Selector:             b.binding.Selector,
		Binding:              b.binding,
		Context:              cfg.context,
		Interface:            b.interface_,
		InputSchema:          b.operation.Input,
		Hooks:                b.invoker.snapshotHooks(cfg.hooks),
		MaxDeliveryUnitBytes: b.invoker.MaxDeliveryUnitBytes,
	}
	args.Site = &InvokeSite{
		Operation:   b.operationKey,
		InvokedAs:   b.operationKey,
		BindingKey:  b.bindingKey,
		BindingSpec: b.source.BindingSpec,
		Selector:    b.binding.Selector,
	}
	if runtime := b.invoker.invoker.findInvoker(b.source.BindingSpec); runtime != nil {
		stampSite(args.Site, runtime)
	} else {
		args.Site.seamStamped = true
	}
	if b.compiledBinding != nil {
		return b.compiledBinding.PrepareBinding(ctx, args)
	}
	return b.invoker.PrepareBinding(ctx, args)
}

// CompileRealization performs deterministic closure for one exact prepared
// binding. Live context preflight remains a separate call on the result.
func (e *OperationInvoker) CompileRealization(
	ctx context.Context,
	prepared *openbindings.PreparedInterface,
	binding openbindings.PreparedBindingDescriptor,
) (CompiledRealizationBehavior, error) {
	snapshot := prepared.InterfaceSnapshot()
	if snapshot == nil {
		return nil, fmt.Errorf("openbindings: prepared interface snapshot is unavailable")
	}
	return e.CompileRealizationSnapshot(ctx, prepared, snapshot, binding)
}

// CompileRealizationSnapshot closes one binding over a provider-wide snapshot
// so multiple retained routes never retain duplicate full OBI copies.
func (e *OperationInvoker) CompileRealizationSnapshot(
	ctx context.Context,
	prepared *openbindings.PreparedInterface,
	snapshot *openbindings.Interface,
	binding openbindings.PreparedBindingDescriptor,
) (CompiledRealizationBehavior, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || e.invoker == nil {
		return nil, fmt.Errorf("openbindings: operation invoker is required")
	}
	actual, ok := prepared.Binding(binding.Key)
	if !ok || actual != binding {
		return nil, fmt.Errorf("openbindings: binding %q is not part of the prepared interface", binding.Key)
	}
	supported := false
	for _, info := range e.BindingSpecs() {
		if info.BindingSpec == binding.BindingSpec {
			supported = true
			break
		}
	}
	if !supported {
		return nil, &InvocationError{Code: ErrCodeBindingNotFound}
	}
	if binding.HasTransforms && e.TransformEvaluator == nil {
		return nil, &InvocationError{Code: ErrCodeTransformError}
	}
	inputValidator, _, err := prepared.SchemaValidator(binding.OperationKey, "input")
	if err != nil {
		return nil, &InvocationError{Code: ErrCodeSchemaUnresolved}
	}
	outputValidator, _, err := prepared.SchemaValidator(binding.OperationKey, "output")
	if err != nil {
		return nil, &InvocationError{Code: ErrCodeSchemaUnresolved}
	}
	if snapshot == nil {
		return nil, fmt.Errorf("openbindings: prepared interface snapshot is unavailable")
	}
	operation, ok := snapshot.Operations[binding.OperationKey]
	if !ok {
		return nil, fmt.Errorf("openbindings: prepared operation %q is unavailable", binding.OperationKey)
	}
	bindingEntry, ok := snapshot.Bindings[binding.Key]
	if !ok {
		return nil, fmt.Errorf("openbindings: prepared binding %q is unavailable", binding.Key)
	}
	source, ok := snapshot.Sources[binding.SourceKey]
	if !ok {
		return nil, fmt.Errorf("openbindings: prepared source %q is unavailable", binding.SourceKey)
	}
	var compiledBinding CompiledBindingInvoker
	if runtime := e.invoker.findInvoker(binding.BindingSpec); runtime != nil {
		if compiler, ok := runtime.(BindingCompiler); ok {
			args := &BindingInvocationArgs{
				Source: InvocationSource{
					BindingSpec: source.BindingSpec,
					Location:    source.Location,
					Content:     source.Content,
				},
				Selector:    bindingEntry.Selector,
				Binding:     &bindingEntry,
				Interface:   snapshot,
				InputSchema: operation.Input,
				Site: &InvokeSite{
					Operation:   binding.OperationKey,
					InvokedAs:   binding.OperationKey,
					BindingKey:  binding.Key,
					BindingSpec: binding.BindingSpec,
					Selector:    binding.Selector,
				},
			}
			stampSite(args.Site, runtime)
			compiledBinding, err = compiler.CompileBinding(args)
			if err != nil {
				return nil, err
			}
			if compiledBinding == nil {
				return nil, fmt.Errorf("openbindings: binding compiler returned nil behavior")
			}
		}
	}
	return &compiledOperationBehavior{
		invoker:         e,
		interface_:      snapshot,
		operation:       &operation,
		binding:         &bindingEntry,
		source:          &source,
		operationKey:    binding.OperationKey,
		bindingKey:      binding.Key,
		inputValidator:  inputValidator,
		outputValidator: outputValidator,
		compiledBinding: compiledBinding,
	}, nil
}

var _ ProviderRuntime = (*OperationInvoker)(nil)
var _ ProviderRuntimeSnapshotCompiler = (*OperationInvoker)(nil)
