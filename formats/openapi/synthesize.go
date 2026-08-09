package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// unrealizableTarget records a paths operation admitted by the artifact but
// unrepresentable under revision 1's flattened boundary. Reported instead of
// returned as an error when the caller opts into per-operation tolerance
// (the coverage and inspection surfaces), so one unrepresentable operation
// narrows coverage rather than vetoing the document (core §10's posture;
// interface-synthesizer contract's "sound partial OBI").
type unrealizableTarget struct {
	ref          string
	operationKey string
	reasonCode   string
	rule         string
	message      string
}

// convertDocToInterface converts a loaded OpenAPI document into an
// OpenBindings interface.
//
// When onUnrealizable is non-nil, an operation whose revision-1 flattened
// boundary cannot be represented is reported and skipped — no operation, no
// binding — and synthesis continues (tolerant mode). When nil, the same
// condition returns an error (strict mode: SynthesizeInterface), preserving
// the convenient strict surface's guarantee that it never returns a
// statically unbindable partial interface without evidence.
func convertDocToInterface(doc *openapi3.T, location, bindingSpec string, warn func(openbindings.SynthesizerWarning), onUnrealizable func(unrealizableTarget)) (openbindings.Interface, error) {
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
	refRegistry := buildRefRegistry(doc)
	cyclic := cyclicRefs(refRegistry)

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

			opKey := deriveOperationKey(op, path, method, usedKeys)
			usedKeys[opKey] = true

			params := effectiveParameters(pathItem, op)
			if field := unflattenableParamForRevision(params, bindingSpec); field != "" {
				reason := fmt.Sprintf("parameter %q has no unique revision-1 flattened identity", field)
				if onUnrealizable != nil {
					onUnrealizable(unrealizableTarget{
						ref:          buildJSONPointerRef(path, method),
						operationKey: opKey,
						reasonCode:   "openapi.flattening_collision",
						rule:         "OAPI-P-03",
						message:      reason,
					})
					continue
				}
				return iface, unrealizableOperation(opKey, reason)
			}

			var requestPlans []*bodyPlan
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				plans, planErr := planRequestBodies(op)
				plannedCount := len(plans)
				if planErr == nil {
					for _, plan := range plans {
						if bindingSpec == BindingSpecV2 || !candidateCollides(params, plan) {
							requestPlans = append(requestPlans, plan)
						}
					}
				}
				requiredBody := op.RequestBody.Value.Required
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
						rule := "OAPI-P-04"
						if allCollided {
							code = "openapi.flattening_collision"
							rule = "OAPI-P-03"
						} else {
							var dme *degenerateMediaError
							if errors.As(planErr, &dme) {
								code = "openapi.media_schema_mismatch"
							}
						}
						onUnrealizable(unrealizableTarget{
							ref:          buildJSONPointerRef(path, method),
							operationKey: opKey,
							reasonCode:   code,
							rule:         rule,
							message:      reason + "; the required request body has no faithful revision-1 carriage",
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
					warn(openbindings.SynthesizerWarning{Code: code, Message: reason + "; optional body omitted from the synthesized contract", Path: fmt.Sprintf("operations.%s.input", opKey)})
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
			inputSchema := buildInputSchemaForPlans(op, params, requestPlans, refRegistry, routes)
			if inputSchema != nil {
				inlined := inlineRefsInOperationSchema(inputSchema, refRegistry, cyclic, opPointer+"/input")
				obiOp.Input = normalizeOperationSchema(inlined, formatVersion, schemaSalvageWarner(warn, opKey, "input"))
			}

			outputSchema := buildOutputSchema(op)
			if outputSchema != nil {
				inlined := inlineRefsInOperationSchema(outputSchema, refRegistry, cyclic, opPointer+"/output")
				obiOp.Output = normalizeOperationSchema(inlined, formatVersion, schemaSalvageWarner(warn, opKey, "output"))
			}

			iface.Operations[opKey] = obiOp

			ref := buildJSONPointerRef(path, method)
			bindingKey := opKey + "." + DefaultSourceName
			binding := openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Ref:       ref,
			}
			if bindingSpec == BindingSpecV2 && routes.needsTransform {
				binding.InputTransform = &openbindings.TransformOrRef{Inline: routes.transformExpression()}
			}
			iface.Bindings[bindingKey] = binding
		}
	}

	return iface, nil
}

