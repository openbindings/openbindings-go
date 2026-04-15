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

			inputSchema := buildInputSchema(op, pathParams)
			if inputSchema != nil {
				obiOp.Input = inlineRefsInOperationSchema(inputSchema, refRegistry)
			}

			outputSchema := buildOutputSchema(op)
			if outputSchema != nil {
				obiOp.Output = inlineRefsInOperationSchema(outputSchema, refRegistry)
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

	populateSecurity(doc, &iface)

	return iface
}

// populateSecurity reads the doc's securitySchemes and per-operation security
// requirements and populates iface.Security and each binding's Security field.
func populateSecurity(doc *openapi3.T, iface *openbindings.Interface) {
	if doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return
	}

	// Convert all security schemes to SecurityMethod.
	schemeMethods := map[string]openbindings.SecurityMethod{}
	for name, ref := range doc.Components.SecuritySchemes {
		if ref == nil || ref.Value == nil {
			continue
		}
		schemeMethods[name] = convertSecurityScheme(ref.Value)
	}
	if len(schemeMethods) == 0 {
		return
	}

	// Build a mapping from binding key to the security requirement key.
	// We iterate paths/operations again to correlate with binding keys.
	if doc.Paths == nil {
		return
	}

	securityEntries := map[string][]openbindings.SecurityMethod{}
	usedKeys := map[string]bool{}

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
			bindingKey := opKey + "." + DefaultSourceName

			// Determine which security requirements apply.
			requirements := op.Security
			if requirements == nil {
				requirements = &doc.Security
			}
			if requirements == nil || len(*requirements) == 0 {
				continue
			}

			// Collect the scheme names from the requirement.
			var schemeNames []string
			for _, req := range *requirements {
				for schemeName := range req {
					schemeNames = append(schemeNames, schemeName)
				}
			}
			if len(schemeNames) == 0 {
				// Empty security requirement means explicitly public.
				continue
			}
			sort.Strings(schemeNames)

			secKey := strings.Join(schemeNames, "+")

			// Build the security entry if not already present.
			if _, ok := securityEntries[secKey]; !ok {
				var methods []openbindings.SecurityMethod
				for _, name := range schemeNames {
					if m, ok := schemeMethods[name]; ok {
						methods = append(methods, m)
					}
				}
				if len(methods) == 0 {
					continue
				}
				securityEntries[secKey] = methods
			}

			// Link the binding.
			if b, ok := iface.Bindings[bindingKey]; ok {
				b.Security = secKey
				iface.Bindings[bindingKey] = b
			}
		}
	}

	if len(securityEntries) > 0 {
		iface.Security = securityEntries
	}
}

// convertSecurityScheme converts a kin-openapi SecurityScheme to an OBI SecurityMethod.
func convertSecurityScheme(s *openapi3.SecurityScheme) openbindings.SecurityMethod {
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "bearer":
			return openbindings.SecurityMethod{Type: "bearer", Description: s.Description}
		case "basic":
			return openbindings.SecurityMethod{Type: "basic", Description: s.Description}
		default:
			return openbindings.SecurityMethod{Type: s.Scheme, Description: s.Description}
		}

	case "oauth2":
		m := openbindings.SecurityMethod{Type: "oauth2", Description: s.Description}
		if s.Flows != nil {
			// Use the first non-nil flow.
			var flow *openapi3.OAuthFlow
			switch {
			case s.Flows.AuthorizationCode != nil:
				flow = s.Flows.AuthorizationCode
			case s.Flows.Implicit != nil:
				flow = s.Flows.Implicit
			case s.Flows.ClientCredentials != nil:
				flow = s.Flows.ClientCredentials
			case s.Flows.Password != nil:
				flow = s.Flows.Password
			}
			if flow != nil {
				m.AuthorizeURL = flow.AuthorizationURL
				m.TokenURL = flow.TokenURL
				var scopes []string
				for scope := range flow.Scopes {
					scopes = append(scopes, scope)
				}
				sort.Strings(scopes)
				m.Scopes = scopes
			}
		}
		return m

	case "apiKey":
		return openbindings.SecurityMethod{
			Type:        "apiKey",
			Description: s.Description,
			Name:        s.Name,
			In:          s.In,
		}

	case "openIdConnect":
		// OpenID Connect builds on OAuth2 with a discovery document. The SDK's
		// SecurityMethod doesn't yet model OIDC explicitly, so we represent it
		// as bearer (the resolved access token's transport mechanism) and
		// preserve the discovery URL in the description so consumers can find
		// it. The discovery document at OpenIdConnectUrl exposes authorize and
		// token endpoints that a richer integration could read.
		desc := s.Description
		if s.OpenIdConnectUrl != "" {
			if desc != "" {
				desc += " "
			}
			desc += "(OpenID Connect discovery: " + s.OpenIdConnectUrl + ")"
		} else if desc == "" {
			desc = "OpenID Connect"
		}
		return openbindings.SecurityMethod{Type: "bearer", Description: desc}

	default:
		return openbindings.SecurityMethod{Type: s.Type, Description: s.Description}
	}
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

func buildInputSchema(op *openapi3.Operation, pathParams openapi3.Parameters) map[string]any {
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
			if bodyProps, ok := bodySchema["properties"].(map[string]any); ok {
				for k, v := range bodyProps {
					properties[k] = v
				}
				if bodyReq, ok := bodySchema["required"].([]string); ok {
					required = append(required, bodyReq...)
				}
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
