package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/openbindings/openbindings-go/synthesize"

	"github.com/getkin/kin-openapi/openapi3"
	openapiclient "github.com/openbindings/openapi-client/go"

	openbindings "github.com/openbindings/openbindings-go"
)

// unrealizableTarget records a paths operation admitted by the artifact but
// unrepresentable at the sibling's synthesizable operation boundary. Reported instead of
// returned as an error when the caller opts into per-operation tolerance
// (the coverage and inspection surfaces), so one unrepresentable operation
// narrows coverage rather than vetoing the document (core §10's posture;
// interface-synthesizer contract's "sound partial OBI").
type unrealizableTarget struct {
	selector     string
	operationKey string
	reasonCode   string
	rule         string
	message      string
}

// convertDocToInterface converts a loaded OpenAPI document into an
// OpenBindings interface.
//
// When onUnrealizable is non-nil, an operation whose synthesizable operation
// boundary cannot be represented is reported and skipped — no operation, no
// binding — and synthesis continues (tolerant mode). When nil, the same
// condition returns an error (strict mode: SynthesizeInterface), preserving
// the convenient strict surface's guarantee that it never returns a
// statically unbindable partial interface without evidence.
func convertDocToInterface(doc *openapi3.T, location, bindingSpec string, warn func(synthesize.SynthesizerWarning), onUnrealizable func(unrealizableTarget)) (openbindings.Interface, error) {
	return convertDocToInterfaceWithOverlay(doc, location, bindingSpec, warn, onUnrealizable, nil, nil)
}

