package openapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

func convertDocToInterface(doc *openapi3.T, location string) openbindings.Interface {
	formatVersion := openbindings.DetectFormatVersion(doc.OpenAPI)

	sourceEntry := openbindings.Source{
		Format: "openapi@" + formatVersion,
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

			inputSchema := buildInputSchema(op, pathParams, refRegistry)
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

func loadDocument(location string, content any) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

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

	return loader.LoadFromFile(location)
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

func buildInputSchema(op *openapi3.Operation, pathParams openapi3.Parameters, refRegistry map[string]any) map[string]any {
	properties := map[string]any{}
	var required []string

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
					properties[k] = v
				}
				required = append(required, stringSlice(bodySchema["required"])...)
			} else {
				properties["body"] = bodySchema
				if rb.Required {
					required = append(required, "body")
				}
			}
		}
	}

	if len(properties) == 0 {
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
