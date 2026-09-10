package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

const compositionTestSpec = "example.local@1"

type compositionTestBehavior struct{ preflights *atomic.Int64 }

func (b compositionTestBehavior) Invoke(ctx context.Context, _ ...InvokeOption) Invocation[any, any] {
	invocation := NewInvocationImpl[any, any](ctx)
	go func() {
		input, err := invocation.ReadInput(ctx)
		if err != nil {
			invocation.FireError(AsInvocationError(err))
			return
		}
		_ = invocation.CloseInput()
		if err := invocation.EmitOutput(input); err != nil {
			return
		}
		invocation.CloseOutput()
	}()
	return invocation
}

func (b compositionTestBehavior) Preflight(context.Context, ...InvokeOption) (*ContextRequiredDetails, error) {
	b.preflights.Add(1)
	return nil, nil
}

type compositionTestRuntime struct {
	specs      []openbindings.BindingSpecInfo
	compiled   atomic.Int64
	preflights atomic.Int64
	fail       bool
}

func (r *compositionTestRuntime) BindingSpecs() []openbindings.BindingSpecInfo {
	return append([]openbindings.BindingSpecInfo(nil), r.specs...)
}

func (r *compositionTestRuntime) CompileRealization(
	context.Context,
	*openbindings.PreparedInterface,
	openbindings.PreparedBindingDescriptor,
) (CompiledRealizationBehavior, error) {
	r.compiled.Add(1)
	if r.fail {
		return nil, errors.New("cannot close")
	}
	return compositionTestBehavior{preflights: &r.preflights}, nil
}

func compositionConsumer(t *testing.T, input openbindings.JSONSchema) *openbindings.PreparedInterface {
	t.Helper()
	prepared, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"deliver": {Input: input, Output: map[string]any{"type": "string"}},
		},
		Dependencies: map[string]openbindings.DependencyEntry{
			"delivery": {Operation: "deliver", BindingSpecs: []string{compositionTestSpec}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func compositionProvider(t *testing.T, bindings int, input openbindings.JSONSchema, runtime ProviderRuntime, key string) *PreparedProvider {
	t.Helper()
	entries := make(map[string]openbindings.BindingEntry)
	for index := 0; index < bindings; index++ {
		bindingKey := "binding" + string(rune('a'+index))
		entries[bindingKey] = openbindings.BindingEntry{Operation: "deliver", Source: "local", Selector: bindingKey}
	}
	prepared, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"deliver": {Input: input, Output: map[string]any{"type": "string"}},
		},
		Sources: map[string]openbindings.Source{
			"local": {BindingSpec: compositionTestSpec, Content: json.RawMessage(`{"handler":true}`)},
		},
		Bindings: entries,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := PrepareProvider(PreparedProviderOptions{Key: key, Interface: prepared, Runtime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func compositionRuntime() *compositionTestRuntime {
	return &compositionTestRuntime{specs: []openbindings.BindingSpecInfo{{BindingSpec: compositionTestSpec}}}
}

func TestCompositionSessionCapturesImmutableConfiguration(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string"})
	provider := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "primary")
	options := CompositionSessionOptions{
		Consumer:  consumer,
		Policy:    ReferenceCompositionPolicy,
		Providers: []ProviderRegistration{{Provider: provider}},
	}
	session, err := NewCompositionSession(options)
	if err != nil {
		t.Fatal(err)
	}
	revision := session.SessionID()
	options.Consumer = compositionConsumer(t, map[string]any{"type": "number"})
	options.Policy = invalidElectionPolicy{ReferenceCompositionPolicy, RealizationPolicySelection{Status: "invented"}}
	options.Providers[0] = ProviderRegistration{}
	if session.Consumer() != consumer || session.Policy() != ReferenceCompositionPolicy || session.SessionID() != revision {
		t.Fatal("constructor options altered retained session configuration")
	}
	result, err := session.resolve(t.Context(), "delivery")
	if err != nil || result.status != DependencyAvailable {
		t.Fatalf("resolution = %#v, %v", result, err)
	}
	inspection, err := session.InspectDependency(t.Context(), "delivery")
	if err != nil || inspection.SessionID != revision {
		t.Fatalf("inspection = %#v, %v", inspection, err)
	}
	// Prevent accidental reintroduction of caller-settable revision inputs.
	typ := reflect.TypeOf(*session)
	for index := 0; index < typ.NumField(); index++ {
		if typ.Field(index).IsExported() {
			t.Fatalf("mutable public session field: %s", typ.Field(index).Name)
		}
	}
}

func TestCompositionSessionResolvesWithoutLivePreflight(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string"})
	runtime := compositionRuntime()
	provider := compositionProvider(t, 1, map[string]any{"type": "string"}, runtime, "primary")
	session, err := NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: provider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ResolveDependency(
		shortCtx(t),
		session,
		NewDependencySignatureForOperation("delivery", NewOperationSignature[string, string]("deliver")),
	)
	if err != nil || result.Status != DependencyAvailable {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if runtime.compiled.Load() != 1 || runtime.preflights.Load() != 0 {
		t.Fatalf("compiled=%d preflights=%d", runtime.compiled.Load(), runtime.preflights.Load())
	}
	if _, err := result.Route.Preflight(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	if runtime.preflights.Load() != 1 {
		t.Fatal("explicit preflight did not reach behavior")
	}
	call := result.Route.Invoke(shortCtx(t))
	if err := call.Write(shortCtx(t), "hello"); err != nil {
		t.Fatal(err)
	}
	output, err := Single(shortCtx(t), call.Outputs())
	if err != nil || output != "hello" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestCompositionSessionSeparatesProviderAndRealizationAmbiguity(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string"})
	one := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "one")
	two := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "two")
	session, _ := NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: one}, {Provider: two}},
	})
	result, err := ResolveDependency(shortCtx(t), session, NewDynamicDependencySignature("delivery"))
	if err != nil || result.Status != DependencyAmbiguous || result.Ambiguity.Stage != "provider" {
		t.Fatalf("provider ambiguity = %#v, %v", result, err)
	}

	many := compositionProvider(t, 2, map[string]any{"type": "string"}, compositionRuntime(), "many")
	session, _ = NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: many}},
	})
	result, err = ResolveDependency(shortCtx(t), session, NewDynamicDependencySignature("delivery"))
	if err != nil || result.Status != DependencyAmbiguous || result.Ambiguity.Stage != "realization" {
		t.Fatalf("realization ambiguity = %#v, %v", result, err)
	}
}