func convertDocToInterfaceWithOverlay(doc *openapi3.T, location, bindingSpec string, warn func(synthesize.SynthesizerWarning), onUnrealizable func(unrealizableTarget), schemaOverlays *rawSchemaOverlayCollector, floor *acceptanceFloor) (openbindings.Interface, error) {
	// The schema-dialect translation keys off the artifact's own declared
	// version (3.0 vs 3.1); the identifier stays exact and version-free.
	formatVersion := majorMinor(doc.OpenAPI)

	sourceEntry := openbindings.Source{
		BindingSpec: bindingSpec,
	}
	if location != "" {
		sourceEntry.Location = location
	}

	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations:   map[string]openbindings.Operation{},
		Bindings:     map[string]openbindings.BindingEntry{},
		Sources: map[string]openbindings.Source{
			DefaultSourceName: sourceEntry,
		},
	}

	if doc.Info != nil {
		iface.Name = doc.Info.Title
		iface.Version = doc.Info.Version
		iface.Description = doc.Info.Description
	}

	if doc.Paths == nil {
		return iface, nil
	}

	// Build a registry of `$ref → resolved schema` from
	// doc.Components.Schemas. Used to inline every `$ref` that survives
	// kin-openapi's MarshalJSON pass on operation input/output schemas.
	// See inlineRefs / buildRefRegistry above for the rationale.
	refRegistry := buildRefRegistry(doc, schemaOverlays)
	// Cut points are decided per direction, over the registry as that direction
	// will emit it: read/write projection can remove the only edge that closed a
	// cycle, and a cycle that is not in the emitted graph must not be cut there.
	// See cut_points.go for the convention and its TypeScript twin.
	requestGraph, responseGraph := newDirectionGraphs(refRegistry)
	namer := newCutPointNamer(location, schemaOverlays.externalComponents())

	usedKeys := map[string]bool{}

	// Sort paths alphabetically for deterministic output across languages.
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

			// The acceptance floor (the registered OpenAPI binding family §3): a
			// ladder-invalid target is not addressed. Tolerant surfaces skip
			// it (its invalid coverage entry is emitted by the coverage
			// walk); the strict surface refuses, preserving its guarantee.
			// Skipped BEFORE key derivation, in every engine identically.
			verdict := floor.opVerdict(buildJSONPointerSelector(path, method))
			if verdict != nil && verdict.Disposition == "invalid" {
				if onUnrealizable != nil {
					continue
				}
				return iface, fmt.Errorf("cannot synthesize OpenAPI operation at %q: %s; synthesis would return a statically unbindable partial interface", buildJSONPointerSelector(path, method), floorInvalidTargetMessage(len(verdict.Defects)))
			}

			opKey := deriveOperationKey(op, path, method, usedKeys)
			usedKeys[opKey] = true

			params := effectiveParameters(pathItem, op)
			if _, serverErr := openapiclient.EffectiveServerSet(doc, pathItem, op, location); serverErr != nil {
				reason := serverErr.Error()
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.server_url_excluded",
						rule:         openAPIRule(bindingSpec, "P-04"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}
			securityRequirements := effectiveSecurityRequirements(doc, op)
			if securityRequirements != nil && len(*securityRequirements) > 0 {
				entryPlans := viableSecurityPlansWithContext(doc, op, contextWithConfigurationPoint(nil, "implicitConnectionScope", "entry"), "", params)
				referringPlans := viableSecurityPlansWithContext(doc, op, contextWithConfigurationPoint(nil, "implicitConnectionScope", "referring"), "", params)
				if len(entryPlans) == 0 && len(referringPlans) == 0 {
					reason := "the effective security declaration has no usable complete alternative"
					if onUnrealizable != nil {
						onUnrealizable(unrealizableTarget{
							selector: buildJSONPointerSelector(path, method), operationKey: opKey,
							reasonCode: "openapi.security_alternative_unusable", rule: openAPIRule(bindingSpec, "P-04"), message: reason,
						})
						continue
					}
					return iface, unrealizableOperation(opKey, reason)
				}
			}
			if duplicate := duplicateEffectiveParameterIdentity(params); duplicate != "" {
				reason := fmt.Sprintf("parameter identity %q is declared more than once", duplicate)
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.duplicate_parameter_identity",
						rule:         openAPIRule(bindingSpec, "P-02"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}
			if field := unflattenableParamForRevision(params, bindingSpec); field != "" {
				reason := fmt.Sprintf("parameter %q has no unique flattened identity", field)
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.flattening_collision",
						rule:         openAPIRule(bindingSpec, "P-02"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}
			if parameter := malformedEffectiveParameterFor(params, bindingSpec); parameter != "" {
				reason := fmt.Sprintf("effective parameter %q violates the closed Parameter Object declaration list", parameter)
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.parameter_declaration_excluded",
						rule:         openAPIRule(bindingSpec, "P-02"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}
			if err := checkPathTemplateDeclaration(path, params, bindingSpec); err != nil || bindingSpec == BindingSpecOpenAPI31 && equivalentPathTemplateCollision(doc.Paths, path) != "" {
				reason := "the selected path declaration is ambiguous or does not correspond one-to-one with its effective path parameters"
				if err != nil {
					reason = err.Error()
				}
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.path_correspondence_excluded",
						rule:         openAPIRule(bindingSpec, "P-02"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}

			if parameter := unsupportedParameterContentFor(params, bindingSpec); parameter != "" {
				reason := fmt.Sprintf("parameter %q declares content with no faithful candidate carriage", parameter)
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.parameter_content_excluded",
						rule:         openAPIRule(bindingSpec, "P-02"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}

			// A style-lane parameter declaring a member with no defined
			// expansion can never be populated faithfully: every value
			// conforming to the declaration carries that member as a
			// composite, and the governing OAS style row defines no
			// representation for one. The refusal is decided by the
			// DECLARATION, so the operation is excluded here with durable
			// evidence rather than published as represented and refused at
			// invocation. See styleLaneUndefinedExpansionMember in media.go
			// for the per-edition authority reading.
			if member := styleLaneUndefinedExpansionParamFor(params, bindingSpec, isOpenAPI30(formatVersion)); member != "" {
				reason := fmt.Sprintf("parameter member %q has no expansion defined by its governing OAS style row", member)
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.parameter_style_expansion_excluded",
						rule:         openAPIRule(bindingSpec, "P-02"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}

			if parameter := formStyleCookieMultiValueParamFor(params, isOpenAPI30(formatVersion)); parameter != "" {
				reason := fmt.Sprintf("cookie parameter %q statically proves multi-pair form expansion", parameter)
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						selector:     buildJSONPointerSelector(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.cookie_multi_value_excluded",
						rule:         openAPIRule(bindingSpec, "P-02"),
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}

			inputOperation := op
			if requestBodyIgnoredForBindingSpec(bindingSpec, method) {
				copy := *op
				copy.RequestBody = nil
				inputOperation = &copy
			}
			var requestPlans []*bodyPlan
			invalidPropertyMediaCandidate := false
			if inputOperation.RequestBody != nil && inputOperation.RequestBody.Value != nil {
				plans, planErr := planRequestBodiesFor(doc, op, bindingSpec)
				for _, plan := range plans {
					invalidPropertyMediaCandidate = invalidPropertyMediaCandidate || len(plan.propertyMedia) > 0
				}
				// The acceptance floor (the registered OpenAPI binding family §3): a
				// ladder-invalid request media ALTERNATIVE is a unit that is
				// malformed under its upstream authority, so it is not a
				// candidate the operation may carry. It never climbs -- the
				// operation survives on its remaining alternatives, and a
				// REQUIRED body left with none falls to the existing
				// unresolvable-request-body exclusion below (OAPI-P-04),
				// carried and not reopened. Applied BEFORE the candidate count
				// the exclusion reason is chosen from, so a body whose only
				// alternative the ladder invalidated is not misreported as a
				// flattening collision.
				plans = filterLadderInvalidAlternatives(plans, verdict, buildJSONPointerSelector(path, method))
				plannedCount := len(plans)
				if planErr == nil {
					for _, plan := range plans {
						if usesRoutedInput(bindingSpec) || !candidateCollides(params, plan) {
							requestPlans = append(requestPlans, plan)
						}
					}
				}
				requiredBody := inputOperation.RequestBody.Value.Required
				if requiredBody && (planErr != nil || len(requestPlans) == 0) {
					reason := "no artifact-declared request media candidate can realize its required flattened input"
					if planErr != nil {
						reason = planErr.Error()
					}
					if onUnrealizable != nil {
						// Every plannable candidate colliding with an
						// independently declared parameter is the
						// flattening-identity refusal (OAPI-P-03); a candidate
						// set that never planned is the media-carriage refusal
						// (OAPI-P-04).
						allCollided := planErr == nil && plannedCount > 0
						code := "openapi.unresolvable_request_body"
						rule := openAPIRule(bindingSpec, "P-03")
						if allCollided {
							code = "openapi.flattening_collision"
							rule = openAPIRule(bindingSpec, "P-02")
						} else {
							var dme *degenerateMediaError
							if errors.As(planErr, &dme) {
								code = "openapi.media_schema_mismatch"
							} else if invalidPropertyMediaCandidate {
								code = "openapi.media_schema_mismatch"
							}
						}
						onUnrealizable(unrealizableTarget{
							selector:     buildJSONPointerSelector(path, method),
							operationKey: opKey,
							reasonCode:   code,
							rule:         rule,
							message:      reason + "; the required request body has no faithful candidate carriage",
						})
						continue
					}
					return iface, unrealizableOperation(opKey, reason)
				}
				if len(requestPlans) == 0 && warn != nil {
					code := "openapi.unresolvable_request_body"
					var dme *degenerateMediaError
					if errors.As(planErr, &dme) {
						code = "openapi.media_schema_mismatch"
					}
					reason := "no artifact-declared request media candidate can realize its flattened input"
					if planErr != nil {
						reason = planErr.Error()
					}
					warn(synthesize.SynthesizerWarning{Code: code, Message: reason + "; optional body omitted from the synthesized contract", Path: fmt.Sprintf("operations.%s.input", opKey)})
				}
			}
			if formatVersion == "3.1" {
				if dialectErr := validateProjectedOperationDialects(doc, op, params, requestPlans, bindingSpec); dialectErr != nil {
					reason := dialectErr.Error()
					if onUnrealizable != nil {
						onUnrealizable(unrealizableTarget{selector: buildJSONPointerSelector(path, method), operationKey: opKey, reasonCode: "openapi.unsupported_schema_dialect", rule: "OBI-D-06", message: reason})
						continue
					}
					return iface, unrealizableOperation(opKey, reason)
				}
			}

			obiOp := openbindings.Operation{
				Description: operationDescription(op),
				Deprecated:  op.Deprecated,
			}

			if len(op.Tags) > 0 {
				obiOp.Tags = op.Tags
			}

			opPointer := "#/operations/" + escapeJSONPointerSegment(opKey)
			routes := planAbstractInputRoutes(params, requestPlans)
			inputSchema := buildInputSchemaForPlans(inputOperation, params, requestPlans, &requestGraph, routes, schemaOverlays)
			if inputSchema != nil {
				// Project, then decycle — the TypeScript engine's order, and the
				// one the cut-point question is asked in: the decycler walks the
				// graph this direction actually emits. Projecting the root reads
				// annotations through the unprojected registry, because a
				// reference has not been inlined yet.
				projected := projectOpenAPISchemaWithRegistry(inputSchema, openAPIRequestSchema, requestSchemaProjectionExemptions(routes), refRegistry)
				inlined, hoisted := inlineRefsInOperationSchema(projected, requestGraph.registry, requestGraph.cyclic, opPointer+"/input", namer)
				restored := restoreBooleanSchemas(pruneUnreachableDefs(inlined, opPointer+"/input", hoisted))
				if object, ok := restored.(map[string]any); ok {
					obiOp.Input = translateSchemaDialect(object, formatVersion)
				} else {
					obiOp.Input = restored
				}
			}

			outputSchema := buildOutputSchemaWithCyclicRefs(op, schemaOverlays, bindingSpec, &responseGraph, doc.OpenAPI)
			if outputSchema != nil {
				projected := projectOpenAPISchemaWithRegistry(outputSchema, openAPIResponseSchema, nil, refRegistry)
				inlined, hoisted := inlineRefsInOperationSchema(projected, responseGraph.registry, responseGraph.cyclic, opPointer+"/output", namer)
				restored := restoreBooleanSchemas(pruneUnreachableDefs(inlined, opPointer+"/output", hoisted))
				if object, ok := restored.(map[string]any); ok {
					obiOp.Output = translateSchemaDialect(object, formatVersion)
				} else {
					obiOp.Output = restored
				}
			}

			iface.Operations[opKey] = obiOp

			selector := buildJSONPointerSelector(path, method)
			bindingKey := opKey + "." + DefaultSourceName
			binding := openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Selector:  selector,
			}
			if usesRoutedInput(bindingSpec) && routes.hasInput() {
				binding.InputTransform = &openbindings.TransformOrRef{Inline: routes.transformExpression(params)}
			}
			iface.Bindings[bindingKey] = binding
		}
	}

	return iface, nil
}

func restoreBooleanSchemas(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if literal, ok := structuralBooleanSchemaLiteral(typed); ok {
			return literal
		}
		for key, child := range typed {
			typed[key] = restoreBooleanSchemas(child)
		}
		return typed
	case []any:
		for index, child := range typed {
			typed[index] = restoreBooleanSchemas(child)
		}
		return typed
	default:
		return value
	}
}

// unrealizableOperation builds the error returned when an operation has no
// faithful carriage and the caller supplied no unrealizable sink. Callers that
// do supply one (the coverage path) instead record the position as an excluded
// coverage entry carrying its reason code, rule and artifact pointer, and
// synthesis continues past the operation. Without that sink there is nowhere to
// disclose the exclusion, so synthesis fails rather than return an interface
// whose operation set is silently narrower than the artifact declared.
func unrealizableOperation(operationKey, reason string) error {
	return fmt.Errorf("cannot synthesize OpenAPI operation %q: %s; synthesis would return a statically unbindable partial interface", operationKey, reason)
}

// httpMethods defines the iteration order for path item methods.
// Matches TS to ensure deterministic output across languages.
var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// loadDocument loads and discriminates an OpenAPI source per
// the registered OpenAPI binding family §3-§6: `content`, when present, is the artifact
// (content primacy), with a co-present `location` serving as the embedded
// artifact's BASE URI — relative $refs resolve against it exactly as they
// would had the document been retrieved from that address (OAPI-D-01/D-02,
// §6). Embedded content with no location has no base and must be
// self-contained: a relative external $ref then fails with a readable error
// (absolute http(s) $refs still resolve — they need no base). The artifact's
// own `openapi` field discriminates the accepted editions (OAPI-P-01).
//
// String content parses as YAML 1.2 (JSON being a valid subset); duplicate
// mapping keys are refused loudly by the YAML layer itself, satisfying the
// §3 duplicate-key pin.
func loadDocument(location string, content json.RawMessage) (*openapi3.T, error) {
	return loadDocumentWithResolver(context.Background(), http.DefaultClient, location, content)
}

func loadDocumentForBindingSpec(location string, content json.RawMessage, bindingSpec string) (*openapi3.T, error) {
	document, err := loadDocument(location, content)
	if err != nil {
		return nil, err
	}
	if err := checkAcceptedOpenAPIVersionForBindingSpec(document, bindingSpec); err != nil {
		return nil, err
	}
	return document, nil
}

// loadDocumentWithResolver loads a complete OpenAPI description using the
// supplied invocation/synthesis context and HTTP client for both the entry
// document and every reachable external reference. Resolver configuration is
// deliberately processor-private; it never changes the OBI document model.
func loadDocumentWithResolver(ctx context.Context, client *http.Client, location string, content json.RawMessage) (*openapi3.T, error) {
	return loadDocumentWithResolverInternal(ctx, client, location, content, nil)
}

func loadDocumentForSynthesis(ctx context.Context, client *http.Client, location string, content json.RawMessage) (*openapi3.T, *rawSchemaOverlayCollector, []byte, error) {
	overlays := newRawSchemaOverlayCollector()
	var entryBytes []byte
	doc, err := loadDocumentWithResolverEntry(ctx, client, location, content, overlays, &entryBytes)
	if err != nil {
		// A load failure does not preempt the whole-source refusal. Part 2's
		// refusal is a property of the ARTIFACT's raw image, decided by the
		// ladder over bytes this call has already captured, and it is the
		// document's own reason -- so a caller that hears the loader's
		// diagnostic instead hears a defect that is not what makes the source
		// unusable.
		//
		// Block 8h made this visible: it brought the confinement's remaining
		// authoring mechanisms onto the emission rail, and a confinement that
		// declines because it cannot gate itself now leaves the loader's error
		// standing where it used to leave a confined document that then
		// reached this refusal one line later. The refusal was riding on the
		// confinement pass succeeding, which was never what decided it.
		return nil, nil, entryBytes, err
	}
	overlays.bindDocument(doc)
	return doc, overlays, entryBytes, nil
}

func loadDocumentWithResolverInternal(ctx context.Context, client *http.Client, location string, content json.RawMessage, schemaOverlays *rawSchemaOverlayCollector) (*openapi3.T, error) {
	return loadDocumentWithResolverEntry(ctx, client, location, content, schemaOverlays, nil)
}

// loadDocumentWithResolverEntry additionally captures the ENTRY document's
// raw bytes (pre-normalization: the artifact's own image, which the
// acceptance floor classifies against) when entryBytes is non-nil. In the
// location lanes the entry is the loader's first resource read.
func loadDocumentWithResolverEntry(ctx context.Context, client *http.Client, location string, content json.RawMessage, schemaOverlays *rawSchemaOverlayCollector, entryBytes *[]byte) (*openapi3.T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = http.DefaultClient
	}

	// The artifact's own entry image, captured once by the first attempt and
	// never overwritten: it is what the acceptance floor classifies against
	// and what block 8d-2's confinement pass reads.
	var captured []byte

	// attempt runs one complete shipped load. `entryOverride`, when non-nil,
	// replaces the ENTRY document's bytes at the seam block 8a proved --
	// before normalizeResource, in every lane -- so a confined retry keeps
	// each lane's own base-URI and retrieval semantics.
	//
	// `overlays` is the collector this load fills. It is a PARAMETER rather
	// than the enclosing one because the emission gate loads its own images
	// and must not write its lane state over the state that describes the
	// document this call returns: one collector cannot describe two documents
	// at once, and building the gate's images with no collector at all is
	// exactly the narrowing record 112 §5.8 refused.
	attempt := func(entryOverride []byte, overlays *rawSchemaOverlayCollector) (*openapi3.T, error) {
		loader := openapi3.NewLoader()
		loader.Context = ctx
		loader.IsExternalRefsAllowed = true
		retrievalURIs := map[string]*url.URL{}
		var retrievalMu sync.RWMutex
		loader.JoinFunc = artifactJoinFunc(retrievalURIs, &retrievalMu)
		normalizer := newRawRefSiblingNormalizer(loader.JoinFunc)
		normalizer.schemaOverlays = overlays
		readArtifact := artifactReadFunc(client, content != nil && location == "", retrievalURIs, &retrievalMu)
		composition := newExternalComposition(
			func(resource *url.URL) ([]byte, error) { return readArtifact(loader, resource) },
			loader.JoinFunc,
		)
		entrySeen := false
		loader.ReadFromURIFunc = func(loader *openapi3.Loader, resource *url.URL) ([]byte, error) {
			data, err := readArtifact(loader, resource)
			if err != nil {
				return nil, err
			}
			// The entry document is the loader's first resource read ONLY in
			// the location lanes. When content is co-present the entry never
			// passes through here, and this call is an EXTERNAL resource --
			// substituting the entry into it is exactly the confusion that
			// dropped an externally referenced Path Item member.
			if content == nil && !entrySeen {
				entrySeen = true
				if captured == nil {
					captured = append([]byte(nil), data...)
				}
				if entryOverride != nil {
					data = append([]byte(nil), entryOverride...)
				}
			}
			retrieval := artifactRetrievalURI(resource, retrievalURIs, &retrievalMu)
			normalizer.rememberImplicitSecuritySchemes(data, retrieval)
			data = composition.prune(resource, data)
			// A reference the edition's own text makes unresolvable is reported
			// here, at the seam that already serves every resource, rather than
			// left for the typed loader to fail on some later symptom.
			if err := composition.refusal(); err != nil {
				return nil, err
			}
			return normalizer.normalizeResourceAt(data, resource, retrieval)
		}

		doc, err := loadDocumentRaw(loader, normalizer, composition, location, content, &captured, entryOverride)
		if err != nil {
			return nil, err
		}
		localizeReferenceMetadata(doc)
		if err := checkAcceptedOpenAPIVersion(doc); err != nil {
			return nil, err
		}
		return doc, nil
	}

	doc, err := attempt(nil, schemaOverlays)
	if entryBytes != nil {
		*entryBytes = captured
	}
	if err == nil {
		return doc, nil
	}

	// Fast path first: confinement is reached only after the shipped load has
	// already refused. On any confinement failure the ORIGINAL error stands.
	confined, confinedErr, took := confineEntryDocument(captured, func(data []byte) (*openapi3.T, error) {
		schemaOverlays.reset()
		return attempt(data, schemaOverlays)
	}, err, synthesisEmissionGate(ctx, location, attempt))
	switch {
	case !took:
		return nil, err
	case confinedErr != nil:
		return nil, confinedErr
	default:
		return confined, nil
	}
}

// synthesisEmissionGate is this engine's answer to the confinement pass's
// emission question (see `confinementEmissionGate`). The content this engine
// emits from a document is the interface its own shipped synthesis derives, so
// the gate runs that synthesis over both loads and compares the emitted bytes.
// Nothing here knows which OAS channels the synthesis reads; that is the point.
//
// Two deliberate choices:
//
//   - The TOLERANT surface is used (a non-nil `onUnrealizable`), because it is
//     the surface that emits the most: a ladder-invalid target is skipped
//     rather than refused, so every operation this document can put into
//     shipped content is present in the comparison.
//   - Both the family's binding specification and the empty one are compared,
//     so a projection that only one of them emits cannot hide a mark.
//
// THE GATE RUNS THE SHIPPING EMISSION FUNCTION, and that is not a restatement
// of the two choices above -- it is the correction record 112 §6 ground 1
// required. This gate used to be handed two already-loaded documents and to
// convert both with `schemaOverlays` NIL, defended by the argument that
// overlays only ADD members to a schema the emitter already carries, so their
// absence could hide no mark. THAT ARGUMENT IS FALSE, and the reason it is
// false is not the argument it answers.
//
// The gate's two loads came from `reload`, which is
// `schemaOverlays.reset(); attempt(data)`. That collects the authorial presence
// facts kin-openapi's typed model erases -- `erasedSchemaPresence` names them
// -- and then NEVER BINDS THEM. `bindDocument` is what pairs a collected
// overlay with typed identity, and only `loadDocumentForSynthesis` called it.
// So the gate's images were emitted from documents whose erased spellings had
// been collected and dropped, and eleven measured facts -- `title`,
// `description`, `format`, `pattern`, `default`, `example`, `enum`,
// `minLength`, `uniqueItems`, `readOnly`, `deprecated`, each in the erased
// spelling `erasedSchemaPresence` recognises -- were INVISIBLE to the gate and
// VISIBLE to what ships. A difference at an authored position spelled in any of
// them would have been certified unreachable and would have shipped.
// `TestSynthesisEmissionGate_TheSupersededEmitterWasBlindToTheOverlay` is that
// measurement, and it attributes the blindness to the missing BIND: once a
// document is bound, the eleven reach emitted content whether or not the
// collector is also handed to the conversion.
//
// So each side is now built by exactly what `loadDocumentForSynthesis` and
// `invoker.go` build the shipped interface by, in that order:
//
//	overlays := newRawSchemaOverlayCollector()   -- its OWN, per image
//	doc      := attempt(image, overlays)
//	overlays.bindDocument(doc)                   -- `newIsolatedEmissionLoad`
//	overlays.setExternalComponents(internalizeExternalRefs(ctx, doc))
//	convertDocToInterfaceWithOverlay(doc, location, bindingSpec, …, overlays, floor)
//
// That is why the gate takes BYTES: the load has to happen inside it, because
// the document and the lane state that load collected are ONE input and no
// caller can hand over two of those pairs from one collector.
// `internalizeExternalRefs` MUTATES the document it is given, which is a second
// reason each side must be this gate's own isolated load and never the document
// the pass proposes to return.
//
// The shipped image is the same bytes in every round, so its images are
// computed ONCE and reused, and the pass's total entry-document loads are
// unchanged by the move.
// newIsolatedEmissionLoad is the emission gate's own load: one complete shipped
// load into its OWN overlay collector, BOUND, and never the collector whose
// state has to describe the document the surrounding call returns.
//
// It is a named function and not an inline closure for one reason: it is the
// half of the shipping emission function that the gate was missing, so a proof
// that the gate runs the shipping emitter has to be able to drive THIS, and a
// perturbation that removes the bind has to be able to redden it. An inline
// closure the shipped path alone can see is exactly the shape of proof this
// file has had defeated twice.
func newIsolatedEmissionLoad(attempt func([]byte, *rawSchemaOverlayCollector) (*openapi3.T, error)) func([]byte) (*openapi3.T, *rawSchemaOverlayCollector, error) {
	return func(data []byte) (*openapi3.T, *rawSchemaOverlayCollector, error) {
		overlays := newRawSchemaOverlayCollector()
		image, err := attempt(data, overlays)
		if err != nil {
			return nil, nil, err
		}
		overlays.bindDocument(image)
		return image, overlays, nil
	}
}

// It takes `attempt` -- one shipped load -- rather than an already-isolated
// loader, so that no caller can hand it a load that shares a collector with
// anything else. The isolation is the gate's own and there is no wiring line
// between them for a later change to get wrong.
func synthesisEmissionGate(ctx context.Context, location string,
	attempt func([]byte, *rawSchemaOverlayCollector) (*openapi3.T, error)) confinementEmissionGate {
	isolate := newIsolatedEmissionLoad(attempt)

	// emit returns this engine's shipped emission for one image, and whether
	// that image LOADED at all. `(nil, true)` is "loaded, and this engine
	// cannot emit from it", which is a conclusive failure; `(nil, false)` is
	// "did not load", which is evidence in neither direction.
	emit := func(data []byte, floor *acceptanceFloor) ([][]byte, bool) {
		doc, overlays, err := isolate(data)
		if err != nil {
			return nil, false
		}
		overlays.setExternalComponents(internalizeExternalRefs(ctx, doc))
		images := make([][]byte, 0, 2)
		for _, bindingSpec := range []string{bindingSpecForOpenAPIEdition(doc.OpenAPI), ""} {
			image, imageErr := emittedImage(doc, location, bindingSpec, overlays, floor)
			if imageErr != nil {
				return nil, true
			}
			images = append(images, image)
		}
		return images, true
	}

	var shippedData []byte
	var shippedImages [][]byte
	return func(shipped, marked []byte, floor *acceptanceFloor) (bool, bool) {
		if shippedImages == nil || !bytes.Equal(shippedData, shipped) {
			images, loaded := emit(shipped, floor)
			if !loaded || images == nil {
				// The shipped image is what the pass proposes to hand back. A
				// proposal this engine cannot load, or cannot emit from, is a
				// failure to make the showing at all -- conclusive, and not
				// identical.
				return false, true
			}
			shippedData = append([]byte(nil), shipped...)
			shippedImages = images
		}
		markedImages, loaded := emit(marked, floor)
		if !loaded {
			return false, false
		}
		if len(markedImages) != len(shippedImages) {
			return false, true
		}
		for i := range shippedImages {
			if !bytes.Equal(shippedImages[i], markedImages[i]) {
				return false, true
			}
		}
		return true, true
	}
}

func emittedImage(doc *openapi3.T, location, bindingSpec string,
	overlays *rawSchemaOverlayCollector, floor *acceptanceFloor) ([]byte, error) {
	iface, err := convertDocToInterfaceWithOverlay(doc, location, bindingSpec, nil, func(unrealizableTarget) {}, overlays, floor)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(iface)
	if err != nil {
		return nil, err
	}
	return withoutInternalJoinKeys(data)
}

// withoutInternalJoinKeys removes `schemaOverlayMarker` from an emitted image
// before the gate compares two of them.
//
// The marker is a PRIVATE JOIN KEY between one raw Schema Object and the typed
// schema produced from it, and `schema_overlay.go` states that it exists only
// inside the loader's normalized bytes. `takeMarker` consumes it during
// `bindDocument`, but only at the schema entrances that traversal reaches, and
// a schema it misses carries the marker into the marshaled form. Its VALUE is a
// per-load ordinal, so two loads of the same document can carry different
// values at the same position.
//
// That is fatal to a gate whose entire question is byte-identity: block 8h
// measured three corpus specimens -- `lancedb`, `rswag`,
// `stoatchat/javascript-client-api` -- whose two images differed at NOTHING BUT
// these ordinals, and the gate refused a confinement for a reason that has
// nothing to do with whether anything authored reaches emitted content. The
// defect is latent at the gate's landed head and only fires on documents that
// record overlays, which no document reaching the URef round happened to do.
//
// Removed HERE and not in `schemaRefToMap`, deliberately: the marker can sit at
// any depth inside a marshaled schema, so a single top-level delete would be a
// partial guarantee wearing a total one's clothes, and paying for a recursive
// walk on the shipped emission path would be paying it on every synthesis to
// serve a comparison only this gate makes. That a marker can survive
// `bindDocument` at all is a separate defect and is filed, not fixed here.
func withoutInternalJoinKeys(data []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	stripped := stripJoinKeys(value)
	return json.Marshal(stripped)
}

func stripJoinKeys(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, schemaOverlayMarker)
		for key, child := range typed {
			typed[key] = stripJoinKeys(child)
		}
		return typed
	case []any:
		for i, child := range typed {
			typed[i] = stripJoinKeys(child)
		}
		return typed
	}
	return value
}

