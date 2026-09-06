package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

type testCompiledBehavior struct{}

func (testCompiledBehavior) Invoke(ctx context.Context, _ ...InvokeOption) Invocation[any, any] {
	invocation := NewInvocationImpl[any, any](ctx)
	invocation.CloseOutput()
	return invocation
}

func (testCompiledBehavior) Preflight(context.Context, ...InvokeOption) (*ContextRequiredDetails, error) {
	return nil, nil
}

type testProviderRuntime struct {
	compile func(context.Context) (CompiledRealizationBehavior, error)
	closed  atomic.Int32
}

func (r *testProviderRuntime) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: "example.concurrent@1"}}
}

func (r *testProviderRuntime) CompileRealization(
	ctx context.Context,
	_ *openbindings.PreparedInterface,
	_ openbindings.PreparedBindingDescriptor,
) (CompiledRealizationBehavior, error) {
	return r.compile(ctx)
}

func (r *testProviderRuntime) Close() error {
	r.closed.Add(1)
	return nil
}

func preparedConcurrentProvider(t *testing.T, runtime ProviderRuntime) *PreparedProvider {
	t.Helper()
	prepared, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"work": {},
		},
		Sources: map[string]openbindings.Source{
			"source": {
				BindingSpec: "example.concurrent@1",
				Content:     json.RawMessage(`{}`),
			},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"work.binding": {Operation: "work", Source: "source"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := PrepareProvider(PreparedProviderOptions{
		Key:       "concurrent",
		Interface: prepared,
		Runtime:   runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func TestPreparedProviderCloseRealizationSingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	runtime := &testProviderRuntime{compile: func(context.Context) (CompiledRealizationBehavior, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return testCompiledBehavior{}, nil
	}}
	provider := preparedConcurrentProvider(t, runtime)

	const callers = 24
	results := make(chan *PreparedRealization, callers)
	errors := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for range callers {
		go func() {
			defer group.Done()
			realization, err := provider.CloseRealization(context.Background(), "work.binding")
			results <- realization
			errors <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("compilation did not start")
	}
	close(release)
	group.Wait()
	close(results)
	close(errors)
	if got := calls.Load(); got != 1 {
		t.Fatalf("compiled %d times, want one", got)
	}
	var first *PreparedRealization
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for realization := range results {
		if first == nil {
			first = realization
		} else if realization != first {
			t.Fatal("single-flight callers received different realization identities")
		}
	}
}

func TestPreparedProviderFailedOrCancelledClosureCanRetry(t *testing.T) {
	var calls atomic.Int32
	runtime := &testProviderRuntime{compile: func(context.Context) (CompiledRealizationBehavior, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("transient compile failure")
		}
		return testCompiledBehavior{}, nil
	}}
	provider := preparedConcurrentProvider(t, runtime)
	if _, err := provider.CloseRealization(context.Background(), "work.binding"); err == nil {
		t.Fatal("expected first compile failure")
	}
	if _, err := provider.CloseRealization(context.Background(), "work.binding"); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("compile attempts = %d, want 2", got)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	calls.Store(0)
	runtime.compile = func(context.Context) (CompiledRealizationBehavior, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return testCompiledBehavior{}, nil
	}
	provider = preparedConcurrentProvider(t, runtime)
	ownerDone := make(chan error, 1)
	go func() {
		_, err := provider.CloseRealization(context.Background(), "work.binding")
		ownerDone <- err
	}()
	<-started
	waiterContext, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := provider.CloseRealization(waiterContext, "work.binding")
		waiterDone <- err
	}()
	cancel()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v", err)
	}
	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
	if _, err := provider.CloseRealization(context.Background(), "work.binding"); err != nil {
		t.Fatalf("cached realization after waiter cancellation: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("cancelled waiter poisoned cache; compile attempts = %d", got)
	}
}

func TestPreparedProviderCloseWaitsForCompilation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runtime := &testProviderRuntime{compile: func(context.Context) (CompiledRealizationBehavior, error) {
		close(started)
		<-release
		return testCompiledBehavior{}, nil
	}}
	provider := preparedConcurrentProvider(t, runtime)
	compileDone := make(chan error, 1)
	go func() {
		_, err := provider.CloseRealization(context.Background(), "work.binding")
		compileDone <- err
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- provider.Close() }()
	select {
	case <-closeDone:
		t.Fatal("provider closed its runtime while compilation was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-compileDone; err == nil {
		t.Fatal("in-flight closure published a realization after provider disposal")
	}
	if got := runtime.closed.Load(); got != 1 {
		t.Fatalf("runtime close count = %d", got)
	}
}

var _ ProviderRuntime = (*testProviderRuntime)(nil)
var _ ProviderRuntimeCloser = (*testProviderRuntime)(nil)
