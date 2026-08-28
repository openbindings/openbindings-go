package openapi

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/getkin/kin-openapi/openapi3"
	openapiclient "github.com/openbindings/openapi-client/go"

	openbindings "github.com/openbindings/openbindings-go"
)

type synthesisBindingIdentity struct {
	operationKey string
	selector     string
}

// openAPISynthesisCoverage inventories the interaction units that matter to
// OpenAPI synthesis fidelity in revision 1:
//   - every client-invoked paths operation;
//   - every independently selectable request media declaration;
//   - unusable server/security alternatives whose smallest-owner exclusions
//     must remain visible beside a represented sibling;
//   - callbacks and 3.1 webhooks, which are upstream interactions but are
//     explicitly outside this revision's caller-to-service direction.
//
// Incorporated parameter serialization and response selection are behavior of
// a represented target, not independently addressable units.
func openAPISynthesisCoverage(doc *openapi3.T, artifact *openapiclient.Artifact, iface *openbindings.Interface, unrealizable map[string]unrealizableTarget, floor *acceptanceFloor) []synthesize.SynthesisCoverageEntry {
	if doc == nil || iface == nil {
		return []synthesize.SynthesisCoverageEntry{}
	}

	bySelector := make(map[string]synthesisBindingIdentity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		bySelector[binding.Selector] = synthesisBindingIdentity{operationKey: binding.Operation, selector: binding.Selector}
	}
	sourceLocation := ""
	bindingSpec := ""
	for _, source := range iface.Sources {
		if isRequestImplementedOpenAPIBindingSpec(source.BindingSpec) {
			sourceLocation = source.Location
			bindingSpec = source.BindingSpec
			break
		}
	}
	if bindingSpec == "" {
		return []synthesize.SynthesisCoverageEntry{}
	}
	if artifact != nil && artifact.Edition.IsOpenAPI32() {
		return openAPI32SynthesisCoverage(artifact, iface, bySelector, unrealizable, floor, sourceLocation, bindingSpec)
	}

	// The walk is driven from the UNION of the loaded document's path×method
	// inventory and the acceptance floor's raw-tree inventory (block 8d
	// design §3): a ladder-invalid operation may be absent from the loaded
	// document (confined) or present (a loadable defect); either way its
	// invalid entry is owed. Deterministic order: sorted paths × the
	// httpMethods order.
	pathSet := map[string]bool{}
	if doc.Paths != nil {
		for path := range doc.Paths.Map() {
			pathSet[path] = true
		}
	}
	if floor != nil {
		for _, ref := range floor.OpOrder {
			pathSet[floor.Ops[ref].Path] = true
		}
	}

	var entries []synthesize.SynthesisCoverageEntry
	{
		pathKeys := make([]string, 0, len(pathSet))
		for path := range pathSet {
			pathKeys = append(pathKeys, path)
		}
		sort.Strings(pathKeys)
		for _, path := range pathKeys {
			var pathItem *openapi3.PathItem
			if doc.Paths != nil {
				pathItem = doc.Paths.Find(path)
			}
			for _, method := range httpMethods {
				var op *openapi3.Operation
				if pathItem != nil {
					op = pathItem.GetOperation(strings.ToUpper(method))
				}
				selector := buildJSONPointerSelector(path, method)
				verdict := floor.opVerdict(selector)
				if op == nil && verdict == nil {
					continue
				}
				if verdict != nil && verdict.Disposition == "invalid" {
					// A ladder-invalid target: one invalid target entry
					// carrying the owning unit and its defects, then the
					// operation's projection entries.
					entries = append(entries, synthesize.SynthesisCoverageEntry{
						SourceIndex: 0,
						SourceRef:   selector,
						Scope:       synthesize.SynthesisCoverageTarget,
						Status:      synthesize.SynthesisInvalid,
						ReasonCode:  invalidUnitReasonCode,
						Message:     floorInvalidTargetMessage(len(verdict.Defects)),
						Details:     map[string]any{"defects": floorDefectDetails(verdict.Defects)},
					})
					entries = append(entries, floorProjectionEntries(verdict)...)
					continue
				}
				if op == nil {
					// Raw-inventory-only and not ladder-invalid: nothing the
					// loaded document can account further (a confined or
					// unloadable position without a ladder verdict).
					continue
				}
				identity, ok := bySelector[selector]
				if !ok {
					// Tolerant synthesis skipped this operation with a
					// recorded, spec-governed reason: a per-operation
					// exclusion, not an implementation defect. Anything else
					// genuinely missing remains an implementation invariant
					// violation.
					if skipped, recorded := unrealizable[selector]; recorded {
						entries = append(entries, synthesize.SynthesisCoverageEntry{
							SourceIndex: 0,
							SourceRef:   selector,
							Scope:       synthesize.SynthesisCoverageTarget,
							Status:      synthesize.SynthesisExcluded,
							ReasonCode:  skipped.reasonCode,
							Rule:        skipped.rule,
							Message:     skipped.message,
						})
						// An excluded target is still ADDRESSED; its
						// ladder-invalid request media alternatives and
						// projection entries are owed regardless.
						entries = append(entries, floorInvalidAlternativeEntries(verdict)...)
						entries = append(entries, floorProjectionEntries(verdict)...)
						continue
					}
					entries = append(entries, synthesize.SynthesisCoverageEntry{
						SourceIndex: 0,
						SourceRef:   selector,
						Scope:       synthesize.SynthesisCoverageTarget,
						Status:      synthesize.SynthesisImplementationUnsupported,
						ReasonCode:  "openapi.missing_emitted_binding",
						Message:     "the synthesizer returned without emitting this admitted paths operation",
					})
					continue
				}
				targetRequirements := openAPIServerRequirements(doc, pathItem, op, sourceLocation)
				targetRequirements = append(targetRequirements, openAPISecurityRequirements(doc, op, effectiveParameters(pathItem, op))...)
				if !requestBodyIgnoredForBindingSpec(bindingSpec, method) {
					targetRequirements = append(targetRequirements, openAPIRequestMediaRequirements(doc, pathItem, op, bindingSpec)...)
				}
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex:     0,
					SourceRef:       selector,
					Scope:           synthesize.SynthesisCoverageTarget,
					Status:          synthesize.SynthesisRepresented,
					OperationKey:    identity.operationKey,
					BindingSelector: identity.selector,
					Requirements:    targetRequirements,
				})
				entries = append(entries, openAPIServerAlternativeCoverage(doc, pathItem, op, selector, identity.operationKey, bindingSpec, sourceLocation)...)
				entries = append(entries, openAPISecurityAlternativeCoverage(doc, op, effectiveParameters(pathItem, op), selector, identity.operationKey, bindingSpec)...)
				if !requestBodyIgnoredForBindingSpec(bindingSpec, method) {
					entries = append(entries, openAPIRequestMediaCoverage(doc, op, pathItem, identity, bindingSpec, verdict)...)
				}
				entries = append(entries, floorProjectionEntries(verdict)...)
			}
		}
	}
	entries = append(entries, openAPIInboundDependencyCoverage(doc, nil, iface, unrealizable, bindingSpec)...)
	return entries
}

