package invoke

import (
	"context"
	"fmt"
	"math"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/compare"
)

// OperationRequirement is one operation a consumer needs: its required
// contract and typed identifier. Interface is an ordinary OBI compatibility
// target, commonly unbound. This type adds no consumer fields or optionality
// semantics to that document; it merely pairs the runtime contract with the
// signature application code already invokes through.
//
// Deprecated: declare a core dependency and use PreparedInterface plus
// CompositionSession. This compatibility family receives no new features.
type OperationRequirement[I, O any] struct {
	Interface *openbindings.Interface
	Signature OperationSignature[I, O]
}

// NewOperationRequirement pairs a required interface with one operation it
// carries.
//
// Deprecated: use a generated DependencySignature and CompositionSession.
func NewOperationRequirement[I, O any](iface *openbindings.Interface, signature OperationSignature[I, O]) (OperationRequirement[I, O], error) {
	if _, _, ok := openbindings.ResolveOperation(iface, signature.Key()); !ok {
		return OperationRequirement[I, O]{}, fmt.Errorf("%w: %s", openbindings.ErrOperationNotFound, signature.Key())
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
//
// Deprecated: use PreparedProvider and ProviderRegistration.
type OperationImplementation struct {
	Interface  *openbindings.Interface
	Invoker    *OperationInvoker
	Label      string
	Preference float64
}

// OperationImplementationAssessment explains why one concrete interface did
// not become an invocable match. Issues carries the comparison profile's
// complete findings for the operation when a proven contradiction excluded
// the candidate; Reason states the exclusion in prose.
type OperationImplementationAssessment struct {
	Implementation OperationImplementation
	Issues         []compare.CompatibilityIssue
	Reason         string
}

// OperationMatch is an invocable realization of one requirement whose
// correspondence claim the comparison profile did not contradict.
//
// Issues carries the profile's undecidable findings for the operation
// (compare.CompatibilityIssue with Undecidable set): positions it could not
// read and therefore neither confirms nor contradicts. They are evidence,
// not a defect; the provider's name or alias correspondence is its
// compatibility claim and stands. Nil when the profile decided every
// position.
//
// KnownContextRequirements is the result of the binding's preflight. It is
// advisory: it may omit requirements, and nil means none reported, not a
// guarantee that live invocation cannot raise CONTEXT_REQUIRED. A preflight
// that could not answer predicts nothing and also leaves it nil.
type OperationMatch[I, O any] struct {
	Requirement              OperationRequirement[I, O]
	Implementation           OperationImplementation
	CanonicalOperation       string
	Issues                   []compare.CompatibilityIssue
	KnownContextRequirements *ContextRequiredDetails
}

// OperationRequirementMatches contains every uncontradicted, invocable match
// plus every rejected candidate assessment. Matches are ordered by caller-owned
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

// Preflight resolves this match's operation as Invoke would and asks the selected
// binding which context requirements it can already identify. Supply
// context with WithContext; it is used for this call alone. A non-nil
// result has the shape a live CONTEXT_REQUIRED carries and may omit
// requirements; nil means none reported, not ready. An error means the
// binding could not answer and predicts nothing. Invoke preflights on its
// own before every attempt and consults ContextResolver then; this explicit
// call never does. Discard a result once the operation, binding or context
// changes.
func (m *OperationMatch[I, O]) Preflight(ctx context.Context, opts ...InvokeOption) (*ContextRequiredDetails, error) {
	return m.Implementation.Invoker.PreflightOperation(
		ctx,
		m.Implementation.Interface,
		m.Requirement.Signature.Key(),
		opts...,
	)
}

// OperationRequirementStatus is the outcome of resolving one operation
// requirement.
type OperationRequirementStatus string

const (
	// OperationRequirementAvailable means one uniquely highest-preference
	// uncontradicted and invocable implementation exists.
	OperationRequirementAvailable OperationRequirementStatus = "available"
	// OperationRequirementAmbiguous means equally preferred matches remain
	// and the application must choose.
	OperationRequirementAmbiguous OperationRequirementStatus = "ambiguous"
	// OperationRequirementUnavailable means no uncontradicted, invocable
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

// MatchOperationRequirement finds every invocable match for one operation
// requirement.
//
// A name or alias correspondence is the provider's compatibility claim. The
// SDK proceeds on the claim and looks for a contradiction:
//  1. the required identifier must correspond by key or alias;
//  2. the reference comparison profile must not prove the schemas
//     incompatible. Positions it cannot decide leave the claim standing and
//     ride the match as OperationMatch.Issues;
//  3. the supplied operation invoker must resolve a concrete binding. Its
//     preflight is asked for known context requirements; a preflight that
//     cannot answer predicts nothing and does not exclude the candidate,
//     unless it reports a resolution fact (ERR_OPERATION_NOT_FOUND,
//     ERR_BINDING_NOT_FOUND, ERR_BINDING_SELECTION_REQUIRED,
//     ERR_UNKNOWN_SOURCE), which proves the binding is not invocable.
//
// The returned matches are ordered by caller-owned preference, but this
// function selects nothing. Applications whose operation semantics aggregate,
// fan out, race, or fall through consume the matches according to their own
// policy.
//
// The function owns no registry and performs no invocation. Applications call
// it again whenever their interface/delegate state changes.
//
// Deprecated: use CompositionSession inspection or explanation.
func MatchOperationRequirement[I, O any](
	ctx context.Context,
	requirement OperationRequirement[I, O],
	implementations []OperationImplementation,
) (*OperationRequirementMatches[I, O], error) {
	if _, _, ok := openbindings.ResolveOperation(requirement.Interface, requirement.Signature.Key()); !ok {
		return nil, fmt.Errorf("%w: %s", openbindings.ErrOperationNotFound, requirement.Signature.Key())
	}

	assessments := make([]OperationImplementationAssessment, 0)
	matches := make([]preferredOperationMatch[I, O], 0)

	for _, implementation := range implementations {
		// Honor cancellation between candidates: each PreflightOperation below
		// may do real work (schema compilation, discovery), so a cancelled
		// context must stop the assessment loop rather than run to
		// completion. Parity with the TS SDK, which throwIfAborted()s per
		// candidate.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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

		issues, err := compare.CheckOperationCompatibility(
			requirement.Interface,
			requirement.Signature.Key(),
			implementation.Interface,
		)
		if err != nil {
			return nil, err
		}
		var undecidable []compare.CompatibilityIssue
		contradicted := false
		for _, issue := range issues {
			if issue.Undecidable {
				undecidable = append(undecidable, issue)
			} else {
				contradicted = true
			}
		}
		if contradicted {
			assessments = append(assessments, OperationImplementationAssessment{
				Implementation: implementation,
				Issues:         issues,
				Reason:         "the provided operation contract contradicts the required contract",
			})
			continue
		}

		canonicalOperation, _, ok := openbindings.ResolveOperation(
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

		knownRequirements, err := implementation.Invoker.PreflightOperation(
			ctx,
			implementation.Interface,
			requirement.Signature.Key(),
		)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if isResolutionError(err) {
				assessments = append(assessments, OperationImplementationAssessment{
					Implementation: implementation,
					Reason:         err.Error(),
				})
				continue
			}
			// The binding could not answer; an unsuccessful preflight
			// carries no prediction, so the match stands with nothing
			// known.
			knownRequirements = nil
		}

		matches = append(matches, preferredOperationMatch[I, O]{
			preference: implementation.Preference,
			match: &OperationMatch[I, O]{
				Requirement:              requirement,
				Implementation:           implementation,
				CanonicalOperation:       canonicalOperation,
				Issues:                   undecidable,
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

// isResolutionError reports whether err is an operation-invoker resolution
// fact: the operation, binding, or source could not be resolved, or a
// binding choice is still required. These prove the candidate is not
// invocable. Any other preflight error means the binding could not answer
// and predicts nothing.
func isResolutionError(err error) bool {
	switch wireError(err).Code {
	case ErrCodeOperationNotFound, ErrCodeBindingNotFound, ErrCodeBindingSelectionRequired, ErrCodeUnknownSource:
		return true
	}
	return false
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
//
// Deprecated: use ResolveDependency on a CompositionSession.
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