// absolutizeArtifactLocation lifts a bare filesystem path to the file://
// document address the strict loader accepts — authoring-time operator
// convenience at the SYNTHESIS entries only, the usage family's posture
// (one loader for every lane, no bare-path lane). The invoke/binding lanes
// never absolutize: a document's own bare-path location is a relative
// reference in form and is refused (OAPI-D-02). Synthesis emits this
// normalized address so the returned source remains invocable.
func absolutizeArtifactLocation(location string) (string, error) {
	if location == "" || strings.Contains(location, "://") {
		return location, nil
	}
	abs := location
	if !filepath.IsAbs(location) {
		var err error
		abs, err = filepath.Abs(location)
		if err != nil {
			return "", fmt.Errorf("resolve OpenAPI artifact path: %w", err)
		}
	}
	return "file://" + abs, nil
}

// validateDocumentAddress checks OAPI-D-02's location grammar offline,
// without dereferencing: `location`, when present, is an absolute URI
// addressing the OpenAPI document itself. A bare filesystem path is a
// relative reference in form (core OBI-D-05) and is refused — a local
// artifact is addressed as file:// or embedded as the source's content.
func validateDocumentAddress(location string) error {
	u, err := url.Parse(location)
	if err != nil || u.Scheme == "" || u.Opaque != "" {
		return fmt.Errorf("openapi location %q is not an absolute URI addressing the document (OAPI-D-02): a local artifact is addressed as file:// or embedded as the source's content", location)
	}
	return nil
}