func openAPI32SynthesisCoverage(
	artifact *openapiclient.Artifact,
	iface *openbindings.Interface,
	bySelector map[string]synthesisBindingIdentity,
	unrealizable map[string]unrealizableTarget,
	floor *acceptanceFloor,
	sourceLocation, bindingSpec string,
) []synthesize.SynthesisCoverageEntry {
	var entries []synthesize.SynthesisCoverageEntry
	for _, disposition := range artifact.OperationInventory() {
		selector := disposition.Reference.Ref
		verdict := floor.opVerdict(selector)
		if verdict != nil && verdict.Disposition == "invalid" {
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: selector, Scope: synthesize.SynthesisCoverageTarget,
				Status: synthesize.SynthesisInvalid, ReasonCode: invalidUnitReasonCode,
				Message: floorInvalidTargetMessage(len(verdict.Defects)), Details: map[string]any{"defects": floorDefectDetails(verdict.Defects)},
			})
			entries = append(entries, floorProjectionEntries(verdict)...)
			continue
		}
		identity, represented := bySelector[selector]
		if disposition.Err != nil || !represented {
			if skipped, recorded := unrealizable[selector]; recorded {
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: selector, Scope: synthesize.SynthesisCoverageTarget,
					Status: synthesize.SynthesisExcluded, ReasonCode: skipped.reasonCode, Rule: skipped.rule, Message: skipped.message,
				})
			} else if disposition.Err != nil {
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: selector, Scope: synthesize.SynthesisCoverageTarget,
					Status: synthesize.SynthesisExcluded, ReasonCode: "openapi.target_excluded", Rule: openAPIRule(bindingSpec, "P-01"), Message: disposition.Err.Error(),
				})
			} else {
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0, SourceRef: selector, Scope: synthesize.SynthesisCoverageTarget,
					Status: synthesize.SynthesisImplementationUnsupported, ReasonCode: "openapi.missing_emitted_binding",
					Message: "the synthesizer returned without emitting this admitted paths operation",
				})
			}
			entries = append(entries, floorInvalidAlternativeEntries(verdict)...)
			entries = append(entries, floorProjectionEntries(verdict)...)
			continue
		}
		target := disposition.Target
		if target == nil {
			continue
		}
		operation := target.Operation
		if len(target.ReferringSecuritySchemes) > 0 {
			copyOperation := *operation
			copyOperation.Extensions = make(map[string]any, len(operation.Extensions)+1)
			for name, value := range operation.Extensions {
				copyOperation.Extensions[name] = value
			}
			copyOperation.Extensions[referringSecuritySchemesMarker] = target.ReferringSecuritySchemes
			operation = &copyOperation
		}
		params := effectiveParameters(target.PathItem, operation)
		requirements := openAPIServerRequirements(target.Document, target.PathItem, operation, sourceLocation)
		requirements = append(requirements, openAPISecurityRequirements(target.Document, operation, params)...)
		if !requestBodyIgnoredForBindingSpec(bindingSpec, disposition.Reference.Method) {
			requirements = append(requirements, openAPIRequestMediaRequirements(target.Document, target.PathItem, operation, bindingSpec)...)
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: selector, Scope: synthesize.SynthesisCoverageTarget,
			Status: synthesize.SynthesisRepresented, OperationKey: identity.operationKey,
			BindingSelector: identity.selector, Requirements: requirements,
		})
		entries = append(entries, openAPIServerAlternativeCoverage(target.Document, target.PathItem, operation, selector, identity.operationKey, bindingSpec, sourceLocation)...)
		entries = append(entries, openAPISecurityAlternativeCoverage(target.Document, operation, params, selector, identity.operationKey, bindingSpec)...)
		if !requestBodyIgnoredForBindingSpec(bindingSpec, disposition.Reference.Method) {
			entries = append(entries, openAPIRequestMediaCoverage(target.Document, operation, target.PathItem, identity, bindingSpec, verdict)...)
		}
		for _, exclusion := range target.ResponseMediaExclusions {
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0,
				SourceRef:   selector + "/responses/" + escapeJSONPointerToken(exclusion.ResponseKey) + "/content/" + escapeJSONPointerToken(exclusion.MediaType),
				Scope:       synthesize.SynthesisCoverageAlternative, Status: synthesize.SynthesisExcluded,
				ReasonCode: "openapi.response_media_excluded", Rule: openAPIRule(bindingSpec, "P-01"), Message: exclusion.Reason,
				Details: map[string]any{"mediaType": exclusion.MediaType, "responseKey": exclusion.ResponseKey},
			})
		}
		entries = append(entries, floorProjectionEntries(verdict)...)
	}
	entries = append(entries, openAPIInboundDependencyCoverage(artifact.Document, artifact, iface, unrealizable, bindingSpec)...)
	return entries
}

