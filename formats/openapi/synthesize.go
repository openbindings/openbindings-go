package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

func convertDocToInterface(doc *openapi3.T, location string, warn func(openbindings.SynthesizerWarning)) (openbindings.Interface, error) {
	// The schema-dialect translation keys off the artifact's own declared
	// version (3.0 vs 3.1); the identifier stays exact and version-free.
	formatVersion := majorMinor(doc.OpenAPI)

	sourceEntry := openbindings.Source{
		BindingSpec: BindingSpec,
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
			if field := unflattenableParam(params); field != "" {
				return iface, unrealizableOperation(opKey, fmt.Sprintf("parameter %q has no unique revision-1 flattened identity", field))
			}

			var requestPlans []*bodyPlan
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				plans, planErr := planRequestBodies(op)
				if planErr == nil {
					for _, plan := range plans {
						if !candidateCollides(params, plan) {
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

			inputSchema := buildInputSchemaForPlans(op, params, requestPlans, refRegistry)
			if inputSchema != nil {
				inlined := inlineRefsInOperationSchema(inputSchema, refRegistry)
				obiOp.Input = translateSchemaDialect(inlined, formatVersion)
			}

			outputSchema := buildOutputSchema(op)
			if outputSchema != nil {
				inlined := inlineRefsInOperationSchema(outputSchema, refRegistry)
				obiOp.Output = translateSchemaDialect(inlined, formatVersion)
			}

			iface.Operations[opKey] = obiOp

			ref := buildJSONPointerRef(path, method)
			bindingKey := opKey + "." + DefaultSourceName
			iface.Bindings[bindingKey] = openbindings.BindingEntry{
				Operation: opKey,
				Source:    DefaultSourceName,
				Ref:       ref,
			}
		}
	}

	return iface, nil
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

func buildInputSchemaForPlans(op *openapi3.Operation, allParams openapi3.Parameters, requestPlans []*bodyPlan, refRegistry map[string]any) map[string]any {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return buildInputSchema(op, allParams, nil, refRegistry)
	}
	var variants []map[string]any
	if !op.RequestBody.Value.Required {
		if parameterOnly := buildInputSchema(op, allParams, nil, refRegistry); parameterOnly != nil {
			variants = append(variants, parameterOnly)
		}
	}
	for _, plan := range requestPlans {
		if schema := buildInputSchema(op, allParams, plan, refRegistry); schema != nil {
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

func buildInputSchema(op *openapi3.Operation, allParams openapi3.Parameters, requestPlan *bodyPlan, refRegistry map[string]any) map[string]any {
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
		if prop != nil {
			properties[param.Name] = prop
		}

		if param.Required {
			required = append(required, param.Name)
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
				if resolved, ok := inlineRefs(bodySchema, refRegistry, map[string]bool{}).(map[string]any); ok {
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
				properties["body"] = bodySchema
				if rb.Required {
					required = append(required, "body")
				}
			case hasProps:
				for k, v := range bodyProps {
					// Colliding candidates were removed before this plan was chosen.
					properties[k] = v
				}
				required = append(required, stringSlice(bodySchema["required"])...)
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

	data, err := ref.MarshalJSON()
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
		data, err := schemaRef.MarshalJSON()
		if err != nil {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			continue
		}
		delete(v, "__origin__")
		registry["#/components/schemas/"+name] = v
	}
	return registry
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
func inlineRefs(node any, registry map[string]any, seen map[string]bool) any {
	switch v := node.(type) {
	case map[string]any:
		// Check if this object IS a ref.
		if ref, ok := v["$ref"].(string); ok && len(v) == 1 {
			if seen[ref] {
				// Cycle: leave the ref in place.
				return v
			}
			resolved, found := registry[ref]
			if !found {
				return v
			}
			// Mark this ref as being expanded, recurse to inline
			// any nested refs in the resolved value, then unmark.
			seen[ref] = true
			expanded := inlineRefs(resolved, registry, seen)
			delete(seen, ref)
			return expanded
		}
		// Recurse into each property.
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = inlineRefs(val, registry, seen)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = inlineRefs(item, registry, seen)
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
func inlineRefsInOperationSchema(schema map[string]any, registry map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	result := inlineRefs(schema, registry, map[string]bool{})
	if m, ok := result.(map[string]any); ok {
		return m
	}
	return schema
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