func loadDocumentRaw(loader *openapi3.Loader, normalizer *rawRefSiblingNormalizer, composition *externalComposition, location string, content json.RawMessage, entryBytes *[]byte, entryOverride []byte) (*openapi3.T, error) {
	// `location`, when present, must be an absolute URI (OAPI-D-02) —
	// whether it is the fetch target or only the embedded content's base.
	// The former bare-path lenience ("for local tooling") is gone: the
	// usage family's posture, applied here.
	if location != "" {
		if err := validateDocumentAddress(location); err != nil {
			return nil, err
		}
	}

	if content != nil {
		data, err := openbindings.ContentToBytes(content)
		if err != nil {
			return nil, err
		}
		if entryBytes != nil && *entryBytes == nil {
			// The artifact's own image, before ref-sibling normalization: what
			// the acceptance floor classifies against.
			*entryBytes = append([]byte(nil), data...)
		}
		if entryOverride != nil {
			// A confined retry (block 8d-2) substitutes the entry document at
			// the same seam the normalizer already occupies.
			data = append([]byte(nil), entryOverride...)
		}
		var resource *url.URL
		if location != "" {
			resource, _ = url.Parse(location)
		}
		data, err = normalizer.normalizeResource(data, resource)
		if err != nil {
			return nil, err
		}
		composition.setEntry(resource, data)
		// Embedded content never passes through ReadFromURIFunc, so the entry's
		// own tree is read here instead. It is the same call the location lanes
		// make from `prune`, and it retrieves nothing.
		composition.scanEntry(data)
		if err := composition.refusal(); err != nil {
			return nil, err
		}
		if location != "" {
			if resource != nil {
				return loader.LoadFromDataWithPath(data, resource)
			}
		}
		// No co-present location: no base URI. Absolute http(s) references
		// still resolve; anything else (a relative $ref in particular) has
		// nothing to resolve against and refuses with a readable error —
		// never a silent working-directory file read.
		return loader.LoadFromData(data)
	}

	if location == "" {
		return nil, fmt.Errorf("source must have location or content")
	}

	if openbindings.IsHTTPURL(location) {
		loc, err := url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("invalid URL %q: %w", location, err)
		}
		composition.setEntry(loc, nil)
		return loader.LoadFromURI(loc)
	}

	// A file:// location (the conformant absolute-URI spelling, OAPI-D-02)
	// loads from its path.
	if strings.HasPrefix(location, "file://") {
		loc, err := url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("invalid URL %q: %w", location, err)
		}
		composition.setEntry(&url.URL{Path: filepath.ToSlash(loc.Path)}, nil)
		return loader.LoadFromFile(loc.Path)
	}
	u, _ := url.Parse(location) // validated above: absolute, non-opaque
	return nil, fmt.Errorf("openapi location scheme %q is not dereferenced by this processor (supported: file, http, https)", u.Scheme)
}

// artifactReadFunc reads the entry document and its complete reference closure
// through one client, caches successful retrievals for the load, and records a
// redirect's final retrieval URI for subsequent relative-reference joins. In
// the content-only lane it permits absolute HTTP(S) targets (which need no
// base) and refuses relative/file targets: with no co-present location the
// embedded artifact has no base URI (§6 — bundle before embedding).
func artifactReadFunc(client *http.Client, selfContained bool, retrievalURIs map[string]*url.URL, retrievalMu *sync.RWMutex) openapi3.ReadFromURIFunc {
	cache := map[string][]byte{}
	return func(loader *openapi3.Loader, u *url.URL) ([]byte, error) {
		key := u.String()
		if data, ok := cache[key]; ok {
			return append([]byte(nil), data...), nil
		}

		var data []byte
		var err error
		switch u.Scheme {
		case "http", "https":
			if !u.IsAbs() || u.Host == "" {
				return nil, fmt.Errorf("reference %q is not an absolute HTTP URI", u)
			}
			ctx := loader.Context
			if ctx == nil {
				ctx = context.Background()
			}
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
			if requestErr != nil {
				return nil, requestErr
			}
			resp, requestErr := client.Do(req)
			if requestErr != nil {
				return nil, requestErr
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
				return nil, fmt.Errorf("error loading %q: request returned status code %d", u.String(), resp.StatusCode)
			}
			finalURI := u
			if resp.Request != nil && resp.Request.URL != nil {
				finalURI = resp.Request.URL
			}
			retrievalMu.Lock()
			retrievalURIs[artifactResourceKey(u)] = cloneURL(finalURI)
			retrievalMu.Unlock()
			data, err = io.ReadAll(resp.Body)
		case "", "file":
			if selfContained {
				return nil, fmt.Errorf("reference %q cannot resolve: embedded content with no co-present location has no base URI and must be self-contained (bundle the document before embedding, or set the source's location)", u)
			}
			data, err = openapi3.ReadFromFile(loader, u)
		default:
			return nil, fmt.Errorf("openapi reference scheme %q is not dereferenced by this processor (supported: file, http, https)", u.Scheme)
		}
		if err != nil {
			return nil, err
		}
		cache[key] = append([]byte(nil), data...)
		return data, nil
	}
}

func artifactJoinFunc(retrievalURIs map[string]*url.URL, retrievalMu *sync.RWMutex) func(*url.URL, *url.URL) *url.URL {
	return func(base, relative *url.URL) *url.URL {
		if base == nil {
			return relative
		}
		retrievalMu.RLock()
		resolvedBase := retrievalURIs[artifactResourceKey(base)]
		retrievalMu.RUnlock()
		if resolvedBase == nil {
			resolvedBase = base
		}
		return resolvedBase.ResolveReference(relative)
	}
}

func artifactRetrievalURI(resource *url.URL, retrievalURIs map[string]*url.URL, retrievalMu *sync.RWMutex) *url.URL {
	if resource == nil {
		return nil
	}
	retrievalMu.RLock()
	resolved := cloneURL(retrievalURIs[artifactResourceKey(resource)])
	retrievalMu.RUnlock()
	if resolved != nil {
		return resolved
	}
	return resource
}

func artifactResourceKey(u *url.URL) string {
	if u == nil {
		return ""
	}
	copy := *u
	copy.Fragment = ""
	return copy.String()
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	copy := *u
	copy.Fragment = ""
	return &copy
}

// checkAcceptedOpenAPIVersion applies the union load gate used by the shared
// 3.0/3.1 synthesis loader. The 3.2 request lane classifies through the
// client-owned Artifact loader before reference resolution instead.
func checkAcceptedOpenAPIVersion(doc *openapi3.T) error {
	v := doc.OpenAPI
	if v == "" {
		return fmt.Errorf("document declares no `openapi` field: the registered OpenAPI 3.0/3.1 engines require an exact accepted edition (OAPI30-P-01/OAPI31-P-01; Swagger 2.0 is not accepted)")
	}
	switch v {
	case "3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4",
		"3.1.0", "3.1.1", "3.1.2":
		return nil
	}
	return fmt.Errorf("unsupported OpenAPI version %q: the registered OpenAPI 3.0/3.1 engines accept exactly 3.0.0–3.0.4 or 3.1.0–3.1.2 under their own tokens", v)
}