func openAPIRequestMediaRequirements(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, bindingSpec string) []string {
	if !hasMediaFidelity(bindingSpec) || op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
		return nil
	}
	plans, err := planRequestBodiesFor(doc, op, bindingSpec)
	if err != nil {
		return nil
	}
	if len(plans) == 1 && len(plans[0].propertyMedia) > 0 {
		// Retain the corpus's established synthesis requirement label while
		// runtime configuration uses the family specification's propertyMedia
		// point.
		return []string{"configuration.propertyMedia"}
	}
	params := effectiveParameters(pathItem, op)
	hasRange := false
	for _, plan := range plans {
		if !usesRoutedInput(bindingSpec) && candidateCollides(params, plan) {
			continue
		}
		if plan.mediaRange {
			hasRange = true
			continue
		}
		return nil
	}
	if hasRange {
		return []string{"configuration.requestMedia"}
	}
	return nil
}

func openAPIServerRequirements(
	doc *openapi3.T,
	pathItem *openapi3.PathItem,
	op *openapi3.Operation,
	sourceLocation string,
) []string {
	if _, err := resolveServer(doc, pathItem, op, nil, sourceLocation); err != nil {
		var required *configRequired
		if !errors.As(err, &required) {
			return nil
		}
		return []string{"configuration.server"}
	}
	return nil
}