// schemaSalvageWarner adapts the schema walker's salvage reports to
// SynthesizerWarnings at the operation's schema path. Salvage (repairing or
// dropping something the source spec shipped malformed) must never be
// silent — the warning is the evidence that the contract differs from what
// the artifact literally claimed. The walker decides code and message; this
// adapter contributes only the operation-rooted path. Returns nil when there
// is no warn sink, which the walker treats as "salvage without reporting".
func schemaSalvageWarner(warn func(openbindings.SynthesizerWarning), opKey, side string) func(path, code, message string) {
	if warn == nil {
		return nil
	}
	return func(path, code, message string) {
		warn(openbindings.SynthesizerWarning{
			Code:    code,
			Message: message,
			Path:    fmt.Sprintf("operations.%s.%s%s", opKey, side, path),
		})
	}
}

func unrealizableOperation(operationKey, reason string) error {
	return fmt.Errorf("cannot synthesize OpenAPI operation %q: %s; synthesis would return a statically unbindable partial interface", operationKey, reason)
}

// httpMethods defines the iteration order for path item methods.
// Matches TS to ensure deterministic output across languages.
var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// loadDocument loads and discriminates an OpenAPI source per
// openbindings.openapi@1 §3-§6: `content`, when present, is the artifact
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
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	doc, err := loadDocumentRaw(loader, location, content)
	if err != nil {
		return nil, err
	}
	if err := checkAcceptedOpenAPIVersion(doc); err != nil {
		return nil, err
	}
	return doc, nil
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

func loadDocumentRaw(loader *openapi3.Loader, location string, content json.RawMessage) (*openapi3.T, error) {
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
		if location != "" {
			loc, err := url.Parse(location)
			if err == nil {
				return loader.LoadFromDataWithPath(data, loc)
			}
		}
		// No co-present location: no base URI. Absolute http(s) references
		// still resolve; anything else (a relative $ref in particular) has
		// nothing to resolve against and refuses with a readable error —
		// never a silent working-directory file read.
		loader.ReadFromURIFunc = selfContainedReadFunc()
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
		return loader.LoadFromURI(loc)
	}

	// A file:// location (the conformant absolute-URI spelling, OAPI-D-02)
	// loads from its path.
	if strings.HasPrefix(location, "file://") {
		loc, err := url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("invalid URL %q: %w", location, err)
		}
		return loader.LoadFromFile(loc.Path)
	}
	u, _ := url.Parse(location) // validated above: absolute, non-opaque
	return nil, fmt.Errorf("openapi location scheme %q is not dereferenced by this processor (supported: file, http, https)", u.Scheme)
}

// selfContainedReadFunc allows absolute http(s) reference targets (they
// resolve without a base) and refuses everything else: with no co-present
// location the embedded artifact has no base URI, so a relative reference is
// unresolvable by definition (§6 — bundle before embedding).
func selfContainedReadFunc() openapi3.ReadFromURIFunc {
	httpRead := openapi3.ReadFromHTTP(http.DefaultClient)
	return func(loader *openapi3.Loader, u *url.URL) ([]byte, error) {
		if u.IsAbs() && (u.Scheme == "http" || u.Scheme == "https") {
			return httpRead(loader, u)
		}
		return nil, fmt.Errorf("reference %q cannot resolve: embedded content with no co-present location has no base URI and must be self-contained (bundle the document before embedding, or set the source's location)", u)
	}
}

// checkAcceptedOpenAPIVersion discriminates the exact accepted editions per
// OAPI-P-01. Patch-looking values outside the frozen set are not inferred
// compatible.
func checkAcceptedOpenAPIVersion(doc *openapi3.T) error {
	v := doc.OpenAPI
	if v == "" {
		return fmt.Errorf("document declares no `openapi` field: openbindings.openapi@1 requires one of its exact accepted OpenAPI editions (OAPI-P-01; Swagger 2.0 is not accepted)")
	}
	switch v {
	case "3.0.0", "3.0.1", "3.0.2", "3.0.3", "3.0.4",
		"3.1.0", "3.1.1", "3.1.2":
		return nil
	}
	return fmt.Errorf("unsupported OpenAPI version %q: openbindings.openapi@1 accepts exactly 3.0.0–3.0.4 and 3.1.0–3.1.2 (OAPI-P-01)", v)
}

