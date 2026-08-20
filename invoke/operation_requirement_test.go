package invoke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/compare"
)

var operationRequirementContract = &openbindings.Interface{
	OpenBindings: "0.2.0",
	Operations: map[string]openbindings.Operation{
		"example.tasks.create": {
			Input:  map[string]any{"$ref": "#/schemas/CreateInput"},
			Output: map[string]any{"$ref": "#/schemas/CreateOutput"},
		},
		"example.tasks.list": {
			Output: map[string]any{"type": "array"},
		},
	},
	Schemas: map[string]openbindings.JSONSchema{
		"CreateInput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string"},
			},
			"required": []any{"title"},
		},
		"CreateOutput": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
			},
			"required": []any{"id"},
		},
	},
}

func operationRequirementCandidate(bindingSpec string, output openbindings.JSONSchema) *openbindings.Interface {
	if output == nil {
		output = map[string]any{"$ref": "#/schemas/CreateOutput"}
	}
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Name:         bindingSpec,
		Operations: map[string]openbindings.Operation{
			"createTodo": {
				Aliases: []string{"example.tasks.create"},
				Input:   map[string]any{"$ref": "#/schemas/CreateInput"},
				Output:  output,
			},
		},
		Schemas: operationRequirementContract.Schemas,
		Sources: map[string]openbindings.Source{
			"service": {BindingSpec: bindingSpec},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"createTodo": {
				Operation: "createTodo",
				Source:    "service",
				Ref:       "create",
			},
		},
	}
}

type operationRequirementBinding struct {
	bindingSpec string
	prefix      string
	requirement *ContextRequiredDetails
	invocations int
}

func (b *operationRequirementBinding) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: b.bindingSpec}}
}

func (b *operationRequirementBinding) PrepareBinding(context.Context, *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	return b.requirement, nil
}

func (b *operationRequirementBinding) InvokeBinding(ctx context.Context, _ *BindingInvocationArgs) Invocation[any, any] {
	b.invocations++
	invocation := NewInvocationImpl[any, any](ctx)
	go func() {
		input, err := invocation.ReadInput(ctx)
		if err != nil {
			if err != io.EOF {
				invocation.FireError(AsInvocationError(err))
			}
			return
		}
		_ = invocation.CloseInput()
		object, ok := input.(map[string]any)
		if !ok {
			invocation.FireError(&InvocationError{
				Code: ErrCodeRuntime,
			})
			return
		}
		if err := invocation.EmitOutput(map[string]any{
			"id": fmt.Sprintf("%s:%v", b.prefix, object["title"]),
		}); err != nil {
			return
		}
		invocation.CloseOutput()
	}()
	return invocation
}

func operationRequirementImplementation(
	bindingSpec, prefix string,
	preference float64,
	output openbindings.JSONSchema,
	requirement *ContextRequiredDetails,
) OperationImplementation {
	binding := &operationRequirementBinding{
		bindingSpec: bindingSpec,
		prefix:      prefix,
		requirement: requirement,
	}
	return OperationImplementation{
		Interface:  operationRequirementCandidate(bindingSpec, output),
		Invoker:    NewOperationInvoker(binding),
		Label:      prefix,
		Preference: preference,
	}
}