func openAPISecurityRequirements(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters) []string {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	var result []string
	if len(*requirements) > 1 {
		result = append(result, "configuration.security")
	}
	entryPlans := viableSecurityPlansWithContext(doc, op, contextWithConfigurationPoint(nil, "implicitConnectionScope", "entry"), "", params)
	referringPlans := viableSecurityPlansWithContext(doc, op, contextWithConfigurationPoint(nil, "implicitConnectionScope", "referring"), "", params)
	if len(entryPlans) == 0 && len(referringPlans) > 0 {
		result = append(result, "configuration.implicitConnectionScope")
	}
	return result
}

func openAPIServerAlternativeCoverage(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, selector, operationKey, bindingSpec, sourceLocation string) []synthesize.SynthesisCoverageEntry {
	servers, prefix := effectiveServerDeclaration(doc, pathItem, op, selector)
	if len(servers) == 0 {
		return nil
	}
	if len(servers) == 1 {
		eligible, _ := eligibleServers(servers, doc.OpenAPI, sourceLocation)
		if len(eligible) == 1 {
			if _, err := resolveServer(doc, pathItem, op, nil, sourceLocation); err == nil {
				// A sole usable alternative self-selects and owns no
				// configuration requirement independent of the target.
				return nil
			}
		}
	}
	entries := make([]synthesize.SynthesisCoverageEntry, 0, len(servers))
	for index, server := range servers {
		eligible, err := eligibleServers(openapi3.Servers{server}, doc.OpenAPI, sourceLocation)
		if len(eligible) != 0 {
			continue
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: fmt.Sprintf("%s/%d", prefix, index), Scope: synthesize.SynthesisCoverageAlternative,
			Status: synthesize.SynthesisExcluded, ReasonCode: "openapi.server_url_excluded", Rule: openAPIRule(bindingSpec, "P-04"), Message: err.Error(),
			OperationKey: operationKey, BindingSelector: selector,
		})
	}
	return entries
}

func effectiveServerDeclaration(doc *openapi3.T, pathItem *openapi3.PathItem, op *openapi3.Operation, selector string) (openapi3.Servers, string) {
	if op != nil && op.Servers != nil && len(*op.Servers) > 0 {
		return *op.Servers, selector + "/servers"
	}
	if pathItem != nil && len(pathItem.Servers) > 0 {
		return pathItem.Servers, selector[:strings.LastIndex(selector, "/")] + "/servers"
	}
	if doc != nil && len(doc.Servers) > 0 {
		return doc.Servers, "#/servers"
	}
	return nil, ""
}

func openAPISecurityAlternativeCoverage(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters, selector, operationKey, bindingSpec string) []synthesize.SynthesisCoverageEntry {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	prefix := "#/security"
	if op != nil && op.Security != nil {
		prefix = selector + "/security"
	}
	plans := append(
		securityPlansWithContext(doc, op, "", contextWithConfigurationPoint(nil, "implicitConnectionScope", "entry")),
		securityPlansWithContext(doc, op, "", contextWithConfigurationPoint(nil, "implicitConnectionScope", "referring"))...,
	)
	if len(*requirements) == 1 {
		for _, plan := range plans {
			if plan.authoredIndex == 0 && securityPlanCarriageError(plan, params) == nil {
				// A sole usable alternative self-selects; its credentials are
				// invocation prerequisites, not binding configuration.
				return nil
			}
		}
	}
	entries := make([]synthesize.SynthesisCoverageEntry, 0, len(*requirements))
	for index := range *requirements {
		usable := false
		for _, plan := range plans {
			if plan.authoredIndex == index && securityPlanCarriageError(plan, params) == nil {
				usable = true
				break
			}
		}
		if usable {
			continue
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: fmt.Sprintf("%s/%d", prefix, index), Scope: synthesize.SynthesisCoverageAlternative,
			Status: synthesize.SynthesisExcluded, ReasonCode: "openapi.security_alternative_unusable", Rule: openAPIRule(bindingSpec, "P-04"),
			Message:      "security alternative is malformed, unresolved, or collides with an owned request destination",
			OperationKey: operationKey, BindingSelector: selector,
		})
	}
	return entries
}