func bindingSpecForOpenAPIEdition(edition string) string {
	switch {
	case openAPIBindingSpecRegistry[BindingSpecOpenAPI30].editions[edition]:
		return BindingSpecOpenAPI30
	case openAPIBindingSpecRegistry[BindingSpecOpenAPI31].editions[edition]:
		return BindingSpecOpenAPI31
	case openAPIBindingSpecRegistry[BindingSpecOpenAPI32].editions[edition]:
		return BindingSpecOpenAPI32
	default:
		return ""
	}
}

func checkAcceptedOpenAPIVersionForBindingSpec(doc *openapi3.T, bindingSpec string) error {
	registration, registered := openAPIBindingSpecRegistry[bindingSpec]
	if !registered || !registration.requestImplemented {
		return fmt.Errorf("%s: binding specification %q is not implemented", ErrCodeUnsupportedBindingSpec, bindingSpec)
	}
	edition := ""
	if doc != nil {
		edition = doc.OpenAPI
	}
	if registration.editions[edition] {
		return nil
	}
	return fmt.Errorf("document edition %q is not admitted by binding specification %q", edition, bindingSpec)
}

func deriveOperationKey(op *openapi3.Operation, path, method string, used map[string]bool) string {
	if op.OperationID != "" {
		key := synthesize.SanitizeKey(op.OperationID)
		if !used[key] {
			return key
		}
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")
	var parts []string
	for _, seg := range segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			continue
		}
		if seg != "" {
			parts = append(parts, seg)
		}
	}

	key := strings.Join(parts, ".") + "." + strings.ToLower(method)
	key = synthesize.SanitizeKey(key)
	return synthesize.UniqueKey(key, used)
}

func operationDescription(op *openapi3.Operation) string {
	if op.Description != "" {
		return op.Description
	}
	return op.Summary
}

func buildJSONPointerSelector(path, method string) string {
	escaped := strings.ReplaceAll(path, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return "#/paths/" + escaped + "/" + strings.ToLower(method)
}

func buildInputSchemaForPlans(op *openapi3.Operation, allParams openapi3.Parameters, requestPlans []*bodyPlan, graph *directionGraph, routes abstractInputRoutes, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return buildInputSchema(op, allParams, nil, graph, routes, schemaOverlays)
	}
	var variants []map[string]any
	if !op.RequestBody.Value.Required {
		if parameterOnly := buildInputSchema(op, allParams, nil, graph, routes, schemaOverlays); parameterOnly != nil {
			variants = append(variants, parameterOnly)
		}
	}
	for _, plan := range requestPlans {
		if schema := buildInputSchema(op, allParams, plan, graph, routes, schemaOverlays); schema != nil {
			variants = append(variants, schema)
		}
	}
	seen := map[string]bool{}
	unique := make([]map[string]any, 0, len(variants))
	for _, schema := range variants {
		encoded, _ := json.Marshal(schema)
		key := string(encoded)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, schema)
		}
	}
	if len(unique) == 0 {
		return nil
	}
	if len(unique) == 1 {
		return unique[0]
	}
	anyOf := make([]any, len(unique))
	for i, schema := range unique {
		anyOf[i] = schema
	}
	return map[string]any{"anyOf": anyOf}
}

func buildInputSchema(op *openapi3.Operation, allParams openapi3.Parameters, requestPlan *bodyPlan, graph *directionGraph, routes abstractInputRoutes, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	properties := map[string]any{}
	var required []string
	// Only JSON-family object candidates can carry undeclared fields. The
	// parameter-only, multipart/form, and scalar-body surfaces stay closed.
	hasOpenBody := planAllowsObjectPassthrough(requestPlan)

	for _, paramRef := range allParams {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		param := paramRef.Value

		prop := paramToSchema(param, graph, schemaOverlays)
		field := routes.parameterField(param.In, param.Name)
		if prop != nil {
			properties[field] = prop
		}

		if param.Required {
			required = append(required, field)
		}
	}

	if op.RequestBody != nil && op.RequestBody.Value != nil && requestPlan != nil {
		rb := op.RequestBody.Value
		var bodySchema map[string]any
		// A schema that asserts nothing is the same declaration as an omitted
		// one (§9.2), so the byte lane it selects synthesizes the canonical
		// boundary schema rather than decorating an empty declaration.
		assertionFreeByteLane := requestPlan.rawBoundary && requestPlan.media != nil &&
			requestPlan.media.Schema != nil && schemaAssertsNothing(requestPlan.media.Schema.Value)
		if requestPlan.media != nil && requestPlan.media.Schema != nil && !assertionFreeByteLane {
			bodySchema = graph.declaredForm(requestPlan.media.Schema, schemaOverlays)
		} else if requestPlan.media != nil && requestPlan.media.ItemSchema != nil &&
			(requestPlan.family == familySequential || requestPlan.family == familyMultipart ||
				requestPlan.mediaRange && requestPlan.bindingSpec == BindingSpecOpenAPI32) {
			bodySchema = map[string]any{
				"type":  "array",
				"items": graph.declaredForm(requestPlan.media.ItemSchema, schemaOverlays),
			}
		} else if requestPlan.rawBoundary {
			bodySchema = map[string]any{"type": "string", "contentEncoding": "base64"}
		} else if hasMediaFidelity(requestPlan.bindingSpec) && requestPlan.synthetic {
			// A schema-omitted revision-3 JSON/range declaration allows any
			// application JSON value. Preserve that unconstrained whole-body
			// value under the protocol-neutral body field.
			bodySchema = map[string]any{}
		}
		if bodySchema != nil {
			// Resolve a $ref body BEFORE the flatten decision: bodies
			// declared by reference are the production norm, and wrapping
			// the unresolved {"$ref"} in a phantom "body" property emits a
			// contract the invoker then sends literally onto the wire.
			if _, isRef := bodySchema["$ref"]; isRef && !graph.hoists(requestPlan.media.Schema) {
				// nil decycle context: this expansion only informs the
				// flatten decision; the embedded schema is decycled later by
				// inlineRefsInOperationSchema.
				if resolved, ok := inlineRefs(bodySchema, graph.registryOrNil(), map[string]bool{}, nil).(map[string]any); ok {
					bodySchema = resolved
				}
			}
			if requestPlan.rawBoundary {
				// This is a caller-boundary annotation, not an application-schema
				// replacement: retain every authored keyword and add only the
				// Base64 spelling required to carry raw bytes through JSON values.
				bodySchema["contentEncoding"] = "base64"
			}
			var bodyObject bool
			var bodyProps map[string]any
			var bodyRequired map[string]bool
			if requestPlan.media != nil && requestPlan.media.Schema != nil {
				bodyObject, bodyProps, bodyRequired = resolvedSynthesisBodyShape(requestPlan.media.Schema.Value, map[*openapi3.Schema]bool{}, graph, schemaOverlays)
			} else {
				bodyProps = map[string]any{}
				bodyRequired = map[string]bool{}
			}
			hasProps := len(bodyProps) > 0
			if hasMediaFidelity(requestPlan.bindingSpec) && requestPlan.oas30 && requestPlan.family == familyMultipart {
				for _, property := range bodyProps {
					if propertySchema, ok := property.(map[string]any); ok {
						decorateMultipartPartBinaryBoundary(propertySchema)
					}
				}
			}
			switch {
			case !bodyObject || requestPlan.wholeObject:
				// A non-object body, an explicitly dynamic object, or a
				// declaration-complex JSON body rides as one
				// protocol-independent application value. Non-object
				// schemas include array, scalar, binary, or
				// TYPELESS (neither `properties` nor an explicit object
				// type; §9.1's determination is declaration-only): the
				// flattened contract carries it under the synthetic
				// `body` property, unwrapped at the wire.
				field := routes.wholeBodyField
				if field == "" {
					field = syntheticBodyProperty
				}
				properties[field] = bodySchema
				if rb.Required {
					required = append(required, field)
				}
			case hasProps:
				for k, v := range bodyProps {
					properties[routes.bodyField(k)] = v
				}
				for name := range bodyRequired {
					required = append(required, routes.bodyField(name))
				}
			default:
				// A free-form object body (type object, no named
				// properties): the flattened model passes unmatched input
				// fields through into the body (the registered OpenAPI binding family
				// §9.1), so the flattened surface stays an OPEN object —
				// the synthetic `body` wrap is reserved for NON-object
				// body schemas, and wrapping here would describe a field
				// the conformant invoker refuses as unmatched.
				// hasOpenBody was determined by the selected candidate's family.
			}
		}
	}

	if len(properties) == 0 {
		if hasOpenBody {
			return map[string]any{"type": "object"}
		}
		if requestPlan != nil && op.RequestBody != nil && op.RequestBody.Value != nil && op.RequestBody.Value.Required {
			return map[string]any{"type": "object", "additionalProperties": false}
		}
		return nil
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if !hasOpenBody {
		schema["additionalProperties"] = false
	}
	if len(required) > 0 {
		sort.Strings(required)
		requiredValues := make([]any, len(required))
		for i, name := range required {
			requiredValues[i] = name
		}
		schema["required"] = requiredValues
	}
	return schema
}

func decorateMultipartPartBinaryBoundary(schema map[string]any) {
	if schema == nil {
		return
	}
	if projectedSchemaTypeIs(schema, "string") && projectedSchemaFormatIs(schema, "binary") {
		schema["contentEncoding"] = "base64"
		return
	}
	if !projectedSchemaTypeIs(schema, "array") {
		return
	}
	for _, candidate := range append([]map[string]any{schema}, projectedSchemaList(schema["allOf"])...) {
		items, _ := candidate["items"].(map[string]any)
		if items != nil && projectedSchemaTypeIs(items, "string") && projectedSchemaFormatIs(items, "binary") {
			items["contentEncoding"] = "base64"
		}
	}
}

func projectedSchemaTypeIs(schema map[string]any, want string) bool {
	if value, ok := schema["type"].(string); ok && value == want {
		return true
	}
	for _, child := range projectedSchemaList(schema["allOf"]) {
		if projectedSchemaTypeIs(child, want) {
			return true
		}
	}
	return false
}

func projectedSchemaFormatIs(schema map[string]any, want string) bool {
	if value, ok := schema["format"].(string); ok && value == want {
		return true
	}
	for _, child := range projectedSchemaList(schema["allOf"]) {
		if projectedSchemaFormatIs(child, want) {
			return true
		}
	}
	return false
}

func projectedSchemaList(value any) []map[string]any {
	values, _ := value.([]any)
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if schema, ok := value.(map[string]any); ok {
			out = append(out, schema)
		}
	}
	return out
}

