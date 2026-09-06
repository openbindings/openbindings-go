package invoke

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

// RealizationSelector elects one binding key from an already ordered set of
// eligible realizations. ok=false declines every realization.
type RealizationSelector func([]ProviderRealizationDescriptor) (bindingKey string, ok bool)

// PreparedProviderOptions configures one application-owned provider snapshot.
type PreparedProviderOptions struct {
	Key               string
	Label             string
	Interface         *openbindings.PreparedInterface
	Runtime           ProviderRuntime
	SelectRealization RealizationSelector
}

// ProviderRealizationDescriptor is an SDK-derived concrete binding identity
// plus the installed runtime's exact support result.
type ProviderRealizationDescriptor struct {
	OperationKey string `json:"operationKey"`
	BindingKey   string `json:"bindingKey"`
	SourceKey    string `json:"sourceKey"`
	BindingSpec  string `json:"bindingSpec"`
	Selector     string `json:"selector"`
	Supported    bool   `json:"supported"`

	binding openbindings.PreparedBindingDescriptor
}

// ProviderDisposedError reports use after the explicit provider lifetime.
type ProviderDisposedError struct{ ProviderKey string }

func (e *ProviderDisposedError) Error() string {
	return fmt.Sprintf("openbindings: prepared provider is disposed: %s", e.ProviderKey)
}

// RealizationNotFoundError reports an absent or unsupported exact binding.
type RealizationNotFoundError struct {
	ProviderKey string
	BindingKey  string
}

func (e *RealizationNotFoundError) Error() string {
	return fmt.Sprintf("openbindings: provider %q has no executable realization for binding %q", e.ProviderKey, e.BindingKey)
}

// PreparedRealization is an SDK-owned executable closure for one exact
// provider binding. Runtime behavior cannot alter the exported identity.
type PreparedRealization struct {
	ProviderKey       string `json:"providerKey"`
	InterfaceRevision string `json:"interfaceRevision"`
	OperationKey      string `json:"operationKey"`
	BindingKey        string `json:"bindingKey"`
	SourceKey         string `json:"sourceKey"`
	BindingSpec       string `json:"bindingSpec"`
	Selector          string `json:"selector"`

	provider *PreparedProvider
	behavior CompiledRealizationBehavior
}

// Invoke starts an invocation or returns an already-errored handle when the
// provider's explicit lifetime has ended.
func (r *PreparedRealization) Invoke(ctx context.Context, opts ...InvokeOption) Invocation[any, any] {
	if err := r.provider.assertActive(); err != nil {
		return NewErroredInvocation[any, any](&InvocationError{Code: ErrCodeRuntime, Data: err.Error()})
	}
	return r.behavior.Invoke(ctx, opts...)
}

// Preflight evaluates current context without making a timeless liveness
// claim.
func (r *PreparedRealization) Preflight(ctx context.Context, opts ...InvokeOption) (*ContextRequiredDetails, error) {
	if err := r.provider.assertActive(); err != nil {
		return nil, err
	}
	return r.behavior.Preflight(ctx, opts...)
}

// PreparedProvider is an immutable provider catalog with a bounded, lazy
// realization-closure cache and explicit lifecycle.
type PreparedProvider struct {
	Key               string
	Label             string
	Interface         *openbindings.PreparedInterface
	SelectRealization RealizationSelector

	mu          sync.RWMutex
	runtime     ProviderRuntime
	snapshot    *openbindings.Interface
	specs       []openbindings.BindingSpecInfo
	descriptors map[string]ProviderRealizationDescriptor
	byOperation map[string][]ProviderRealizationDescriptor
	closures    map[string]*realizationClosure
	closeWG     sync.WaitGroup
	disposed    bool
}

type realizationClosure struct {
	mu          sync.Mutex
	realization *PreparedRealization
	inflight    *realizationAttempt
}

type realizationAttempt struct {
	done        chan struct{}
	realization *PreparedRealization
	err         error
}

// PrepareProvider creates the indexed catalog without compiling realizations
// or performing live preflight.
func PrepareProvider(options PreparedProviderOptions) (*PreparedProvider, error) {
	if strings.TrimSpace(options.Key) == "" {
		return nil, fmt.Errorf("openbindings: provider key is required")
	}
	if options.Interface == nil {
		return nil, fmt.Errorf("openbindings: prepared provider interface is required")
	}
	if options.Runtime == nil {
		return nil, fmt.Errorf("openbindings: provider runtime is required")
	}
	snapshot := options.Interface.InterfaceSnapshot()
	if snapshot == nil {
		return nil, fmt.Errorf("openbindings: prepared provider snapshot is unavailable")
	}
	specs := append([]openbindings.BindingSpecInfo(nil), options.Runtime.BindingSpecs()...)
	supportedSpecs := make(map[string]bool, len(specs))
	for _, info := range specs {
		supportedSpecs[info.BindingSpec] = true
	}
	descriptors := make(map[string]ProviderRealizationDescriptor)
	byOperation := make(map[string][]ProviderRealizationDescriptor)
	refiner, hasRefiner := options.Runtime.(ProviderRuntimeBindingSupport)
	for _, bindingKey := range options.Interface.BindingKeys() {
		binding, _ := options.Interface.Binding(bindingKey)
		supported := supportedSpecs[binding.BindingSpec]
		if supported && hasRefiner {
			supported = refiner.SupportsBinding(binding)
		}
		descriptor := ProviderRealizationDescriptor{
			OperationKey: binding.OperationKey,
			BindingKey:   binding.Key,
			SourceKey:    binding.SourceKey,
			BindingSpec:  binding.BindingSpec,
			Selector:     binding.Selector,
			Supported:    supported,
			binding:      binding,
		}
		descriptors[bindingKey] = descriptor
		byOperation[binding.OperationKey] = append(byOperation[binding.OperationKey], descriptor)
	}
	closures := make(map[string]*realizationClosure, len(descriptors))
	for bindingKey := range descriptors {
		closures[bindingKey] = &realizationClosure{}
	}
	return &PreparedProvider{
		Key:               options.Key,
		Label:             options.Label,
		Interface:         options.Interface,
		SelectRealization: options.SelectRealization,
		runtime:           options.Runtime,
		snapshot:          snapshot,
		specs:             specs,
		descriptors:       descriptors,
		byOperation:       byOperation,
		closures:          closures,
	}, nil
}