func openAPIRequestMediaCoverage(doc *openapi3.T, op *openapi3.Operation, pathItem *openapi3.PathItem, identity struct {
	operationKey string
	selector     string
}, bindingSpec string, verdict *floorOp) []synthesize.SynthesisCoverageEntry {
	if op.RequestBody == nil || op.RequestBody.Value == nil || len(op.RequestBody.Value.Content) == 0 {
		return nil
	}
	params := effectiveParameters(pathItem, op)
	plans, planErr := planRequestBodiesFor(doc, op, bindingSpec)
	// §9.2's normalized collision confines to the colliding parsed identity:
	// the colliding keys are excluded alternatives naming that identity, and
	// the map's non-colliding siblings stay represented beside them.
	colliding := normalizedMediaCollisions(op.RequestBody.Value.Content, bindingSpec)
	planned := make(map[string]bool, len(plans))
	represented := make(map[string]bool, len(plans))
	requiresRequestMedia := make(map[string]bool, len(plans))
	requiresPropertyMedia := make(map[string]bool, len(plans))
	for _, plan := range plans {
		planned[plan.mediaKey] = true
		if usesRoutedInput(bindingSpec) || !candidateCollides(params, plan) {
			represented[plan.mediaKey] = true
			requiresRequestMedia[plan.mediaKey] = plan.mediaRange
			requiresPropertyMedia[plan.mediaKey] = len(plan.propertyMedia) > 0
		}
	}
	mediaKeys := make([]string, 0, len(op.RequestBody.Value.Content))
	for mediaKey := range op.RequestBody.Value.Content {
		mediaKeys = append(mediaKeys, mediaKey)
	}
	sort.Strings(mediaKeys)
	entries := make([]synthesize.SynthesisCoverageEntry, 0, len(mediaKeys))
	for _, mediaKey := range mediaKeys {
		sourceRef := identity.selector + "/requestBody/content/" + escapeJSONPointerToken(mediaKey)
		if verdict != nil {
			if defects, invalid := verdict.InvalidAlternatives[sourceRef]; invalid {
				// The ladder invalidates this alternative: `invalid`, not
				// `excluded` -- the unit is malformed under its upstream
				// authority, not declined by the revision.
				entries = append(entries, synthesize.SynthesisCoverageEntry{
					SourceIndex: 0,
					SourceRef:   sourceRef,
					Scope:       synthesize.SynthesisCoverageAlternative,
					Status:      synthesize.SynthesisInvalid,
					ReasonCode:  invalidUnitReasonCode,
					Message:     floorInvalidAlternativeMessage(len(defects)),
					Details: map[string]any{
						"defects":   floorDefectDetails(defects),
						"mediaType": mediaKey,
					},
				})
				continue
			}
		}
		if represented[mediaKey] {
			var requirements []string
			if requiresRequestMedia[mediaKey] {
				requirements = []string{"configuration.requestMedia"}
			}
			if requiresPropertyMedia[mediaKey] {
				requirements = append(requirements, "configuration.propertyMedia")
			}
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex:     0,
				SourceRef:       sourceRef,
				Scope:           synthesize.SynthesisCoverageAlternative,
				Status:          synthesize.SynthesisRepresented,
				OperationKey:    identity.operationKey,
				BindingSelector: identity.selector,
				Requirements:    requirements,
			})
			continue
		}
		reasonCode := "openapi.request_media_excluded"
		message := "request media alternative has no faithful candidate carriage"
		rule := openAPIRule(bindingSpec, "P-03")
		if planned[mediaKey] {
			reasonCode = "openapi.flattening_collision"
			message = "request media alternative collides with an independently declared parameter in the candidate's application boundary"
			rule = openAPIRule(bindingSpec, "P-02")
		} else if identity, collides := colliding[mediaKey]; collides {
			message = fmt.Sprintf("request media alternative denotes the parsed media identity %s, which another declaration in this content map also denotes; no selection may land on a normalized-colliding identity", identity)
		} else if planErr != nil {
			message = planErr.Error()
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0,
			SourceRef:   sourceRef,
			Scope:       synthesize.SynthesisCoverageAlternative,
			Status:      synthesize.SynthesisExcluded,
			ReasonCode:  reasonCode,
			Rule:        rule,
			Message:     message,
			Details: map[string]any{
				"mediaType": mediaKey,
			},
		})
	}
	return entries
}