func deriveOperationKey(op *openapi3.Operation, path, method string, used map[string]bool) string {
	if op.OperationID != "" {
		key := openbindings.SanitizeKey(op.OperationID)
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
	key = openbindings.SanitizeKey(key)
	return openbindings.UniqueKey(key, used)
}

func operationDescription(op *openapi3.Operation) string {
	if op.Description != "" {
		return op.Description
	}
	return op.Summary
}

func buildJSONPointerRef(path, method string) string {
	escaped := strings.ReplaceAll(path, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return "#/paths/" + escaped + "/" + strings.ToLower(method)
}

func buildInputSchemaForPlans(op *openapi3.Operation, allParams openapi3.Parameters, requestPlans []*bodyPlan, refRegistry map[string]any, routes abstractInputRoutes) map[string]any {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return buildInputSchema(op, allParams, nil, refRegistry, routes)
	}
	var variants []map[string]any
	if !op.RequestBody.Value.Required {
		if parameterOnly := buildInputSchema(op, allParams, nil, refRegistry, routes); parameterOnly != nil {
			variants = append(variants, parameterOnly)
		}
	}
	for _, plan := range requestPlans {
		if schema := buildInputSchema(op, allParams, plan, refRegistry, routes); schema != nil {
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

func buildInputSchema(op *openapi3.Operation, allParams openapi3.Parameters, requestPlan *bodyPlan, refRegistry map[string]any, routes abstractInputRoutes) map[string]any {
	properties := map[string]any{}
	var required []string
	// Only JSON-family object candidates can carry undeclared fields. The
	// parameter-only, multipart/form, and scalar-body surfaces stay closed.
	hasOpenBody := requestPlan != nil && requestPlan.family == familyJSON && !requestPlan.synthetic

	for _, paramRef := range allParams {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		param := paramRef.Value

		prop := paramToSchema(param)
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
		if requestPlan.media != nil && requestPlan.media.Schema != nil {
			bodySchema = schemaRefToMap(requestPlan.media.Schema)
		}
		if bodySchema != nil {
			// Resolve a $ref body BEFORE the flatten decision: bodies
			// declared by reference are the production norm, and wrapping
			// the unresolved {"$ref"} in a phantom "body" property emits a
			// contract the invoker then sends literally onto the wire.
			if _, isRef := bodySchema["$ref"]; isRef {
				// nil decycle context: this expansion only informs the
				// flatten decision; the embedded schema is decycled later by
				// inlineRefsInOperationSchema.
				if resolved, ok := inlineRefs(bodySchema, refRegistry, map[string]bool{}, nil).(map[string]any); ok {
					bodySchema = resolved
				}
			}
			bodyProps, hasProps := bodySchema["properties"].(map[string]any)
			switch {
			case !bodySchemaFlattens(hasProps, isObjectTypedSchema(bodySchema)):
				// A non-object body schema — array, scalar, binary, or
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
				for _, name := range stringSlice(bodySchema["required"]) {
					required = append(required, routes.bodyField(name))
				}
			default:
				// A free-form object body (type object, no named
				// properties): the flattened model passes unmatched input
				// fields through into the body (openbindings.openapi@1
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

func paramToSchema(param *openapi3.Parameter) map[string]any {
	if param.Schema != nil && param.Schema.Value != nil {
		schema := schemaRefToMap(param.Schema)
		if param.Description != "" {
			if schema == nil {
				schema = map[string]any{}
			}
			schema["description"] = param.Description
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
			schema := schemaRefToMap(media.Schema)
			if param.Description != "" {
				schema["description"] = param.Description
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

func buildOutputSchema(op *openapi3.Operation) map[string]any {
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
			parsed, err := parseMediaType(mediaKey)
			if err != nil {
				continue
			}
			var schema map[string]any
			if isJSONMediaType(parsed.base) {
				media := ref.Value.Content[mediaKey]
				if media == nil || media.Schema == nil {
					return nil // an unconstrained JSON success can emit any JSON value
				}
				schema = schemaRefToMap(media.Schema)
			} else {
				// The revision-1 builtin non-JSON lane emits text, including
				// one text value per SSE event.
				schema = map[string]any{"type": "string"}
			}
			encoded, _ := json.Marshal(schema)
			identity := string(encoded)
			if !seen[identity] {
				seen[identity] = true
				schemas = append(schemas, schema)
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
		anyOf[i] = schema
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

func schemaRefToMap(ref *openapi3.SchemaRef) map[string]any {
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
func buildRefRegistry(doc *openapi3.T) map[string]any {
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
		registry["#/components/schemas/"+escapeJSONPointerSegment(name)] = v
	}
	return registry
}

// cyclicRefs computes, once per document, the set of registry refs that
// participate in a reference cycle (can reach themselves through $ref
// strings). Mirrors the TS SDK's cyclicComponents (packages/openapi/src/util.ts).
func cyclicRefs(registry map[string]any) map[string]bool {
	// direct successor refs per registry entry
	succ := make(map[string][]string, len(registry))
	var collect func(node any, out *[]string)
	collect = func(node any, out *[]string) {
		switch v := node.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				*out = append(*out, ref)
			}
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				collect(v[k], out)
			}
		case []any:
			for _, item := range v {
				collect(item, out)
			}
		}
	}
	for ref, node := range registry {
		var out []string
		collect(node, &out)
		succ[ref] = out
	}
	cyclic := make(map[string]bool)
	for ref := range registry {
		visited := map[string]bool{}
		work := append([]string(nil), succ[ref]...)
		for len(work) > 0 {
			current := work[len(work)-1]
			work = work[:len(work)-1]
			if current == ref {
				cyclic[ref] = true
				break
			}
			if visited[current] {
				continue
			}
			visited[current] = true
			work = append(work, succ[current]...)
		}
	}
	return cyclic
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
	switch v := node.(type) {
	case map[string]any:
		// Check if this object IS a ref.
		if ref, ok := v["$ref"].(string); ok {
			var expanded any
			if ctx != nil && ctx.cyclic[ref] {
				// A cycle participant: every occurrence becomes a
				// same-document reference to a hoisted $defs entry (the
				// dialect's own recursion mechanism — OBI-D-16-resolvable
				// from the OBI root). Mirrors the TS SDK's decycleSchema.
				name := defNameForRef(ref)
				if _, materialized := ctx.defs[name]; !materialized {
					ctx.defs[name] = nil // reserve before expansion: terminates self-reference
					if resolved, found := resolveRegistryRef(ref, registry); found {
						ctx.defs[name] = inlineRefs(resolved, registry, seen, ctx)
					}
				}
				expanded = map[string]any{"$ref": ctx.refBase + "/$defs/" + escapeJSONPointerSegment(name)}
			} else if seen[ref] {
				// Cycle outside the registry graph: leave the ref in place.
				expanded = map[string]any{"$ref": ref}
			} else if resolved, found := resolveRegistryRef(ref, registry); found {
				// Mark this ref as being expanded, recurse to inline
				// any nested refs in the resolved value, then unmark.
				seen[ref] = true
				expanded = inlineRefs(resolved, registry, seen, ctx)
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
					siblings[key] = inlineRefs(value, registry, seen, ctx)
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
		// Recurse into each property.
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = inlineRefs(val, registry, seen, ctx)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = inlineRefs(item, registry, seen, ctx)
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
func inlineRefsInOperationSchema(schema map[string]any, registry map[string]any, cyclic map[string]bool, refBase string) map[string]any {
	if schema == nil {
		return nil
	}
	ctx := &decycleContext{cyclic: cyclic, refBase: refBase, defs: map[string]any{}}
	result := inlineRefs(schema, registry, map[string]bool{}, ctx)
	m, ok := result.(map[string]any)
	if !ok {
		return schema
	}
	if len(ctx.defs) > 0 {
		m["$defs"] = ctx.defs
	}
	return m
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

// bodySchemaFlattens is THE §9.1 flatten-vs-synthetic determination, in one
// place for the two sites that must never disagree: buildInputSchema, which
// publishes the flattened contract, and planRequestBody (media.go), which
// routes the wire. A declared request-body schema participates in the
// flattened model by property name iff it declares `properties` or an
// explicit object type; ANY other schema — array, scalar, binary, and the
// TYPELESS schema that declares neither — rides the synthetic `body`
// property, unwrapped at the wire. The determination is declaration-only:
// what the schema might admit at runtime never participates. Each caller
// extracts the two declaration facts from its own schema representation
// (the raw map here, kin-openapi's typed form in media.go) and routes the
// decision through this one predicate, so the published contract and the
// wire cannot diverge again.
func bodySchemaFlattens(hasProperties, objectTyped bool) bool {
	return hasProperties || objectTyped
}

// isObjectTypedSchema reports whether a body schema is explicitly
// object-typed (3.0 string form or a single-element 3.1 type array): the
// object half of bodySchemaFlattens' declaration facts, in the raw-map
// representation.
func isObjectTypedSchema(schema map[string]any) bool {
	switch ty := schema["type"].(type) {
	case string:
		return ty == "object"
	case []any:
		return len(ty) == 1 && ty[0] == "object"
	}
	return false
}