// resolvedSynthesisBodyShape resolves the declaration-only object surface
// used by OAPI-P-03 synthesis. allOf contributes its recursive property and
// required-name union; wrapping the allOf node as a synthetic whole body
// would publish a contract that the invoker (correctly) routes as object
// properties.
func resolvedSynthesisBodyShape(schema *openapi3.Schema, seen map[*openapi3.Schema]bool, graph *directionGraph, schemaOverlays *rawSchemaOverlayCollector) (bool, map[string]any, map[string]bool) {
	properties := map[string]any{}
	required := map[string]bool{}
	if schema == nil || seen[schema] {
		return false, properties, required
	}
	seen[schema] = true
	defer delete(seen, schema)

	object := schema.Type.Is("object") || schema.Properties != nil
	for name, property := range schema.Properties {
		properties[name] = graph.declaredForm(property, schemaOverlays)
	}
	for _, name := range schema.Required {
		required[name] = true
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		memberObject, memberProperties, memberRequired := resolvedSynthesisBodyShape(member.Value, seen, graph, schemaOverlays)
		object = object || memberObject
		for name, property := range memberProperties {
			if existing, present := properties[name]; present {
				properties[name] = map[string]any{"allOf": []any{existing, property}}
			} else {
				properties[name] = property
			}
		}
		for name := range memberRequired {
			required[name] = true
		}
	}
	return object, properties, required
}

func mergeParameters(pathParams, opParams openapi3.Parameters) openapi3.Parameters {
	if len(pathParams) == 0 {
		return opParams
	}
	if len(opParams) == 0 {
		return pathParams
	}

	overridden := map[string]bool{}
	for _, p := range opParams {
		if p != nil && p.Value != nil {
			overridden[p.Value.In+":"+p.Value.Name] = true
		}
	}

	var merged openapi3.Parameters
	for _, p := range pathParams {
		if p != nil && p.Value != nil {
			if !overridden[p.Value.In+":"+p.Value.Name] {
				merged = append(merged, p)
			}
		}
	}
	merged = append(merged, opParams...)
	return merged
}

func paramToSchema(param *openapi3.Parameter, graph *directionGraph, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if param.Schema != nil && param.Schema.Value != nil {
		// A Parameter Object description belongs to the PARAMETER, and merging it
		// produces a schema that is no longer the referenced component — so the
		// merged schema is not that cut point, and the component still cuts
		// wherever it is referenced inside. Both engines read it that way; the
		// TypeScript twin's merge allocates a new node for the same reason
		// (packages/openapi/src/synthesize.ts, paramToSchema).
		schema := graph.parameterForm(param.Schema, param.Description != "", schemaOverlays)
		if param.Description != "" {
			if schema == nil {
				schema = map[string]any{}
			}
			schema = schemaWithParameterDescription(schema, param.Description)
		}
		return schema
	}
	if len(param.Content) > 0 {
		keys := make([]string, 0, len(param.Content))
		for key := range param.Content {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if media := param.Content[keys[0]]; media != nil && media.Schema != nil {
			schema := graph.parameterForm(media.Schema, param.Description != "", schemaOverlays)
			if param.Description != "" {
				schema = schemaWithParameterDescription(schema, param.Description)
			}
			return schema
		}
	}

	prop := map[string]any{"type": "string"}
	if param.Description != "" {
		prop["description"] = param.Description
	}
	return prop
}

func schemaWithParameterDescription(schema map[string]any, description string) map[string]any {
	if _, boolean := structuralBooleanSchemaLiteral(schema); boolean {
		// A JSON Schema boolean cannot carry siblings. Preserve both authorial
		// facts by wrapping the literal in allOf, matching the TypeScript SDK's
		// reversible projection.
		return map[string]any{"allOf": []any{schema}, "description": description}
	}
	schema["description"] = description
	return schema
}

// unsupportedParameterContentFor returns the first content-form parameter
// whose single media declaration the named binding family cannot serialize.
// Creation-time soundness requires excluding the target rather than emitting
// an operation that is statically guaranteed to refuse when that parameter is
// populated.
func unsupportedParameterContentFor(params openapi3.Parameters, bindingSpec string) string {
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		param := ref.Value
		if hasMediaFidelity(bindingSpec) && bindingSpec != BindingSpecOpenAPI32 {
			if err := validateRevision3ParameterSerialization(param, bindingSpec == BindingSpecOpenAPI30); err != nil {
				return param.Name
			}
		}
		if len(param.Content) == 0 {
			continue
		}
		if len(param.Content) != 1 {
			return param.Name
		}
		for mediaKey := range param.Content {
			var parsed parsedMediaType
			var err error
			if hasMediaFidelity(bindingSpec) {
				parsed, err = parseRevision3MediaType(mediaKey)
			} else {
				parsed, err = parseMediaType(mediaKey)
			}
			queryStringForm := bindingSpec == BindingSpecOpenAPI32 &&
				param.In == openapiclient.ParameterInQueryString &&
				parsed.base == "application/x-www-form-urlencoded"
			if err != nil || (!queryStringForm && !isJSONMediaType(parsed.base) && parsed.base != "text/plain") {
				return param.Name
			}
			if hasMediaFidelity(bindingSpec) && parsed.base == "text/plain" && supportedTextCharset(parsed) != nil {
				return param.Name
			}
		}
	}
	return ""
}

func buildOutputSchema(op *openapi3.Operation, schemaOverlays *rawSchemaOverlayCollector, bindingSpec string, openapiVersions ...string) map[string]any {
	return buildOutputSchemaWithCyclicRefs(op, schemaOverlays, bindingSpec, nil, openapiVersions...)
}

func buildOutputSchemaWithCyclicRefs(op *openapi3.Operation, schemaOverlays *rawSchemaOverlayCollector, bindingSpec string, graph *directionGraph, openapiVersions ...string) map[string]any {
	openapiVersion := "3.0"
	if len(openapiVersions) > 0 {
		openapiVersion = openapiVersions[0]
	}
	if op.Responses == nil {
		return nil
	}

	responses := op.Responses.Map()
	keys := make([]string, 0, len(responses))
	hasRange := false
	exactSuccesses := 0
	for key := range responses {
		keys = append(keys, key)
		if key == "2XX" {
			hasRange = true
		}
		if len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9' {
			exactSuccesses++
		}
	}
	sort.Strings(keys)
	var schemas []map[string]any
	seen := map[string]bool{}
	cyclicRootRefs := map[string]string{}
	appendSchema := func(schema map[string]any, cyclicRootRef string) {
		encoded, _ := json.Marshal(schema)
		identity := string(encoded)
		if cyclicRootRef != "" {
			cyclicRootRefs[identity] = cyclicRootRef
		}
		if !seen[identity] {
			seen[identity] = true
			schemas = append(schemas, schema)
		}
	}
	for _, key := range keys {
		isExact := len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9'
		if !isExact && key != "2XX" && !(key == "default" && !hasRange && exactSuccesses < 100) {
			continue
		}
		ref := responses[key]
		if ref == nil || ref.Value == nil || len(ref.Value.Content) == 0 {
			continue // this outcome emits no value
		}
		mediaKeys := make([]string, 0, len(ref.Value.Content))
		for mediaKey := range ref.Value.Content {
			mediaKeys = append(mediaKeys, mediaKey)
		}
		sort.Strings(mediaKeys)
		for _, mediaKey := range mediaKeys {
			var parsed parsedMediaType
			var err error
			if hasMediaFidelity(bindingSpec) {
				parsed, err = parseMediaDeclaration(mediaKey)
			} else {
				parsed, err = parseMediaType(mediaKey)
			}
			if err != nil || (parsed.rangeSpecificity < 2 && !hasResponseFidelity(bindingSpec)) {
				continue
			}
			media := ref.Value.Content[mediaKey]
			admitsJSON := isJSONMediaType(parsed.base) || (parsed.rangeSpecificity < 2 && (parsed.base == "application/*" || parsed.base == "*/*"))
			if admitsJSON {
				if media == nil || media.Schema == nil {
					return nil // an unconstrained JSON success can emit any JSON value
				}
				cyclicRootRef := ""
				if media.Schema.Ref != "" && graph.isCyclic(media.Schema.Ref) {
					cyclicRootRef = media.Schema.Ref
				}
				appendSchema(graph.rootForm(media.Schema, schemaOverlays), cyclicRootRef)
			}
			admitsNonJSON := !isJSONMediaType(parsed.base) || parsed.rangeSpecificity < 2
			if admitsNonJSON {
				rawBoundary := hasResponseFidelity(bindingSpec) && !strings.HasPrefix(parsed.base, "text/") &&
					((isOpenAPI30(majorMinor(openapiVersion)) &&
						((hasSchemaOmittedOAS30ByteCarriage(bindingSpec) && parsed.rangeSpecificity == 2 && mediaSchema(media) == nil) || binarySignaled(mediaSchema(media), true))) ||
						(!isOpenAPI30(majorMinor(openapiVersion)) && media != nil && media.Schema == nil))
				// The revision-1 builtin non-JSON lane emits text, including
				// one text value per SSE event. The artifact-authorized
				// byte lane uses canonical Base64 at the operation boundary.
				if rawBoundary {
					appendSchema(map[string]any{"type": "string", "contentEncoding": "base64"}, "")
				} else {
					appendSchema(map[string]any{"type": "string"}, "")
				}
			}
		}
	}
	if len(schemas) == 0 {
		return nil
	}
	if len(schemas) == 1 {
		return schemas[0]
	}
	anyOf := make([]any, len(schemas))
	for i, schema := range schemas {
		encoded, _ := json.Marshal(schema)
		if ref := cyclicRootRefs[string(encoded)]; ref != "" {
			// The TypeScript processor retains dereferenced object identity:
			// a cyclic component below an anyOf root is hoisted as a unit. Go's
			// typed marshal would otherwise inline that first occurrence and
			// hoist only its nested back-edge. Keep the artifact ref long enough
			// for inlineRefsInOperationSchema to make the same $defs projection.
			anyOf[i] = map[string]any{"$ref": ref}
		} else {
			anyOf[i] = schema
		}
	}
	return map[string]any{"anyOf": anyOf}
}