func newCreateRequirement(t *testing.T) OperationRequirement[map[string]any, map[string]any] {
	t.Helper()
	requirement, err := NewOperationRequirement(
		operationRequirementContract,
		NewOperationSignature[map[string]any, map[string]any]("example.tasks.create"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return requirement
}

func TestNewOperationRequirement(t *testing.T) {
	requirement := newCreateRequirement(t)
	if requirement.Interface != operationRequirementContract {
		t.Fatal("requirement did not retain its interface")
	}
	if requirement.Signature.Key() != "example.tasks.create" {
		t.Fatalf("signature key = %q", requirement.Signature.Key())
	}

	_, err := NewOperationRequirement(
		operationRequirementContract,
		NewOperationSignature[any, any]("example.tasks.remove"),
	)
	if err == nil {
		t.Fatal("missing operation was accepted")
	}
}

func TestCheckOperationCompatibilityIsPerOperation(t *testing.T) {
	issues, err := compare.CheckOperationCompatibility(
		operationRequirementContract,
		"example.tasks.create",
		operationRequirementCandidate("example.local@1", nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %#v; unrelated list operation should not participate", issues)
	}
}

func TestResolveOperationRequirementInvokesLocalImplementation(t *testing.T) {
	ctx := context.Background()
	resolution, err := ResolveOperationRequirement(
		ctx,
		newCreateRequirement(t),
		[]OperationImplementation{
			operationRequirementImplementation("example.local@1", "local", 0, nil, nil),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != OperationRequirementAvailable || resolution.Match == nil {
		t.Fatalf("resolution = %#v", resolution)
	}
	if resolution.Match.CanonicalOperation != "createTodo" {
		t.Fatalf("canonical operation = %q", resolution.Match.CanonicalOperation)
	}
	if resolution.Match.KnownContextRequirements != nil {
		t.Fatalf("unexpected requirements: %#v", resolution.Match.KnownContextRequirements)
	}

	call := resolution.Match.Invoke(ctx)
	if err := call.Write(ctx, map[string]any{"title": "draft"}); err != nil {
		t.Fatal(err)
	}
	output, err := Single(ctx, call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if output["id"] != "local:draft" {
		t.Fatalf("output = %#v", output)
	}
}

func TestResolveOperationRequirementSubstitutesBindingFamilies(t *testing.T) {
	ctx := context.Background()
	requirement := newCreateRequirement(t)
	one := operationRequirementImplementation("example.protocol-one@1", "one", 0, nil, nil)
	two := operationRequirementImplementation("example.protocol-two@1", "two", 0, nil, nil)

	for _, implementation := range []OperationImplementation{one, two} {
		resolution, err := ResolveOperationRequirement(ctx, requirement, []OperationImplementation{implementation})
		if err != nil {
			t.Fatal(err)
		}
		if resolution.Status != OperationRequirementAvailable {
			t.Fatalf("%s resolution = %#v", implementation.Label, resolution)
		}
		call := resolution.Match.Invoke(ctx)
		if err := call.Write(ctx, map[string]any{"title": "same-consumer"}); err != nil {
			t.Fatal(err)
		}
		output, err := Single(ctx, call.Outputs())
		if err != nil {
			t.Fatal(err)
		}
		want := implementation.Label + ":same-consumer"
		if output["id"] != want {
			t.Fatalf("%s output = %#v, want id %q", implementation.Label, output, want)
		}
	}
}

func TestResolveOperationRequirementPreferenceAndAmbiguity(t *testing.T) {
	ctx := context.Background()
	requirement := newCreateRequirement(t)
	one := operationRequirementImplementation("example.protocol-one@1", "one", 0, nil, nil)
	two := operationRequirementImplementation("example.protocol-two@1", "two", 0, nil, nil)

	tied, err := ResolveOperationRequirement(ctx, requirement, []OperationImplementation{one, two})
	if err != nil {
		t.Fatal(err)
	}
	if tied.Status != OperationRequirementAmbiguous || len(tied.Matches) != 2 {
		t.Fatalf("tied resolution = %#v", tied)
	}

	two.Preference = 10
	preferred, err := ResolveOperationRequirement(ctx, requirement, []OperationImplementation{one, two})
	if err != nil {
		t.Fatal(err)
	}
	if preferred.Status != OperationRequirementAvailable || preferred.Match.Implementation.Label != "two" {
		t.Fatalf("preferred resolution = %#v", preferred)
	}
}

func TestMatchOperationRequirementReturnsEveryOrderedMatch(t *testing.T) {
	ctx := context.Background()
	requirement := newCreateRequirement(t)
	one := operationRequirementImplementation("example.protocol-one@1", "one", -1, nil, nil)
	two := operationRequirementImplementation("example.protocol-two@1", "two", 10, nil, nil)
	three := operationRequirementImplementation("example.protocol-three@1", "three", 10, nil, nil)

	result, err := MatchOperationRequirement(
		ctx,
		requirement,
		[]OperationImplementation{one, two, three},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Assessments) != 0 {
		t.Fatalf("assessments = %#v", result.Assessments)
	}
	if len(result.Matches) != 3 {
		t.Fatalf("matches = %#v", result.Matches)
	}
	got := []string{
		result.Matches[0].Implementation.Label,
		result.Matches[1].Implementation.Label,
		result.Matches[2].Implementation.Label,
	}
	want := []string{"two", "three", "one"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels = %#v, want %#v", got, want)
		}
	}
}

func TestMatchOperationRequirementDoesNotInvokeCandidates(t *testing.T) {
	binding := &operationRequirementBinding{
		bindingSpec: "example.local@1",
		prefix:      "local",
	}
	result, err := MatchOperationRequirement(
		context.Background(),
		newCreateRequirement(t),
		[]OperationImplementation{{
			Interface: operationRequirementCandidate(binding.bindingSpec, nil),
			Invoker:   NewOperationInvoker(binding),
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %#v", result.Matches)
	}
	if binding.invocations != 0 {
		t.Fatalf("matching invoked the binding %d time(s)", binding.invocations)
	}
}

func TestResolveOperationRequirementRefusesIncompatibleSchema(t *testing.T) {
	incompatibleOutput := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "number"},
		},
		"required": []any{"id"},
	}
	resolution, err := ResolveOperationRequirement(
		context.Background(),
		newCreateRequirement(t),
		[]OperationImplementation{
			operationRequirementImplementation("example.local@1", "bad", 0, incompatibleOutput, nil),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != OperationRequirementUnavailable {
		t.Fatalf("resolution = %#v", resolution)
	}
	if len(resolution.Assessments) != 1 ||
		len(resolution.Assessments[0].Issues) != 1 ||
		resolution.Assessments[0].Issues[0].Kind != compare.CompatibilityOutputIncompatible {
		t.Fatalf("assessments = %#v", resolution.Assessments)
	}
}

func TestResolveOperationRequirementAttachesContextPreflight(t *testing.T) {
	durable := false
	requirements := &ContextRequiredDetails{
		Target: "local:test",
		Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{
			Type:    "approval.user",
			Durable: &durable,
		}}}},
	}
	resolution, err := ResolveOperationRequirement(
		context.Background(),
		newCreateRequirement(t),
		[]OperationImplementation{
			operationRequirementImplementation("example.local@1", "approval", 0, nil, requirements),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != OperationRequirementAvailable ||
		resolution.Match.KnownContextRequirements != requirements {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestResolveOperationRequirementRequiresInvocableBinding(t *testing.T) {
	resolution, err := ResolveOperationRequirement(
		context.Background(),
		newCreateRequirement(t),
		[]OperationImplementation{{
			Interface: operationRequirementCandidate("example.uninstalled@1", nil),
			Invoker:   NewOperationInvoker(),
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != OperationRequirementUnavailable ||
		len(resolution.Assessments) != 1 ||
		resolution.Assessments[0].Reason == "" {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestResolveOperationRequirementRefusesNonFinitePreference(t *testing.T) {
	implementation := operationRequirementImplementation("example.local@1", "bad-pref", 0, nil, nil)
	implementation.Preference = math.NaN()
	resolution, err := ResolveOperationRequirement(
		context.Background(),
		newCreateRequirement(t),
		[]OperationImplementation{implementation},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != OperationRequirementUnavailable ||
		len(resolution.Assessments) != 1 ||
		resolution.Assessments[0].Reason != "operation implementation preference must be a finite number" {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestResolveOperationRequirementReportsMalformedCandidates(t *testing.T) {
	tests := []struct {
		name           string
		implementation OperationImplementation
		reason         string
	}{
		{
			name: "missing interface",
			implementation: OperationImplementation{
				Invoker: NewOperationInvoker(),
			},
			reason: "operation implementation interface is required",
		},
		{
			name: "missing invoker",
			implementation: OperationImplementation{
				Interface: operationRequirementCandidate("example.local@1", nil),
			},
			reason: "operation implementation invoker is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := ResolveOperationRequirement(
				context.Background(),
				newCreateRequirement(t),
				[]OperationImplementation{test.implementation},
			)
			if err != nil {
				t.Fatal(err)
			}
			if resolution.Status != OperationRequirementUnavailable ||
				len(resolution.Assessments) != 1 ||
				resolution.Assessments[0].Reason != test.reason {
				t.Fatalf("resolution = %#v", resolution)
			}
		})
	}
}

// A cancelled context must stop MatchOperationRequirement rather than run the
// full candidate assessment: each candidate's PrepareOperation can do real
// work (schema compilation, discovery). Parity with the TS SDK, which
// throwIfAborted()s per candidate.
func TestMatchOperationRequirementHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	requirement := newCreateRequirement(t)
	one := operationRequirementImplementation("example.protocol-one@1", "one", -1, nil, nil)

	_, err := MatchOperationRequirement(
		ctx,
		requirement,
		[]OperationImplementation{one},
	)
	if err == nil {
		t.Fatal("cancelled context produced no error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got: %v", err)
	}
}
