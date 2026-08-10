package openapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// openAPISynthesisCoverage inventories the interaction units that matter to
// OpenAPI synthesis fidelity in revision 1:
//   - every client-invoked paths operation;
//   - every independently selectable request media declaration;
//   - callbacks and 3.1 webhooks, which are upstream interactions but are
//     explicitly outside this revision's caller-to-service direction.
//
// Incorporated parameter serialization, response selection, server
// resolution, and security requirements are behavior of a represented target,
// not independently addressable units.
func openAPISynthesisCoverage(doc *openapi3.T, iface *openbindings.Interface, unrealizable map[string]unrealizableTarget) []openbindings.SynthesisCoverageEntry {
	if doc == nil || iface == nil {
		return []openbindings.SynthesisCoverageEntry{}
	}

	type bindingIdentity struct {
		operationKey string
		ref          string
	}
	byRef := make(map[string]bindingIdentity, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		byRef[binding.Ref] = bindingIdentity{operationKey: binding.Operation, ref: binding.Ref}
	}
	sourceLocation := ""
	bindingSpec := BindingSpec
	for _, source := range iface.Sources {
		if source.BindingSpec == BindingSpecV5 || source.BindingSpec == BindingSpecV4 || source.BindingSpec == BindingSpecV3 || source.BindingSpec == BindingSpecV2 || source.BindingSpec == LegacyBindingSpec {
			sourceLocation = source.Location
			bindingSpec = source.BindingSpec
			break
		}
	}

	var entries []openbindings.SynthesisCoverageEntry
	if doc.Paths != nil {
		pathKeys := make([]string, 0, doc.Paths.Len())
		for path := range doc.Paths.Map() {
			pathKeys = append(pathKeys, path)
		}
		sort.Strings(pathKeys)
		for _, path := range pathKeys {
			pathItem := doc.Paths.Find(path)
			if pathItem == nil {
				continue
			}
			for _, method := range httpMethods {
				op := pathItem.GetOperation(strings.ToUpper(method))
				if op == nil {
					continue
				}
				ref := buildJSONPointerRef(path, method)
				identity, ok := byRef[ref]
				if !ok {
					// Tolerant synthesis skipped this operation with a
					// recorded, spec-governed reason: a per-operation
					// exclusion, not an implementation defect. Anything else
					// genuinely missing remains an implementation invariant
					// violation.
					if skipped, recorded := unrealizable[ref]; recorded {
						entries = append(entries, openbindings.SynthesisCoverageEntry{
							SourceIndex: 0,
							SourceRef:   ref,
							Scope:       openbindings.SynthesisCoverageTarget,
							Status:      openbindings.SynthesisExcluded,
							ReasonCode:  skipped.reasonCode,
							Rule:        skipped.rule,
							Message:     skipped.message,
						})
						continue
					}
					entries = append(entries, openbindings.SynthesisCoverageEntry{
						SourceIndex: 0,
						SourceRef:   ref,
						Scope:       openbindings.SynthesisCoverageTarget,
						Status:      openbindings.SynthesisImplementationUnsupported,
						ReasonCode:  "openapi.missing_emitted_binding",
						Message:     "the synthesizer returned without emitting this admitted paths operation",
					})
					continue
				}
				targetRequirements := openAPIServerRequirements(doc, pathItem, op, sourceLocation)
				targetRequirements = append(targetRequirements, openAPIRequestMediaRequirements(doc, pathItem, op, bindingSpec)...)
				entries = append(entries, openbindings.SynthesisCoverageEntry{
					SourceIndex:  0,
					SourceRef:    ref,
					Scope:        openbindings.SynthesisCoverageTarget,
					Status:       openbindings.SynthesisRepresented,
					OperationKey: identity.operationKey,
					BindingRef:   identity.ref,
					Requirements: targetRequirements,
				})
				entries = append(entries, openAPIRequestMediaCoverage(doc, op, pathItem, identity, bindingSpec)...)
				entries = append(entries, openAPICallbackCoverage(op, ref)...)
			}
		}
	}
	entries = append(entries, openAPIWebhookCoverage(doc)...)
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
		return []string{"configuration.server"}
	}
	return nil
}