// stringSlice extracts a string list from a schema field that may be
// []string (Go-built schemas) or []any (anything that round-tripped
// through JSON — the required-ness of body fields was silently dropped
// when only []string was handled).
func stringSlice(v any) []string {
	switch vals := v.(type) {
	case []string:
		return vals
	case []any:
		out := make([]string, 0, len(vals))
		for _, item := range vals {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func schemaRefToMap(ref *openapi3.SchemaRef, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	if ref == nil || ref.Value == nil {
		return nil
	}

	// The loader has already resolved ref.Value. Marshal that resolved schema,
	// not the SchemaRef wrapper: the wrapper intentionally serializes as the
	// original `$ref`, which is meaningful inside the OpenAPI artifact but
	// dangles once this schema is projected into an operation-local OBI
	// contract. Nested refs remain visible and are handled by inlineRefs.
	data, err := ref.Value.MarshalJSON()
	if err != nil {
		return map[string]any{"type": "object", "x-conversion-error": err.Error()}
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{"type": "object", "x-conversion-error": err.Error()}
	}

	delete(result, "__origin__")
	if schemaOverlays != nil {
		schemaOverlays.apply(ref, result)
	}

	return result
}

// buildRefRegistry constructs a map of `$ref string → fully-marshaled
// resolved schema` from `doc.Components.Schemas`. The resulting values
// are themselves the OUTPUT of marshaling each component schema with
// kin-openapi (which still leaves nested `$ref` strings in place);
// inlineRefs walks them recursively to fully flatten.
//
// This is used to post-process operation input/output schemas built
// by buildInputSchema / buildOutputSchema, which serialize via
// kin-openapi's `SchemaRef.MarshalJSON` and thus carry `$ref` strings
// pointing into `#/components/schemas/X`. The OBI consumer (codegen)
// has no `components/schemas/` namespace of its own, so any unresolved
// ref becomes `unknown` in the generated client. Inlining everything
// at create time keeps the OBI self-contained.
func buildRefRegistry(doc *openapi3.T, schemaOverlays *rawSchemaOverlayCollector) map[string]any {
	registry := make(map[string]any)
	if doc == nil || doc.Components == nil {
		return registry
	}
	for name, schemaRef := range doc.Components.Schemas {
		if schemaRef == nil || schemaRef.Value == nil {
			continue
		}
		// Marshal the resolved component value. A SchemaRef wrapper whose
		// component is itself an alias would otherwise register only the alias
		// `$ref` and fail to materialize the schema at an operation boundary.
		data, err := schemaRef.Value.MarshalJSON()
		if err != nil {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			continue
		}
		delete(v, "__origin__")
		if schemaOverlays != nil {
			schemaOverlays.apply(schemaRef, v)
		}
		registry["#/components/schemas/"+escapeJSONPointerSegment(name)] = v
	}
	return registry
}

// schemaTraversalPosition keeps JSON-shaped annotation and extension data
// opaque while walking a JSON Schema. A key named "$ref" has reference
// semantics only at a schema position; the same spelling in default,
// example, enum, const, or an extension is ordinary application data.
type schemaTraversalPosition uint8

const (
	schemaObjectPosition schemaTraversalPosition = iota
	schemaMapPosition
	schemaArrayPosition
	schemaDataPosition
)

func schemaChildPosition(key string) schemaTraversalPosition {
	switch {
	case schemaBearingSingleKeys[key]:
		return schemaObjectPosition
	case schemaBearingMapKeys[key]:
		return schemaMapPosition
	case schemaBearingArrayKeys[key]:
		return schemaArrayPosition
	default:
		return schemaDataPosition
	}
}

func validateProjectedOperationDialects(doc *openapi3.T, op *openapi3.Operation, params openapi3.Parameters, requestPlans []*bodyPlan, bindingSpec string) error {
	documentDialect := doc.JSONSchemaDialect
	if documentDialect == "" {
		documentDialect = "https://spec.openapis.org/oas/3.1/dialect/base"
	}
	seen := map[struct {
		schema  *openapi3.Schema
		dialect string
	}]bool{}
	validate := func(side string, ref *openapi3.SchemaRef) error {
		if err := validateSchemaRefDialect(ref, documentDialect, seen); err != nil {
			return fmt.Errorf("%s schema uses %w", side, err)
		}
		return nil
	}
	for _, parameterRef := range params {
		if parameterRef == nil || parameterRef.Value == nil {
			continue
		}
		parameter := parameterRef.Value
		if parameter.Schema != nil {
			if err := validate("input", parameter.Schema); err != nil {
				return err
			}
			continue
		}
		keys := make([]string, 0, len(parameter.Content))
		for key := range parameter.Content {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			if media := parameter.Content[keys[0]]; media != nil && media.Schema != nil {
				if err := validate("input", media.Schema); err != nil {
					return err
				}
			}
		}
	}
	for _, plan := range requestPlans {
		if plan != nil && plan.media != nil && plan.media.Schema != nil {
			if err := validate("input", plan.media.Schema); err != nil {
				return err
			}
		}
	}
	for _, ref := range projectedOutputSchemaRefs(op, bindingSpec) {
		if err := validate("output", ref); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaRefDialect(ref *openapi3.SchemaRef, inherited string, seen map[struct {
	schema  *openapi3.Schema
	dialect string
}]bool) error {
	if ref == nil || ref.Value == nil {
		return nil
	}
	schema := ref.Value
	dialect := inherited
	if schema.SchemaDialect != "" {
		dialect = schema.SchemaDialect
	}
	if !supportedComposingDialect(dialect) {
		return fmt.Errorf("unsupported OpenAPI 3.1 schema dialect %q; portable OBI synthesis is pinned to JSON Schema 2020-12 (OBI-D-06)", dialect)
	}
	key := struct {
		schema  *openapi3.Schema
		dialect string
	}{schema: schema, dialect: dialect}
	if seen[key] {
		return nil
	}
	seen[key] = true
	children := make([]*openapi3.SchemaRef, 0, len(schema.OneOf)+len(schema.AnyOf)+len(schema.AllOf)+len(schema.Properties)+len(schema.PatternProperties)+len(schema.DependentSchemas)+len(schema.Defs)+12)
	children = append(children, schema.OneOf...)
	children = append(children, schema.AnyOf...)
	children = append(children, schema.AllOf...)
	children = append(children, schema.Not, schema.Items, schema.Contains, schema.PropertyNames, schema.If, schema.Then, schema.Else, schema.ContentSchema)
	children = append(children, schema.AdditionalProperties.Schema, schema.UnevaluatedItems.Schema, schema.UnevaluatedProperties.Schema)
	children = append(children, schema.PrefixItems...)
	for _, schemas := range []openapi3.Schemas{schema.Properties, schema.PatternProperties, schema.DependentSchemas, schema.Defs} {
		for _, child := range schemas {
			children = append(children, child)
		}
	}
	for _, child := range children {
		if err := validateSchemaRefDialect(child, dialect, seen); err != nil {
			return err
		}
	}
	return nil
}

func projectedOutputSchemaRefs(op *openapi3.Operation, bindingSpec string) []*openapi3.SchemaRef {
	if op == nil || op.Responses == nil {
		return nil
	}
	responses := op.Responses.Map()
	keys := make([]string, 0, len(responses))
	hasRange := false
	exactSuccesses := 0
	for key := range responses {
		keys = append(keys, key)
		if key == "2XX" {
			hasRange = true
		}
		if len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9' {
			exactSuccesses++
		}
	}
	sort.Strings(keys)
	var refs []*openapi3.SchemaRef
	for _, key := range keys {
		isExact := len(key) == 3 && key[0] == '2' && key[1] >= '0' && key[1] <= '9' && key[2] >= '0' && key[2] <= '9'
		if !isExact && key != "2XX" && !(key == "default" && !hasRange && exactSuccesses < 100) {
			continue
		}
		responseRef := responses[key]
		if responseRef == nil || responseRef.Value == nil {
			continue
		}
		for mediaKey, media := range responseRef.Value.Content {
			var parsed parsedMediaType
			var err error
			if hasMediaFidelity(bindingSpec) {
				parsed, err = parseMediaDeclaration(mediaKey)
			} else {
				parsed, err = parseMediaType(mediaKey)
			}
			admitsJSON := err == nil && (isJSONMediaType(parsed.base) || (hasResponseFidelity(bindingSpec) && parsed.rangeSpecificity < 2 && (parsed.base == "application/*" || parsed.base == "*/*")))
			if !admitsJSON {
				continue
			}
			if media == nil || media.Schema == nil {
				return nil // buildOutputSchema publishes no contract in this case
			}
			refs = append(refs, media.Schema)
		}
	}
	return refs
}

// defNameForRef derives the $defs key for a cyclic ref: the component name
// for `#/components/schemas/X`, else the sanitized trailing pointer segment.
func defNameForRef(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return unescapeJSONPointerSegment(ref[i+1:])
	}
	return ref
}

// escapeJSONPointerSegment escapes a string for use as an RFC 6901 segment.
func escapeJSONPointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

func unescapeJSONPointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
}

// resolveRegistryRef resolves both a named component ref and a JSON Pointer
// into a position below that component. OpenAPI permits refs such as
// `#/components/schemas/Envelope/properties/id`; registering only component
// roots leaves those valid source-artifact pointers dangling after the schema
// is projected into an operation-local OBI contract.
func resolveRegistryRef(ref string, registry map[string]any) (any, bool) {
	if value, found := registry[ref]; found {
		return value, true
	}
	const prefix = "#/components/schemas/"
	if !strings.HasPrefix(ref, prefix) {
		return nil, false
	}
	segments := strings.Split(strings.TrimPrefix(ref, prefix), "/")
	if len(segments) < 2 {
		return nil, false
	}
	rootRef := prefix + segments[0]
	current, found := registry[rootRef]
	if !found {
		return nil, false
	}
	for _, encoded := range segments[1:] {
		segment := unescapeJSONPointerSegment(encoded)
		switch node := current.(type) {
		case map[string]any:
			current, found = node[segment]
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, false
			}
			current, found = node[index], true
		default:
			return nil, false
		}
		if !found {
			return nil, false
		}
	}
	return current, true
}

// inlineRefs walks `node` recursively and replaces every `{"$ref":
// "#/components/schemas/X"}` object with the resolved schema from
// `registry`. Resolution is iterative on the resolved value too, so
// chains of refs (X → Y → Z) flatten in a single pass.
//
// `seen` tracks refs currently being expanded in the call stack to
// avoid infinite recursion on cyclic schemas. When a cycle is hit
// the ref is left in place (the node keeps `{"$ref": "..."}`); the
// codegen falls back to `unknown` for that field, which is the same
// behavior the user would have seen before this fix.
type decycleContext struct {
	cyclic  map[string]bool
	refBase string
	defs    map[string]any
}

func inlineRefs(node any, registry map[string]any, seen map[string]bool, ctx *decycleContext) any {
	return inlineRefsAt(node, schemaObjectPosition, registry, seen, ctx)
}

func inlineRefsAt(node any, position schemaTraversalPosition, registry map[string]any, seen map[string]bool, ctx *decycleContext) any {
	if position == schemaDataPosition {
		return node
	}
	switch v := node.(type) {
	case map[string]any:
		// Check if this object IS a ref.
		if ref, ok := v["$ref"].(string); position == schemaObjectPosition && ok {
			var expanded any
			if ctx != nil && ctx.cyclic[ref] {
				// A cycle participant: every occurrence becomes a
				// same-document reference to a hoisted $defs entry (the
				// dialect's own recursion mechanism — OBI-D-16-resolvable
				// from the OBI root). Mirrors the TS SDK's decycleSchema.
				// Hoisting is keyed by the ref's own identity, and the `$defs`
				// key is assigned afterwards over the complete set of cut
				// points (finalizeHoistedNames). Naming during the walk would
				// make the key a function of traversal order.
				if _, materialized := ctx.defs[ref]; !materialized {
					ctx.defs[ref] = nil // reserve before expansion: terminates self-reference
					if resolved, found := resolveRegistryRef(ref, registry); found {
						ctx.defs[ref] = inlineRefsAt(resolved, schemaObjectPosition, registry, seen, ctx)
					}
				}
				expanded = map[string]any{"$ref": provisionalDefPointer(ctx.refBase, ref)}
			} else if seen[ref] {
				// Cycle outside the registry graph: leave the ref in place.
				expanded = map[string]any{"$ref": ref}
			} else if resolved, found := resolveRegistryRef(ref, registry); found {
				// Mark this ref as being expanded, recurse to inline
				// any nested refs in the resolved value, then unmark.
				seen[ref] = true
				expanded = inlineRefsAt(resolved, schemaObjectPosition, registry, seen, ctx)
				delete(seen, ref)
			} else {
				expanded = map[string]any{"$ref": ref}
			}

			// JSON Schema 2020-12 permits `$ref` siblings. Preserve their
			// intersection semantics when moving the schema into OBI: merge
			// non-conflicting keywords directly, and use allOf when the resolved
			// target declares the same keyword. This also retains descriptive
			// siblings found in common OpenAPI 3.0 documents without leaking the
			// source-artifact reference.
			siblings := make(map[string]any, len(v)-1)
			for key, value := range v {
				if key != "$ref" {
					siblings[key] = inlineRefsAt(value, schemaChildPosition(key), registry, seen, ctx)
				}
			}
			if len(siblings) == 0 {
				return expanded
			}
			if base, ok := expanded.(map[string]any); ok {
				merged := make(map[string]any, len(base)+len(siblings))
				conflict := false
				for key, value := range base {
					merged[key] = value
				}
				for key, value := range siblings {
					if _, present := merged[key]; present {
						conflict = true
						break
					}
					merged[key] = value
				}
				if !conflict {
					return merged
				}
			}
			return map[string]any{"allOf": []any{expanded, siblings}}
		}
		// Recurse only through JSON Schema-bearing keywords. Annotation and
		// extension payloads are data even when they contain schema-like keys.
		out := make(map[string]any, len(v))
		for k, val := range v {
			childPosition := schemaDataPosition
			switch position {
			case schemaObjectPosition:
				childPosition = schemaChildPosition(k)
			case schemaMapPosition:
				childPosition = schemaObjectPosition
			}
			out[k] = inlineRefsAt(val, childPosition, registry, seen, ctx)
		}
		return out
	case []any:
		out := make([]any, len(v))
		childPosition := schemaDataPosition
		if position == schemaArrayPosition {
			childPosition = schemaObjectPosition
		}
		for i, item := range v {
			out[i] = inlineRefsAt(item, childPosition, registry, seen, ctx)
		}
		return out
	default:
		return v
	}
}

// inlineRefsInOperationSchema applies inlineRefs to a single operation
// input or output schema (a map[string]any built by schemaRefToMap or
// buildInputSchema/buildOutputSchema). Returns the input map mutated
// in place (and also returned, for chaining).
// The second return value names the definitions this pass minted, as
// distinct from any `$defs` the artifact's own Schema Object declared. Only
// minted definitions are subject to the reachability closure applied after
// direction projection (pruneUnreachableDefs): an authorial definition is
// declared artifact content and stands whether or not anything references it.
func inlineRefsInOperationSchema(schema map[string]any, registry map[string]any, cyclic map[string]bool, refBase string, namer *cutPointNamer) (map[string]any, map[string]bool) {
	if schema == nil {
		return nil, nil
	}
	ctx := &decycleContext{cyclic: cyclic, refBase: refBase, defs: map[string]any{}}
	result := inlineRefs(schema, registry, map[string]bool{}, ctx)
	m, ok := result.(map[string]any)
	if !ok {
		return schema, nil
	}
	return finalizeHoistedNames(m, ctx, namer)
}

// provisionalDefPointer is the pointer emitted while walking, before the set of
// cut points is complete. It embeds the ref's own identity, so it is unique and
// cannot be forged by artifact content.
func provisionalDefPointer(refBase, ref string) string {
	return refBase + "/$defs/" + escapeJSONPointerSegment(ref)
}

// finalizeHoistedNames assigns the `$defs` keys once the complete set of cut
// points minted for this operation schema is known, then rewrites the
// provisional pointers the walk emitted. The rewrite is position-aware, so a
// `$ref` spelling that is ordinary application data (a default, an example, an
// extension payload) is never touched.
func finalizeHoistedNames(m map[string]any, ctx *decycleContext, namer *cutPointNamer) (map[string]any, map[string]bool) {
	if len(ctx.defs) == 0 {
		return m, nil
	}
	refs := make([]string, 0, len(ctx.defs))
	for ref := range ctx.defs {
		refs = append(refs, ref)
	}
	names := namer.assign(refs)
	rewrite := make(map[string]string, len(refs))
	defs := make(map[string]any, len(refs))
	hoisted := make(map[string]bool, len(refs))
	for _, ref := range refs {
		name := names[ref]
		rewrite[provisionalDefPointer(ctx.refBase, ref)] = ctx.refBase + "/$defs/" + escapeJSONPointerSegment(name)
		defs[name] = ctx.defs[ref]
		hoisted[name] = true
	}
	rewriteHoistedPointers(m, schemaObjectPosition, rewrite)
	for _, value := range defs {
		rewriteHoistedPointers(value, schemaObjectPosition, rewrite)
	}
	m["$defs"] = defs
	return m, hoisted
}

func rewriteHoistedPointers(node any, position schemaTraversalPosition, rewrite map[string]string) {
	if position == schemaDataPosition {
		return
	}
	switch v := node.(type) {
	case map[string]any:
		if position == schemaObjectPosition {
			if ref, ok := v["$ref"].(string); ok {
				if replacement, found := rewrite[ref]; found {
					v["$ref"] = replacement
				}
			}
		}
		for key, value := range v {
			childPosition := schemaDataPosition
			switch position {
			case schemaObjectPosition:
				childPosition = schemaChildPosition(key)
			case schemaMapPosition:
				childPosition = schemaObjectPosition
			}
			rewriteHoistedPointers(value, childPosition, rewrite)
		}
	case []any:
		childPosition := schemaDataPosition
		if position == schemaArrayPosition {
			childPosition = schemaObjectPosition
		}
		for _, item := range v {
			rewriteHoistedPointers(item, childPosition, rewrite)
		}
	}
}

// majorMinor reduces an artifact version string to its major.minor form
// ("3.1.0" → "3.1") for dialect decisions.
func majorMinor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}

// filterLadderInvalidAlternatives drops the request media candidates whose
// alternative the ladder judged invalid. The alternative is the unit that
// carries the defect and it never climbs, so the operation keeps whatever
// alternatives remain; only a REQUIRED body left with none is excluded, by the
// existing unresolvable-request-body rule. A synthetic (undeclared) candidate
// occupies no declared position and is never a ladder unit.
func filterLadderInvalidAlternatives(plans []*bodyPlan, verdict *floorOp, opRef string) []*bodyPlan {
	if verdict == nil || len(verdict.InvalidAlternatives) == 0 {
		return plans
	}
	kept := plans[:0:0]
	for _, plan := range plans {
		if plan.declared {
			altRef := opRef + "/requestBody/content/" + escapeJSONPointerToken(plan.mediaKey)
			if _, invalid := verdict.InvalidAlternatives[altRef]; invalid {
				continue
			}
		}
		kept = append(kept, plan)
	}
	return kept
}
