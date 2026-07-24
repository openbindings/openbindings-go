package openbindings

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// OperationRequirement is one operation a consumer needs: its required
// contract and typed identifier. Interface is an ordinary OBI compatibility
// target, commonly unbound. This type adds no consumer fields or optionality
// semantics to that document; it merely pairs the runtime contract with the
// signature application code already invokes through.
type OperationRequirement[I, O any] struct {
	Interface *Interface
	Signature OperationSignature[I, O]
}

// NewOperationRequirement pairs a required interface with one operation it
// carries.
func NewOperationRequirement[I, O any](iface *Interface, signature OperationSignature[I, O]) (OperationRequirement[I, O], error) {
	if _, _, ok := ResolveOperation(iface, signature.Key()); !ok {
		return OperationRequirement[I, O]{}, fmt.Errorf("%w: %s", ErrOperationNotFound, signature.Key())
	}
	return OperationRequirement[I, O]{
		Interface: iface,
		Signature: signature,
	}, nil
}

// OperationImplementation is one concrete interface the application can use
// to satisfy requirements.
//
// Interface, Invoker, Label, and Preference are all application-owned runtime
// state. The SDK stores no registry. Label is diagnostic only and never
// becomes interface identity. Higher preference wins; equal highest
// preferences remain ambiguous.
type OperationImplementation struct {
	Interface  *Interface
	Invoker    *OperationInvoker
	Label      string
	Preference float64
}

// OperationImplementationAssessment explains why one concrete interface did
// not become an invocable match.
type OperationImplementationAssessment struct {
	Implementation OperationImplementation
	Issues         []CompatibilityIssue
	Reason         string
}

// OperationMatch is a compatible, invocable realization of one requirement.
//
// KnownContextRequirements is the advisory result of side-effect-free
// preflight. Nil means no requirement was knowable during resolution, not a
// guarantee that live invocation cannot raise CONTEXT_REQUIRED.
type OperationMatch[I, O any] struct {
	Requirement              OperationRequirement[I, O]
	Implementation           OperationImplementation
	CanonicalOperation       string
	KnownContextRequirements *ContextRequiredDetails
}

// OperationRequirementMatches contains every compatible, invocable match plus
// every rejected candidate assessment. Matches are ordered by caller-owned
// preference (higher first), preserving input order across ties.
type OperationRequirementMatches[I, O any] struct {
	Matches     []*OperationMatch[I, O]
	Assessments []OperationImplementationAssessment
}

// Invoke invokes this match through its concrete interface and operation
// invoker. It returns the ordinary cardinality-agnostic invocation handle.
func (m *OperationMatch[I, O]) Invoke(ctx context.Context, opts ...InvokeOption) *TypedInvocation[I, O] {
	return Invoke(ctx, m.Implementation.Invoker, m.Implementation.Interface, m.Requirement.Signature, opts...)
}

// Prepare repeats side-effect-free preflight, optionally with caller context
// or binding selection supplied through InvokeOption.
func (m *OperationMatch[I, O]) Prepare(ctx context.Context, opts ...InvokeOption) (*ContextRequiredDetails, error) {
	return m.Implementation.Invoker.PrepareOperation(
		ctx,
		m.Implementation.Interface,
		m.Requirement.Signature.Key(),
		opts...,
	)
}

// OperationRequirementStatus is the conservative outcome of resolving one
// operation requirement.
type OperationRequirementStatus string

const (
	// OperationRequirementAvailable means one uniquely highest-preference
	// compatible and invocable implementation exists.
	OperationRequirementAvailable OperationRequirementStatus = "available"
	// OperationRequirementAmbiguous means equally preferred matches remain
	// and the application must choose.
	OperationRequirementAmbiguous OperationRequirementStatus = "ambiguous"
	// OperationRequirementUnavailable means no compatible, invocable
	// implementation exists.
	OperationRequirementUnavailable OperationRequirementStatus = "unavailable"
)

// OperationRequirementResolution is the complete result of resolving one
// operation requirement. Exactly one of Match, Matches, or Assessments is
// populated according to Status.
type OperationRequirementResolution[I, O any] struct {
	Status      OperationRequirementStatus
	Match       *OperationMatch[I, O]
	Matches     []*OperationMatch[I, O]
	Assessments []OperationImplementationAssessment
}

type preferredOperationMatch[I, O any] struct {
	preference float64
	match      *OperationMatch[I, O]
}

