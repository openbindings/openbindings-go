package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

func convertDocToInterface(doc *openapi3.T, location string, warn func(openbindings.SynthesizerWarning)) openbindings.Interface {
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
		return iface
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

		pathParams := pathItem.Parameters
		for _, method := range httpMethods {
			op := pathItem.GetOperation(strings.ToUpper(method))
			if op == nil {
				continue
			}

			opKey := deriveOperationKey(op, path, method, usedKeys)
			usedKeys[opKey] = true

			obiOp := openbindings.Operation{
				Description: operationDescription(op),
				Deprecated:  op.Deprecated,
			}

			if len(op.Tags) > 0 {
				obiOp.Tags = op.Tags
			}

			inputSchema := buildInputSchema(op, pathParams, refRegistry, opKey, warn)
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

	return iface
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
// own `openapi` field discriminates the accepted lines (OAPI-P-01).
//
// String content parses as YAML 1.2 (JSON being a valid subset); duplicate
// mapping keys are refused loudly by the YAML layer itself, satisfying the
// §3 duplicate-key pin.
func loadDocument(location string, content any) (*openapi3.T, error) {
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

func loadDocumentRaw(loader *openapi3.Loader, location string, content any) (*openapi3.T, error) {
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
	// loads from its path; bare filesystem paths are accepted leniently for
	// local tooling.
	if strings.HasPrefix(location, "file://") {
		loc, err := url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("invalid URL %q: %w", location, err)
		}
		return loader.LoadFromFile(loc.Path)
	}
	return loader.LoadFromFile(location)
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

// checkAcceptedOpenAPIVersion discriminates the accepted lines per
// OAPI-P-01: the artifact's own `openapi` field must declare 3.0.* or
// 3.1.*; any other value — a Swagger 2.0 `swagger` field included — is
// refused loudly at load.
func checkAcceptedOpenAPIVersion(doc *openapi3.T) error {
	v := doc.OpenAPI
	if v == "" {
		return fmt.Errorf("document declares no `openapi` field: openbindings.openapi@1 accepts OpenAPI 3.0.x and 3.1.x documents only (OAPI-P-01; Swagger 2.0 is not accepted)")
	}
	mm := majorMinor(v)
	if mm == "3.0" || mm == "3.1" {
		return nil
	}
	return fmt.Errorf("unsupported OpenAPI version %q: openbindings.openapi@1 accepts the 3.0.x and 3.1.x lines only (OAPI-P-01)", v)
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

func buildInputSchema(op *openapi3.Operation, pathParams openapi3.Parameters, refRegistry map[string]any, opKey string, warn func(openbindings.SynthesizerWarning)) map[string]any {
	properties := map[string]any{}
	var required []string
	hasOpenBody := false

	allParams := mergeParameters(pathParams, op.Parameters)

	for _, paramRef := range allParams {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		param := paramRef.Value

		if param.In == "cookie" {
			continue
		}

		prop := paramToSchema(param)
		if prop != nil {
			properties[param.Name] = prop
		}

		if param.Required {
			required = append(required, param.Name)
		}
	}

	if op.RequestBody != nil && op.RequestBody.Value != nil {
		rb := op.RequestBody.Value
		bodySchema := requestBodyToSchema(rb)
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
			if bodyProps, ok := bodySchema["properties"].(map[string]any); ok {
				for k, v := range bodyProps {
					// Field-collision rule: a name declared as a parameter
					// AND a body property flattens to ONE input field (the
					// body's schema wins deterministically); at invocation
					// the one value is delivered to every declared wire
					// location. Warn so the merge is never silent.
					if _, collides := properties[k]; collides && warn != nil {
						warn(openbindings.SynthesizerWarning{
							Code:    "openapi.param_body_collision",
							Message: fmt.Sprintf("field %q is declared as a parameter and a body property; the flattened input carries one field (body schema shown) whose value is delivered to both wire locations at invocation", k),
							Path:    fmt.Sprintf("operations.%s.input.properties.%s", opKey, k),
						})
					}
					properties[k] = v
				}
				required = append(required, stringSlice(bodySchema["required"])...)
			} else if isObjectTypedSchema(bodySchema) {
				// A free-form object body (type object, no named
				// properties): the flattened model passes unmatched input
				// fields through into the body (openbindings.openapi@1
				// §9.1), so the flattened surface stays an OPEN object —
				// the synthetic `body` wrap is reserved for NON-object
				// body schemas, and wrapping here would describe a field
				// the conformant invoker refuses as unmatched.
				hasOpenBody = true
			} else {
				properties["body"] = bodySchema
				if rb.Required {
					required = append(required, "body")
				}
			}
		}
	}

	if len(properties) == 0 {
		if hasOpenBody {
			return map[string]any{"type": "object"}
		}
		return nil
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
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

	prop := map[string]any{"type": "string"}
	if param.Description != "" {
		prop["description"] = param.Description
	}
	return prop
}

func requestBodyToSchema(rb *openapi3.RequestBody) map[string]any {
	if rb.Content == nil {
		return nil
	}

	mt := preferJSONMediaType(rb.Content)
	if mt == nil || mt.Schema == nil {
		return nil
	}

	return schemaRefToMap(mt.Schema)
}

func buildOutputSchema(op *openapi3.Operation) map[string]any {
	if op.Responses == nil {
		return nil
	}

	for _, code := range []string{"200", "201", "202"} {
		resp := op.Responses.Value(code)
		if resp == nil || resp.Value == nil {
			continue
		}
		return responseToSchema(resp.Value)
	}

	return nil
}

func responseToSchema(resp *openapi3.Response) map[string]any {
	if resp.Content == nil {
		return nil
	}

	mt := preferJSONMediaType(resp.Content)
	if mt == nil || mt.Schema == nil {
		return nil
	}

	return schemaRefToMap(mt.Schema)
}

func preferJSONMediaType(content openapi3.Content) *openapi3.MediaType {
	if mt := content.Get("application/json"); mt != nil {
		return mt
	}

	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if strings.Contains(k, "json") {
			return content[k]
		}
	}

	if len(keys) > 0 {
		return content[keys[0]]
	}
	return nil
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

// isObjectTypedSchema reports whether a body schema is explicitly
// object-typed (3.0 string form or a single-element 3.1 type array): the
// flattened model's passthrough case, never the synthetic-body wrap.
func isObjectTypedSchema(schema map[string]any) bool {
	switch ty := schema["type"].(type) {
	case string:
		return ty == "object"
	case []any:
		return len(ty) == 1 && ty[0] == "object"
	}
	return false
}