// BindingSpecs returns a private copy of installed capability metadata.
func (p *PreparedProvider) BindingSpecs() []openbindings.BindingSpecInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]openbindings.BindingSpecInfo(nil), p.specs...)
}

// Disposed reports whether Close ended the provider lifetime.
func (p *PreparedProvider) Disposed() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.disposed
}

// Realization returns one descriptor by exact binding key.
func (p *PreparedProvider) Realization(bindingKey string) (ProviderRealizationDescriptor, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	descriptor, ok := p.descriptors[bindingKey]
	return descriptor, ok
}

// RealizationsForOperation returns binding-key ordered descriptor copies.
func (p *PreparedProvider) RealizationsForOperation(operationIdentifier string) []ProviderRealizationDescriptor {
	op, ok := p.Interface.Operation(operationIdentifier)
	if !ok {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]ProviderRealizationDescriptor(nil), p.byOperation[op.CanonicalKey]...)
}

// CloseRealization performs deterministic closure once and no live preflight.
func (p *PreparedProvider) CloseRealization(ctx context.Context, bindingKey string) (*PreparedRealization, error) {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return nil, &ProviderDisposedError{ProviderKey: p.Key}
	}
	descriptor, ok := p.descriptors[bindingKey]
	if !ok || !descriptor.Supported {
		p.mu.Unlock()
		return nil, &RealizationNotFoundError{ProviderKey: p.Key, BindingKey: bindingKey}
	}
	closure := p.closures[bindingKey]
	p.closeWG.Add(1)
	p.mu.Unlock()
	defer p.closeWG.Done()

	closure.mu.Lock()
	if closure.realization != nil {
		realization := closure.realization
		closure.mu.Unlock()
		if err := p.assertActive(); err != nil {
			return nil, err
		}
		return realization, nil
	}
	if pending := closure.inflight; pending != nil {
		closure.mu.Unlock()
		select {
		case <-pending.done:
			if err := p.assertActive(); err != nil {
				return nil, err
			}
			return pending.realization, pending.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	attempt := &realizationAttempt{done: make(chan struct{})}
	closure.inflight = attempt
	closure.mu.Unlock()

	var behavior CompiledRealizationBehavior
	var compileErr error
	if compiler, ok := p.runtime.(ProviderRuntimeSnapshotCompiler); ok {
		behavior, compileErr = compiler.CompileRealizationSnapshot(ctx, p.Interface, p.snapshot, descriptor.binding)
	} else {
		behavior, compileErr = p.runtime.CompileRealization(ctx, p.Interface, descriptor.binding)
	}
	if compileErr == nil && behavior == nil {
		compileErr = fmt.Errorf("openbindings: provider runtime returned nil realization behavior")
	}
	if compileErr == nil {
		attempt.realization = &PreparedRealization{
			ProviderKey:       p.Key,
			InterfaceRevision: p.Interface.Revision(),
			OperationKey:      descriptor.OperationKey,
			BindingKey:        descriptor.BindingKey,
			SourceKey:         descriptor.SourceKey,
			BindingSpec:       descriptor.BindingSpec,
			Selector:          descriptor.Selector,
			provider:          p,
			behavior:          behavior,
		}
	} else {
		attempt.err = compileErr
	}
	closure.mu.Lock()
	if attempt.realization != nil {
		closure.realization = attempt.realization
	}
	closure.inflight = nil
	close(attempt.done)
	closure.mu.Unlock()
	if err := p.assertActive(); err != nil {
		return nil, err
	}
	return attempt.realization, attempt.err
}

// Close ends the provider lifetime, invalidates retained routes, and releases
// optional runtime resources. It is idempotent.
func (p *PreparedProvider) Close() error {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return nil
	}
	p.disposed = true
	runtime := p.runtime
	p.mu.Unlock()
	p.closeWG.Wait()
	if closer, ok := runtime.(ProviderRuntimeCloser); ok {
		return closer.Close()
	}
	return nil
}

func (p *PreparedProvider) assertActive() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.disposed {
		return &ProviderDisposedError{ProviderKey: p.Key}
	}
	return nil
}

func sortProviderRealizations(values []ProviderRealizationDescriptor) {
	sort.Slice(values, func(i, j int) bool { return values[i].BindingKey < values[j].BindingKey })
}