// MatchOperationRequirement finds every compatible, invocable match for one
// operation requirement.
//
// Matching is deliberately conservative:
//  1. the required identifier must correspond by key or alias;
//  2. its schemas must satisfy the reference comparison profile;
//  3. the supplied operation invoker must resolve a concrete binding without
//     side effects.
//
// The returned matches are ordered by caller-owned preference, but this
// function selects nothing. Applications whose operation semantics aggregate,
// fan out, race, or fall through consume the matches according to their own
// policy.
//
// The function owns no registry and performs no invocation. Applications call
// it again whenever their interface/delegate state changes.
func MatchOperationRequirement[I, O any](
	ctx context.Context,
	requirement OperationRequirement[I, O],
	implementations []OperationImplementation,
) (*OperationRequirementMatches[I, O], error) {
	if _, _, ok := ResolveOperation(requirement.Interface, requirement.Signature.Key()); !ok {
		return nil, fmt.Errorf("%w: %s", ErrOperationNotFound, requirement.Signature.Key())
	}

	assessments := make([]OperationImplementationAssessment, 0)
	matches := make([]preferredOperationMatch[I, O], 0)

	for _, implementation := range implementations {
		if math.IsNaN(implementation.Preference) || math.IsInf(implementation.Preference, 0) {
			assessments = append(assessments, OperationImplementationAssessment{
				Implementation: implementation,
				Reason:         "operation implementation preference must be a finite number",
			})
			continue
		}
		if implementation.Interface == nil {
			assessments = append(assessments, OperationImplementationAssessment{
				Implementation: implementation,
				Reason:         "operation implementation interface is required",
			})
			continue
		}
		if implementation.Invoker == nil {
			assessments = append(assessments, OperationImplementationAssessment{
				Implementation: implementation,
				Reason:         "operation implementation invoker is required",
			})
			continue
		}

		issues, err := CheckOperationCompatibility(
			requirement.Interface,
			requirement.Signature.Key(),
			implementation.Interface,
		)
		if err != nil {
			return nil, err
		}
		if len(issues) > 0 {
			assessments = append(assessments, OperationImplementationAssessment{
				Implementation: implementation,
				Issues:         issues,
			})
			continue
		}

		canonicalOperation, _, ok := ResolveOperation(
			implementation.Interface,
			requirement.Signature.Key(),
		)
		if !ok {
			assessments = append(assessments, OperationImplementationAssessment{
				Implementation: implementation,
				Reason:         "operation correspondence disappeared during resolution",
			})
			continue
		}

		knownRequirements, err := implementation.Invoker.PrepareOperation(
			ctx,
			implementation.Interface,
			requirement.Signature.Key(),
		)
		if err != nil {
			assessments = append(assessments, OperationImplementationAssessment{
				Implementation: implementation,
				Reason:         err.Error(),
			})
			continue
		}

		matches = append(matches, preferredOperationMatch[I, O]{
			preference: implementation.Preference,
			match: &OperationMatch[I, O]{
				Requirement:              requirement,
				Implementation:           implementation,
				CanonicalOperation:       canonicalOperation,
				KnownContextRequirements: knownRequirements,
			},
		})
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].preference > matches[j].preference
	})
	ordered := make([]*OperationMatch[I, O], len(matches))
	for i, candidate := range matches {
		ordered[i] = candidate.match
	}
	return &OperationRequirementMatches[I, O]{
		Matches:     ordered,
		Assessments: assessments,
	}, nil
}

// ResolveOperationRequirement resolves one operation requirement for
// route-to-one use.
//
// This convenience applies only caller-owned preference: a unique highest
// match is available, no matches is unavailable, and an equal highest tie is
// ambiguous. It never uses slice order, interface name, binding order, or
// invoker registration order as a hidden election. Applications with
// aggregate/fan-out/race/fallback semantics use MatchOperationRequirement
// directly.
func ResolveOperationRequirement[I, O any](
	ctx context.Context,
	requirement OperationRequirement[I, O],
	implementations []OperationImplementation,
) (*OperationRequirementResolution[I, O], error) {
	result, err := MatchOperationRequirement(ctx, requirement, implementations)
	if err != nil {
		return nil, err
	}
	if len(result.Matches) == 0 {
		return &OperationRequirementResolution[I, O]{
			Status:      OperationRequirementUnavailable,
			Assessments: result.Assessments,
		}, nil
	}

	highest := result.Matches[0].Implementation.Preference
	preferred := make([]*OperationMatch[I, O], 0, len(result.Matches))
	for _, match := range result.Matches {
		if match.Implementation.Preference == highest {
			preferred = append(preferred, match)
		}
	}

	if len(preferred) != 1 {
		return &OperationRequirementResolution[I, O]{
			Status:  OperationRequirementAmbiguous,
			Matches: preferred,
		}, nil
	}
	return &OperationRequirementResolution[I, O]{
		Status: OperationRequirementAvailable,
		Match:  preferred[0],
	}, nil
}
