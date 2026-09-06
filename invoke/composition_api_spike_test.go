package invoke_test

// This file is the compile/test half of the S7 two-language API spike. The
// roles are deliberately test-local until TypeScript usability/performance
// evidence closes the naming gate; they ensure the shared architecture has an
// idiomatic Go expression before any public Go composition API freezes.

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

type contractVerdict string

const (
	contractCompatible    contractVerdict = "compatible"
	contractIncompatible  contractVerdict = "incompatible"
	contractIndeterminate contractVerdict = "indeterminate"
)

type realizationBehavior interface {
	Invoke(context.Context, ...invoke.InvokeOption) invoke.Invocation[any, any]
	Preflight(context.Context, ...invoke.InvokeOption) (*invoke.ContextRequiredDetails, error)
}

// providerRuntime supplies behavior for an SDK-selected descriptor. There is
// intentionally no returned operation/binding/spec metadata to trust.
type providerRuntime interface {
	BindingSpecs() []openbindings.BindingSpecInfo
	Close(context.Context, *openbindings.PreparedInterface, openbindings.PreparedBindingDescriptor) (realizationBehavior, error)
}

type preparedProvider struct {
	key               string
	interface_        *openbindings.PreparedInterface
	runtime           providerRuntime
	selectRealization func([]openbindings.PreparedBindingDescriptor) (string, bool)
}

type operationCorrespondence struct {
	Identifier string
	Required   openbindings.PreparedOperationDescriptor
	Provided   openbindings.PreparedOperationDescriptor
}

type contractEvidence struct {
	Verdict contractVerdict
	Method  string
	Detail  string
}

type providerCandidate struct {
	Key        string
	Preference float64
}

type compositionPolicy interface {
	ID() string
	Correspondences(openbindings.PreparedOperationDescriptor, *openbindings.PreparedInterface) []operationCorrespondence
	Assess(context.Context, *openbindings.PreparedInterface, operationCorrespondence, *openbindings.PreparedInterface) (contractEvidence, error)
	SelectProvider([]providerCandidate) (key string, ambiguous []string, ok bool)
	SelectRealization([]openbindings.PreparedBindingDescriptor, func([]openbindings.PreparedBindingDescriptor) (string, bool)) (key string, ambiguous []string, ok bool)
}

type providerRegistration struct {
	Provider   *preparedProvider
	Preference float64
}

type compositionSession struct {
	Consumer  *openbindings.PreparedInterface
	Providers []providerRegistration
	Policy    compositionPolicy
}

type preparedDependencyRoute[I, O any] struct {
	PolicyID          string
	ConsumerRevision  string
	DependencyKey     string
	RequiredOperation string
	ProviderKey       string
	ProviderOperation string
	BindingKey        string
	BindingSpec       string
	behavior          realizationBehavior
}

func (r *preparedDependencyRoute[I, O]) Preflight(ctx context.Context, opts ...invoke.InvokeOption) (*invoke.ContextRequiredDetails, error) {
	return r.behavior.Preflight(ctx, opts...)
}

func (r *preparedDependencyRoute[I, O]) Invoke(ctx context.Context, opts ...invoke.InvokeOption) invoke.Invocation[any, any] {
	return r.behavior.Invoke(ctx, opts...)
}

type dependencyResolution[I, O any] struct {
	Status      string
	Route       *preparedDependencyRoute[I, O]
	Ambiguity   []string
	Assessments []string
}

// Go does not permit methods to declare their own type parameters. The
// natural call therefore uses a generic free function, while a future
// non-generic session method can serve dynamic callers.
func resolveDependency[I, O any](ctx context.Context, session *compositionSession, dependencyKey string) (dependencyResolution[I, O], error) {
	_ = ctx
	_ = session
	_ = dependencyKey
	return dependencyResolution[I, O]{Status: "unavailable"}, nil
}

func TestCompositionAPIHasNaturalGoExpression(t *testing.T) {
	consumer, err := openbindings.PrepareInterface(&openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"deliver": {Input: true, Output: true},
		},
		Dependencies: map[string]openbindings.DependencyEntry{
			"delivery": {Operation: "deliver"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &compositionSession{Consumer: consumer}
	result, err := resolveDependency[string, string](context.Background(), session, "delivery")
	if err != nil || result.Status != "unavailable" {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if contractCompatible == contractIncompatible || contractCompatible == contractIndeterminate {
		t.Fatal("contract verdicts collapsed")
	}
}