func openAPIInboundDependencyCoverage(doc *openapi3.T, artifact *openapiclient.Artifact, iface *openbindings.Interface, unrealizable map[string]unrealizableTarget, bindingSpec string) []synthesize.SynthesisCoverageEntry {
	inventory := openAPIInboundOperationInventory(doc, artifact)
	if len(inventory) == 0 {
		return nil
	}
	usedOperationKeys := map[string]bool{}
	for _, binding := range iface.Bindings {
		usedOperationKeys[binding.Operation] = true
	}
	dependencyOperations := map[string]bool{}
	for _, dependency := range iface.Dependencies {
		dependencyOperations[dependency.Operation] = true
	}
	var entries []synthesize.SynthesisCoverageEntry
	for _, disposition := range inventory {
		if disposition.Err != nil || disposition.Target == nil {
			message := "inbound OpenAPI operation is unresolvable"
			if disposition.Err != nil {
				message = disposition.Err.Error()
			}
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: disposition.Reference.Ref,
				Scope: synthesize.SynthesisCoverageTarget, Status: synthesize.SynthesisExcluded,
				ReasonCode: "openapi.inbound_dependency_excluded", Rule: openAPIRule(bindingSpec, "P-01"), Message: message,
			})
			continue
		}
		opKey := deriveInboundOperationKey(disposition.Target, usedOperationKeys)
		usedOperationKeys[opKey] = true
		if skipped, ok := unrealizable[disposition.Reference.Ref]; ok {
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: disposition.Reference.Ref,
				Scope: synthesize.SynthesisCoverageTarget, Status: synthesize.SynthesisExcluded,
				ReasonCode: skipped.reasonCode, Rule: skipped.rule, Message: skipped.message,
			})
			continue
		}
		if dependencyOperations[opKey] {
			entries = append(entries, synthesize.SynthesisCoverageEntry{
				SourceIndex: 0, SourceRef: disposition.Reference.Ref,
				Scope: synthesize.SynthesisCoverageDependency, Status: synthesize.SynthesisRepresented,
			})
			continue
		}
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0, SourceRef: disposition.Reference.Ref,
			Scope: synthesize.SynthesisCoverageTarget, Status: synthesize.SynthesisImplementationUnsupported,
			ReasonCode: "openapi.missing_emitted_dependency", Rule: openAPIRule(bindingSpec, "S-01"),
			Message: "the synthesizer returned without emitting this supported inbound dependency contract",
		})
	}
	return entries
}

func escapeJSONPointerToken(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func formatCoverageRef(parts ...string) string {
	return fmt.Sprintf("#/%s", strings.Join(parts, "/"))
}

// floorInvalidAlternativeEntries renders a ladder-invalid operation's or an
// excluded operation's invalid request media alternatives.
func floorInvalidAlternativeEntries(verdict *floorOp) []synthesize.SynthesisCoverageEntry {
	if verdict == nil || len(verdict.AltOrder) == 0 {
		return nil
	}
	entries := make([]synthesize.SynthesisCoverageEntry, 0, len(verdict.AltOrder))
	for _, altRef := range verdict.AltOrder {
		defects := verdict.InvalidAlternatives[altRef]
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0,
			SourceRef:   altRef,
			Scope:       synthesize.SynthesisCoverageAlternative,
			Status:      synthesize.SynthesisInvalid,
			ReasonCode:  invalidUnitReasonCode,
			Message:     floorInvalidAlternativeMessage(len(defects)),
			Details: map[string]any{
				"defects":   floorDefectDetails(defects),
				"mediaType": unescapeJSONPointerToken(altRef[strings.LastIndex(altRef, "/")+1:]),
			},
		})
	}
	return entries
}

// floorProjectionEntries renders one projection-scope entry per unit whose
// emitted closure reaches, or whose response rungs record, invalid positions
// that cost it nothing.
func floorProjectionEntries(verdict *floorOp) []synthesize.SynthesisCoverageEntry {
	if verdict == nil || len(verdict.ProjOrder) == 0 {
		return nil
	}
	entries := make([]synthesize.SynthesisCoverageEntry, 0, len(verdict.ProjOrder))
	for _, unit := range verdict.ProjOrder {
		defects := verdict.Projections[unit]
		entries = append(entries, synthesize.SynthesisCoverageEntry{
			SourceIndex: 0,
			SourceRef:   unit,
			Scope:       synthesize.SynthesisCoverageProjection,
			Status:      synthesize.SynthesisInvalid,
			ReasonCode:  invalidUnitReasonCode,
			Message:     floorProjectionMessage(len(defects)),
			Details:     map[string]any{"defects": floorDefectDetails(defects)},
		})
	}
	return entries
}

func unescapeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}