type countingCompositionPolicy struct {
	CompositionPolicy
	low         *openbindings.PreparedInterface
	inspections atomic.Int64
}

func (p *countingCompositionPolicy) AssessContract(
	ctx context.Context,
	required *openbindings.PreparedInterface,
	correspondence OperationCorrespondence,
	provider *openbindings.PreparedInterface,
) (ContractEvidence, error) {
	if provider == p.low {
		p.inspections.Add(1)
	}
	return p.CompositionPolicy.AssessContract(ctx, required, correspondence, provider)
}

func TestCompositionSessionSkipsLowerPreferenceInspectionTier(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string"})
	high := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "high")
	low := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "low")
	policy := &countingCompositionPolicy{
		CompositionPolicy: ReferenceCompositionPolicy,
		low:               low.PreparedInterface(),
	}
	session, err := NewCompositionSession(CompositionSessionOptions{
		Consumer: consumer,
		Providers: []ProviderRegistration{
			{Provider: low, Preference: 0},
			{Provider: high, Preference: 10},
		},
		Policy: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ResolveDependency(shortCtx(t), session, NewDynamicDependencySignature("delivery"))
	if err != nil || result.Status != DependencyAvailable || result.Route.ProviderKey != "high" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if policy.inspections.Load() != 0 {
		t.Fatalf("lower tier inspected %d times", policy.inspections.Load())
	}
}

func TestReferenceCompositionPolicyPlansDescendingPreferenceTiers(t *testing.T) {
	groups := ReferenceCompositionPolicy.ProviderInspectionGroups([]ProviderPolicyCandidate{
		{ProviderKey: "low", Preference: 0},
		{ProviderKey: "high-b", Preference: 10},
		{ProviderKey: "high-a", Preference: 10},
	})
	if len(groups) != 2 || len(groups[0]) != 2 || len(groups[1]) != 1 ||
		groups[0][0].ProviderKey != "high-a" || groups[0][1].ProviderKey != "high-b" ||
		groups[1][0].ProviderKey != "low" {
		t.Fatalf("groups = %#v", groups)
	}
}

func TestCompositionSessionDoesNotFallbackAfterSelectedClosureFailure(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string"})
	highRuntime := compositionRuntime()
	highRuntime.fail = true
	high := compositionProvider(t, 1, map[string]any{"type": "string"}, highRuntime, "high")
	lowRuntime := compositionRuntime()
	low := compositionProvider(t, 1, map[string]any{"type": "string"}, lowRuntime, "low")
	session, _ := NewCompositionSession(CompositionSessionOptions{
		Consumer: consumer,
		Providers: []ProviderRegistration{
			{Provider: high, Preference: 10},
			{Provider: low, Preference: 0},
		},
	})
	result, err := ResolveDependency(shortCtx(t), session, NewDynamicDependencySignature("delivery"))
	if err != nil || result.Status != DependencyUnavailable {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if highRuntime.compiled.Load() != 1 || lowRuntime.compiled.Load() != 0 {
		t.Fatalf("unexpected fallback: high=%d low=%d", highRuntime.compiled.Load(), lowRuntime.compiled.Load())
	}
}

func TestCompositionSessionIndeterminateAndSerializableDiagnostics(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string", "pattern": "^[a-z]+$"})
	provider := compositionProvider(
		t, 1, map[string]any{"type": "string", "pattern": "^[A-Z]+$"}, compositionRuntime(), "pattern",
	)
	session, _ := NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: provider}},
	})
	inspection, err := session.InspectDependency(shortCtx(t), "delivery")
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Assessments) != 1 || inspection.Assessments[0].Code != "contract_indeterminate" {
		t.Fatalf("assessments = %#v", inspection.Assessments)
	}
	data, err := json.Marshal(inspection)
	if err != nil || len(data) == 0 {
		t.Fatalf("diagnostics serialization: %v", err)
	}
}