func openAPIRequestMediaCoverage(doc *openapi3.T, op *openapi3.Operation, pathItem *openapi3.PathItem, identity struct {
	operationKey string
	ref          string
}, bindingSpec string) []openbindings.SynthesisCoverageEntry {
	if op.RequestBody == nil || op.RequestBody.Value == nil || len(op.RequestBody.Value.Content) == 0 {
		return nil
	}
	params := effectiveParameters(pathItem, op)
	plans, planErr := planRequestBodiesFor(doc, op, bindingSpec)
	planned := make(map[string]bool, len(plans))
	represented := make(map[string]bool, len(plans))
	requiresRequestMedia := make(map[string]bool, len(plans))
	for _, plan := range plans {
		planned[plan.mediaKey] = true
		if usesRoutedInput(bindingSpec) || !candidateCollides(params, plan) {
			represented[plan.mediaKey] = true
			requiresRequestMedia[plan.mediaKey] = plan.mediaRange
		}
	}
	mediaKeys := make([]string, 0, len(op.RequestBody.Value.Content))
	for mediaKey := range op.RequestBody.Value.Content {
		mediaKeys = append(mediaKeys, mediaKey)
	}
	sort.Strings(mediaKeys)
	entries := make([]openbindings.SynthesisCoverageEntry, 0, len(mediaKeys))
	for _, mediaKey := range mediaKeys {
		sourceRef := identity.ref + "/requestBody/content/" + escapeJSONPointerToken(mediaKey)
		if represented[mediaKey] {
			var requirements []string
			if requiresRequestMedia[mediaKey] {
				requirements = []string{"configuration.requestMedia"}
			}
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex:  0,
				SourceRef:    sourceRef,
				Scope:        openbindings.SynthesisCoverageAlternative,
				Status:       openbindings.SynthesisRepresented,
				OperationKey: identity.operationKey,
				BindingRef:   identity.ref,
				Requirements: requirements,
			})
			continue
		}
		reasonCode := "openapi.request_media_excluded"
		message := "request media alternative has no faithful revision-1 carriage"
		rule := "OAPI-P-04"
		if planned[mediaKey] {
			reasonCode = "openapi.flattening_collision"
			message = "request media alternative collides with an independently declared parameter in revision 1's flattened boundary"
			rule = "OAPI-P-03"
		} else if planErr != nil {
			message = planErr.Error()
		}
		entries = append(entries, openbindings.SynthesisCoverageEntry{
			SourceIndex: 0,
			SourceRef:   sourceRef,
			Scope:       openbindings.SynthesisCoverageAlternative,
			Status:      openbindings.SynthesisExcluded,
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

func openAPICallbackCoverage(op *openapi3.Operation, parentRef string) []openbindings.SynthesisCoverageEntry {
	if len(op.Callbacks) == 0 {
		return nil
	}
	callbackNames := make([]string, 0, len(op.Callbacks))
	for name := range op.Callbacks {
		callbackNames = append(callbackNames, name)
	}
	sort.Strings(callbackNames)
	var entries []openbindings.SynthesisCoverageEntry
	for _, name := range callbackNames {
		callbackRef := op.Callbacks[name]
		if callbackRef == nil || callbackRef.Value == nil {
			continue
		}
		expressions := make([]string, 0, callbackRef.Value.Len())
		for expression := range callbackRef.Value.Map() {
			expressions = append(expressions, expression)
		}
		sort.Strings(expressions)
		for _, expression := range expressions {
			pathItem := callbackRef.Value.Value(expression)
			if pathItem == nil {
				continue
			}
			for _, method := range httpMethods {
				if pathItem.GetOperation(strings.ToUpper(method)) == nil {
					continue
				}
				entries = append(entries, excludedReverseOpenAPIInteraction(
					parentRef+"/callbacks/"+escapeJSONPointerToken(name)+"/"+escapeJSONPointerToken(expression)+"/"+method,
				))
			}
		}
	}
	return entries
}

func openAPIWebhookCoverage(doc *openapi3.T) []openbindings.SynthesisCoverageEntry {
	if len(doc.Webhooks) == 0 {
		return nil
	}
	names := make([]string, 0, len(doc.Webhooks))
	for name := range doc.Webhooks {
		names = append(names, name)
	}
	sort.Strings(names)
	var entries []openbindings.SynthesisCoverageEntry
	for _, name := range names {
		pathItem := doc.Webhooks[name]
		if pathItem == nil {
			entries = append(entries, openbindings.SynthesisCoverageEntry{
				SourceIndex: 0,
				SourceRef:   "#/webhooks/" + escapeJSONPointerToken(name),
				Scope:       openbindings.SynthesisCoverageTarget,
				Status:      openbindings.SynthesisInvalid,
				ReasonCode:  "openapi.invalid_webhook",
				Message:     "webhook path item is not an object",
			})
			continue
		}
		for _, method := range httpMethods {
			if pathItem.GetOperation(strings.ToUpper(method)) == nil {
				continue
			}
			entries = append(entries, excludedReverseOpenAPIInteraction(
				"#/webhooks/"+escapeJSONPointerToken(name)+"/"+method,
			))
		}
	}
	return entries
}

func excludedReverseOpenAPIInteraction(sourceRef string) openbindings.SynthesisCoverageEntry {
	return openbindings.SynthesisCoverageEntry{
		SourceIndex: 0,
		SourceRef:   sourceRef,
		Scope:       openbindings.SynthesisCoverageTarget,
		Status:      openbindings.SynthesisExcluded,
		ReasonCode:  "openapi.reverse_direction",
		Rule:        "OAPI-D-03",
		Message:     "callbacks and webhooks describe service-to-consumer requests outside openbindings.openapi@1",
	}
}

func escapeJSONPointerToken(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func formatCoverageRef(parts ...string) string {
	return fmt.Sprintf("#/%s", strings.Join(parts, "/"))
}