type stalledCompositionPolicy struct {
	CompositionPolicy
	started chan struct{}
	release chan struct{}
}

func (p stalledCompositionPolicy) AssessContract(
	ctx context.Context,
	required *openbindings.PreparedInterface,
	correspondence OperationCorrespondence,
	provider *openbindings.PreparedInterface,
) (ContractEvidence, error) {
	close(p.started)
	<-p.release // deliberately ignores ctx; the session must suppress stale publication
	return p.CompositionPolicy.AssessContract(context.Background(), required, correspondence, provider)
}

func TestCompositionSessionCancellationSuppressesStalledPolicyResult(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string"})
	provider := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "slow")
	policy := stalledCompositionPolicy{
		CompositionPolicy: ReferenceCompositionPolicy,
		started:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	session, _ := NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: provider}},
		Policy:    policy,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := ResolveDependency(ctx, session, NewDynamicDependencySignature("delivery"))
		done <- err
	}()
	<-policy.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not terminate promptly")
	}
	close(policy.release)
}

func TestPreparedProviderDisposalInvalidatesRetainedRoute(t *testing.T) {
	consumer := compositionConsumer(t, map[string]any{"type": "string"})
	provider := compositionProvider(t, 1, map[string]any{"type": "string"}, compositionRuntime(), "disposable")
	session, _ := NewCompositionSession(CompositionSessionOptions{
		Consumer:  consumer,
		Providers: []ProviderRegistration{{Provider: provider}},
	})
	result, err := ResolveDependency(shortCtx(t), session, NewDynamicDependencySignature("delivery"))
	if err != nil || result.Status != DependencyAvailable {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := result.Route.Preflight(shortCtx(t)); err == nil {
		t.Fatal("preflight after close succeeded")
	}
	call := result.Route.Invoke(shortCtx(t))
	if _, err := call.Outputs().Read(shortCtx(t)); err == nil || err == io.EOF {
		t.Fatalf("invocation after close err = %v", err)
	}
}
