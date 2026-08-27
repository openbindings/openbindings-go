package openapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/invoke"

	"github.com/getkin/kin-openapi/openapi3"
)

func requiredRequestMediaContext(doc *openapi3.T, op *openapi3.Operation, bindingSpec string, bindCtx map[string]any) (*invoke.ContextRequiredDetails, error) {
	if !hasMediaFidelity(bindingSpec) || op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
		return nil, nil
	}
	plans, err := planRequestBodiesFor(doc, op, bindingSpec)
	if err != nil {
		return nil, err
	}
	if !requestMediaUnconfigured(bindCtx) {
		_, err := configuredRequestPlansFor(doc, op, plans, bindCtx, bindingSpec)
		return nil, err
	}
	if soleConcreteRequestPlan(op, plans) != nil {
		return nil, nil
	}
	durable := true
	requirement := invoke.NewConfigValueRequirement(
		"requestMedia", "",
		"select the concrete request media type for this non-sole-concrete declaration",
		nil, &durable,
	)
	return &invoke.ContextRequiredDetails{
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{requirement}}},
	}, nil
}

func requiredPropertyMediaContext(doc *openapi3.T, op *openapi3.Operation, bindingSpec string, bindCtx map[string]any) (*invoke.ContextRequiredDetails, error) {
	if !hasMediaFidelity(bindingSpec) || op == nil || op.RequestBody == nil || op.RequestBody.Value == nil || !op.RequestBody.Value.Required {
		return nil, nil
	}
	plans, err := planRequestBodiesFor(doc, op, bindingSpec)
	if err != nil {
		return nil, err
	}
	if requestMediaUnconfigured(bindCtx) {
		sole := soleConcreteRequestPlan(op, plans)
		if sole == nil {
			// requestMedia is the first missing decision. A retry with that
			// selection can discover the selected alternative's property choices.
			return nil, nil
		}
		plans = []*bodyPlan{sole}
	} else {
		plans, err = configuredRequestPlansFor(doc, op, plans, bindCtx, bindingSpec)
		if err != nil {
			return nil, err
		}
	}
	configured, _, err := propertyMediaMap(bindCtx)
	if err != nil {
		return nil, err
	}
	durable := true
	var requirements []invoke.ContextRequirement
	seen := map[string]bool{}
	for _, plan := range plans {
		for _, name := range plan.propertyMedia {
			raw, present := configured[name]
			if !present || raw == nil {
				if !seen[name] {
					seen[name] = true
					requirements = append(requirements, invoke.NewConfigValueRequirement(
						"propertyMedia", "/"+escapeJSONPointerSegment(name),
						"select one concrete media type for this form or multipart property",
						nil, &durable,
					))
				}
				continue
			}
			choice, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("configuration.propertyMedia.%s must be a concrete media-type string", name)
			}
			if _, err := selectPropertyMedia(plan, name, choice); err != nil {
				return nil, err
			}
		}
	}
	if len(requirements) == 0 {
		return nil, nil
	}
	return &invoke.ContextRequiredDetails{
		Alternatives: []invoke.ContextAlternative{{Requirements: requirements}},
	}, nil
}

// mergeContextRequirements combines two independent context needs. Each
// details value is an OR of alternatives; satisfying the operation requires
// one alternative from each, so the merged value is their Cartesian product.
func mergeContextRequirements(left, right *invoke.ContextRequiredDetails) *invoke.ContextRequiredDetails {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	target := left.Target
	if target == "" {
		target = right.Target
	}
	merged := &invoke.ContextRequiredDetails{Target: target}
	for _, a := range left.Alternatives {
		for _, b := range right.Alternatives {
			requirements := append([]invoke.ContextRequirement(nil), a.Requirements...)
			for _, requirement := range b.Requirements {
				if !containsEquivalentContextRequirement(requirements, requirement) {
					requirements = append(requirements, requirement)
				}
			}
			merged.Alternatives = append(merged.Alternatives, invoke.ContextAlternative{Requirements: requirements})
		}
	}
	return merged
}

func containsEquivalentContextRequirement(requirements []invoke.ContextRequirement, wanted invoke.ContextRequirement) bool {
	for _, requirement := range requirements {
		if requirement.Type != wanted.Type || requirement.Name != wanted.Name {
			continue
		}
		if requirement.Extra["point"] == wanted.Extra["point"] && requirement.Extra["path"] == wanted.Extra["path"] {
			return true
		}
	}
	return false
}

// BuiltinClassify is the openapi builtin result classifier (OAPI-P-08):
// success iff the final HTTP status is 2xx (declared responses refine
// application failure DATA only, never classification).
func BuiltinClassify(_ invoke.InvokeSite, raw invoke.RawResult) (bool, error) {
	return raw.Status != nil && *raw.Status >= 200 && *raw.Status < 300, nil
}

// decodeByContentTypeFor returns the builtin decoder implementing the header
// rule (OAPI-P-07) for the exact binding family token in scope: strict JSON
// for application/json and +json suffixes (a declared-JSON body that fails to
// parse is a lying server — a loud ErrCodeResponseError, never a silent
// string); the text lane otherwise — bytes become a string per the header's
// charset parameter, defaulting to UTF-8, with invalid sequences a loud decode
// error. An empty body (204 included) yields null.
func decodeByContentTypeFor(contentType, bindingSpec string) invoke.OutputDecoder {
	isJSON := isJSONContentTypeFor(contentType, bindingSpec)
	return func(_ invoke.InvokeSite, raw invoke.RawResult) (any, error) {
		if len(raw.Body) == 0 {
			return nil, nil
		}
		if isJSON {
			var parsed any
			if err := json.Unmarshal(raw.Body, &parsed); err != nil {
				return nil, &invoke.InvocationError{
					Code: invoke.ErrCodeResponseError,
				}
			}
			return parsed, nil
		}
		return decodeTextLaneFor(contentType, raw.Body, bindingSpec)
	}
}

// decodeTextLaneFor decodes response bytes as text per the Content-Type
// header's charset parameter, defaulting to UTF-8 (OAPI-P-07). Invalid
// sequences, and charsets this implementation cannot decode, are loud
// decode errors — a consumer needing another charset overrides at the
// decode configuration point.
func decodeTextLaneFor(contentType string, body []byte, bindingSpec string) (any, error) {
	charset := "utf-8"
	if contentType != "" {
		if hasMediaFidelity(bindingSpec) {
			if parsed, err := parseRevision3MediaType(contentType); err == nil {
				if cs, present := parsed.params["charset"]; present {
					charset = cs
				}
			}
		} else if _, params, err := mime.ParseMediaType(contentType); err == nil {
			if cs := params["charset"]; cs != "" {
				charset = cs
			}
		}
	}
	switch strings.ToLower(charset) {
	case "utf-8", "utf8":
		if !utf8.Valid(body) {
			return nil, &invoke.InvocationError{
				Code: invoke.ErrCodeResponseError,
			}
		}
		return string(body), nil
	case "us-ascii", "ascii":
		for i := 0; i < len(body); i++ {
			if body[i] >= 0x80 {
				return nil, &invoke.InvocationError{
					Code: invoke.ErrCodeResponseError,
				}
			}
		}
		return string(body), nil
	case "iso-8859-1", "iso8859-1", "latin-1", "latin1":
		runes := make([]rune, len(body))
		for i, b := range body {
			runes[i] = rune(b)
		}
		return string(runes), nil
	default:
		return nil, &invoke.InvocationError{
			Code: invoke.ErrCodeResponseError,
		}
	}
}

// isJSONContentTypeFor reports whether a Content-Type header declares a JSON
// body for the exact binding family token in scope: application/json or any
// +json structured-suffix type. Absent or unparseable headers are NOT JSON
// (the text lane) — never sniffed.
func isJSONContentTypeFor(contentType, bindingSpec string) bool {
	if hasMediaFidelity(bindingSpec) {
		parsed, err := parseRevision3MediaType(contentType)
		return err == nil && isJSONMediaType(parsed.base)
	}
	return isJSONMediaType(normalizeMediaType(contentType))
}

// requiredContext derives the operation's authentication requirements from
// the document's securitySchemes and the operation-level (falling back to
// document-level) security requirements. It returns a ContextRequiredDetails
// when the call needs context the caller has not supplied, or nil when the
// operation needs no authentication, allows anonymous access, or the present
// context already satisfies a requirement.
//
// OpenAPI `security` is a disjunction (OR) of requirement objects, each a
// conjunction (AND) of scheme names — exactly the alternatives/requirements
// shape of ContextRequiredDetails.
func requiredContext(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters) *invoke.ContextRequiredDetails {
	plans := viableSecurityPlans(doc, op, baseURL, params)
	if len(plans) == 0 {
		return nil
	}
	alternatives := make([]invoke.ContextAlternative, 0, len(plans))
	for _, plan := range plans {
		if len(plan.context.Requirements) == 0 {
			// An empty requirement object means anonymous access is allowed;
			// the context contract intentionally has no empty alternatives.
			return nil
		}
		alternatives = append(alternatives, plan.context)
	}

	details := &invoke.ContextRequiredDetails{
		Target:       baseURL,
		Alternatives: alternatives,
	}
	// Already satisfiable from the supplied context: no challenge needed.
	if invoke.ContextSatisfies(bindCtx, details) {
		return nil
	}
	return details
}

// effectiveSecurityRequirements applies OpenAPI's operation-over-document
// security inheritance rule. A non-nil empty operation list explicitly
// disables document-level security.
func effectiveSecurityRequirements(doc *openapi3.T, op *openapi3.Operation) *openapi3.SecurityRequirements {
	if op != nil && op.Security != nil {
		return op.Security
	}
	return &doc.Security
}

type namedSecurityScheme struct {
	name   string
	scheme *openapi3.SecurityScheme
}

type securityPlan struct {
	context       invoke.ContextAlternative
	schemes       []namedSecurityScheme
	authoredIndex int
}

// securityConfigurationError reports an unusable effective security list.
// Scheme defects are confined to the alternatives that require them; a
// complete sibling alternative keeps the operation usable.
func securityConfigurationError(doc *openapi3.T, op *openapi3.Operation) error {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	if len(securityPlans(doc, op, "")) > 0 {
		return nil
	}
	return fmt.Errorf("the effective OpenAPI security list has no usable alternative")
}

// securityPlans expands the artifact's OR-of-AND Security Requirement
// Objects without flattening them. An OAuth scheme may contribute multiple
// usable declared flows, so one authored AND-set expands to the Cartesian
// product of its schemes' runtime alternatives. Every expanded plan still
// represents exactly one complete artifact-declared requirement object.
func securityPlans(doc *openapi3.T, op *openapi3.Operation, baseURL string) []securityPlan {
	return securityPlansWithContext(doc, op, baseURL, nil)
}

func securityPlansWithContext(doc *openapi3.T, op *openapi3.Operation, baseURL string, bindCtx map[string]any) []securityPlan {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	var plans []securityPlan
	for authoredIndex, secReq := range *requirements {
		if len(secReq) == 0 {
			plans = append(plans, securityPlan{authoredIndex: authoredIndex})
			continue
		}
		expanded := []securityPlan{{authoredIndex: authoredIndex}}
		expressible := true
		names := make([]string, 0, len(secReq))
		for schemeName := range secReq {
			names = append(names, schemeName)
		}
		sort.Strings(names)
		for _, schemeName := range names {
			scheme, ok := securitySchemeForOperation(doc, op, schemeName, bindCtx)
			if !ok || malformedSecurityScheme(scheme) != nil {
				expressible = false
				break
			}
			if strings.HasPrefix(doc.OpenAPI, "3.0.") && scheme.Type == "mutualTLS" {
				expressible = false
				break
			}
			requiredScopes := append([]string(nil), secReq[schemeName]...)
			if strings.HasPrefix(doc.OpenAPI, "3.0.") && !securitySchemeUsesScopes(scheme) && len(requiredScopes) > 0 {
				expressible = false
				break
			}
			options := schemeRequirements(scheme, baseURL, requiredScopes)
			if strings.HasPrefix(doc.OpenAPI, "3.1.") && !securitySchemeUsesScopes(scheme) && len(requiredScopes) > 0 {
				options = requirementsWithRoles(options, requiredScopes)
			}
			if len(options) == 0 {
				expressible = false
				break
			}
			next := make([]securityPlan, 0, len(expanded)*len(options))
			for _, plan := range expanded {
				for _, option := range options {
					durable := true
					option.Name = schemeName
					option.Durable = &durable
					if scheme.Description != "" {
						option.Description = scheme.Description
					}
					reqs := append([]invoke.ContextRequirement(nil), plan.context.Requirements...)
					reqs = append(reqs, option)
					schemes := append([]namedSecurityScheme(nil), plan.schemes...)
					schemes = append(schemes, namedSecurityScheme{name: schemeName, scheme: scheme})
					next = append(next, securityPlan{
						context:       invoke.ContextAlternative{Requirements: reqs},
						schemes:       schemes,
						authoredIndex: authoredIndex,
					})
				}
			}
			expanded = next
		}
		if expressible {
			plans = append(plans, expanded...)
		}
	}
	return plans
}

func securitySchemeUsesScopes(scheme *openapi3.SecurityScheme) bool {
	return scheme != nil && (scheme.Type == "oauth2" || scheme.Type == "openIdConnect")
}

func requirementsWithRoles(requirements []invoke.ContextRequirement, roles []string) []invoke.ContextRequirement {
	result := append([]invoke.ContextRequirement(nil), requirements...)
	for index := range result {
		if result[index].Extra == nil {
			result[index].Extra = map[string]any{}
		}
		result[index].Extra["roles"] = append([]string(nil), roles...)
	}
	return result
}

func securitySchemeForOperation(doc *openapi3.T, op *openapi3.Operation, name string, bindCtx map[string]any) (*openapi3.SecurityScheme, bool) {
	scope := "entry"
	if configured, ok := invoke.ContextConfiguration(bindCtx)["implicitConnectionScope"].(string); ok && configured != "" {
		scope = configured
	}
	if scope == "referring" && op != nil && op.Extensions != nil {
		if rawSchemes, ok := op.Extensions[referringSecuritySchemesMarker].(map[string]any); ok {
			if raw, found := rawSchemes[name]; found {
				encoded, err := json.Marshal(raw)
				if err == nil {
					var scheme openapi3.SecurityScheme
					if json.Unmarshal(encoded, &scheme) == nil {
						return &scheme, true
					}
				}
			}
		}
	}
	if doc == nil || doc.Components == nil || doc.Components.SecuritySchemes == nil {
		return nil, false
	}
	ref, found := doc.Components.SecuritySchemes[name]
	if !found || ref == nil || ref.Value == nil {
		return nil, false
	}
	return ref.Value, true
}

func malformedSecurityScheme(scheme *openapi3.SecurityScheme) error {
	if scheme == nil {
		return fmt.Errorf("Security Scheme Object is absent")
	}
	switch scheme.Type {
	case "apiKey":
		if scheme.Name == "" || (scheme.In != "header" && scheme.In != "query" && scheme.In != "cookie") {
			return fmt.Errorf("apiKey Security Scheme Object requires a name and a query, header, or cookie destination")
		}
	case "http":
		if scheme.Scheme == "" {
			return fmt.Errorf("HTTP Security Scheme Object requires a scheme")
		}
	case "oauth2":
		if scheme.Flows == nil {
			return fmt.Errorf("OAuth 2.0 Security Scheme Object requires flows")
		}
	case "openIdConnect":
		if scheme.OpenIdConnectUrl == "" {
			return fmt.Errorf("OpenID Connect Security Scheme Object requires openIdConnectUrl")
		}
	case "mutualTLS":
		// No additional fixed fields.
	default:
		return fmt.Errorf("Security Scheme Object type %q is not declared by the accepted OpenAPI edition", scheme.Type)
	}
	return nil
}

func viableSecurityPlans(doc *openapi3.T, op *openapi3.Operation, baseURL string, params openapi3.Parameters) []securityPlan {
	return viableSecurityPlansWithContext(doc, op, nil, baseURL, params)
}

func viableSecurityPlansWithContext(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters) []securityPlan {
	plans := securityPlansWithContext(doc, op, baseURL, bindCtx)
	if len(plans) == 0 {
		return nil
	}
	viable := make([]securityPlan, 0, len(plans))
	for _, plan := range plans {
		if err := checkCredentialCollisions(credentialDestinations(plan), params, nil); err == nil {
			viable = append(viable, plan)
		}
	}
	return viable
}

// electSecurityAlternative applies the binding's named `security`
// configuration point to the authored OR list. The portable SDK spelling is
// {"index": N}, where N is the zero-based Security Requirement Object index.
// A sole authored alternative self-selects; an effective empty list means no
// security. Array order is never a preference when more than one complete
// alternative is declared.
func electSecurityAlternative(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters) ([]securityPlan, error) {
	if configured, present := invoke.ContextConfiguration(bindCtx)["implicitConnectionScope"]; present && configured != nil {
		scope, ok := configured.(string)
		if !ok || (scope != "entry" && scope != "referring") {
			return nil, fmt.Errorf("configuration.implicitConnectionScope must be entry or referring")
		}
	}
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil, nil
	}
	selected := 0
	rawSelection, configured := invoke.ContextConfiguration(bindCtx)["security"]
	if len(*requirements) > 1 && (!configured || rawSelection == nil) {
		return nil, fmt.Errorf("the effective security list has %d alternatives; configuration.security must select one", len(*requirements))
	}
	if configured && rawSelection != nil {
		index, ok := securityConfigurationIndex(rawSelection)
		if !ok || index < 0 || index >= len(*requirements) {
			return nil, fmt.Errorf("configuration.security must select an effective alternative by zero-based index")
		}
		selected = index
	}
	plans := securityPlansWithContext(doc, op, baseURL, bindCtx)
	selectedPlans := make([]securityPlan, 0, len(plans))
	for _, plan := range plans {
		if plan.authoredIndex != selected {
			continue
		}
		if err := checkCredentialCollisions(credentialDestinations(plan), params, nil); err == nil {
			selectedPlans = append(selectedPlans, plan)
		}
	}
	if len(selectedPlans) == 0 {
		return nil, fmt.Errorf("selected security alternative %d is unusable", selected)
	}
	installSelectedSecurityAlternative(doc, op, (*requirements)[selected], selectedPlans)
	return selectedPlans, nil
}

// requiredSecuritySelectionContext exposes the binding's security election as
// a typed, preflightable configuration requirement. It intentionally carries
// authored indexes: the value chooses a complete Security Requirement Object,
// never one of the runtime expansions of an OAuth flow.
func requiredSecuritySelectionContext(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, target string) (*invoke.ContextRequiredDetails, bool, error) {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) <= 1 {
		return nil, false, nil
	}
	raw, present := invoke.ContextConfiguration(bindCtx)["security"]
	if present && raw != nil {
		index, ok := securityConfigurationIndex(raw)
		if !ok || index < 0 || index >= len(*requirements) {
			return nil, false, fmt.Errorf("configuration.security must select an effective alternative by zero-based index")
		}
		return nil, false, nil
	}
	values := make([]any, len(*requirements))
	for index := range values {
		values[index] = index
	}
	durable := true
	requirement := invoke.NewConfigValueRequirement(
		"security", "/index", "select one complete effective OpenAPI security alternative",
		map[string]any{"type": "integer", "enum": values}, &durable,
	)
	return &invoke.ContextRequiredDetails{
		Target:       target,
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{requirement}}},
	}, true, nil
}

func requiredImplicitConnectionScopeContext(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters, target string) (*invoke.ContextRequiredDetails, bool, error) {
	configuration := invoke.ContextConfiguration(bindCtx)
	if raw, present := configuration["implicitConnectionScope"]; present && raw != nil {
		scope, ok := raw.(string)
		if !ok || (scope != "entry" && scope != "referring") {
			return nil, false, fmt.Errorf("configuration.implicitConnectionScope must be entry or referring")
		}
		return nil, false, nil
	}
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil, false, nil
	}
	selected := 0
	if len(*requirements) > 1 {
		raw, present := configuration["security"]
		if !present || raw == nil {
			// Security election is the earlier configuration point.
			return nil, false, nil
		}
		index, ok := securityConfigurationIndex(raw)
		if !ok || index < 0 || index >= len(*requirements) {
			return nil, false, fmt.Errorf("configuration.security must select an effective alternative by zero-based index")
		}
		selected = index
	}
	if len(viableSecurityPlansAtScope(doc, op, bindCtx, baseURL, params, selected, "entry")) > 0 {
		return nil, false, nil
	}
	if len(viableSecurityPlansAtScope(doc, op, bindCtx, baseURL, params, selected, "referring")) == 0 {
		return nil, false, nil
	}
	durable := true
	requirement := invoke.NewConfigValueRequirement(
		"implicitConnectionScope", "", "resolve Security Requirement names in the referring OpenAPI document",
		map[string]any{"type": "string", "enum": []any{"referring"}}, &durable,
	)
	return &invoke.ContextRequiredDetails{
		Target:       target,
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{requirement}}},
	}, true, nil
}

func viableSecurityPlansAtScope(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters, authoredIndex int, scope string) []securityPlan {
	scoped := contextWithConfigurationPoint(bindCtx, "implicitConnectionScope", scope)
	plans := securityPlansWithContext(doc, op, baseURL, scoped)
	viable := make([]securityPlan, 0, len(plans))
	for _, plan := range plans {
		if plan.authoredIndex == authoredIndex && checkCredentialCollisions(credentialDestinations(plan), params, nil) == nil {
			viable = append(viable, plan)
		}
	}
	return viable
}

func contextWithConfigurationPoint(bindCtx map[string]any, point string, value any) map[string]any {
	copyContext := make(map[string]any, len(bindCtx)+1)
	for name, current := range bindCtx {
		copyContext[name] = current
	}
	configuration := invoke.ContextConfiguration(bindCtx)
	copyConfiguration := make(map[string]any, len(configuration)+1)
	for name, current := range configuration {
		copyConfiguration[name] = current
	}
	copyConfiguration[point] = value
	copyContext["configuration"] = copyConfiguration
	return copyContext
}

func securityConfigurationIndex(raw any) (int, bool) {
	if object, ok := raw.(map[string]any); ok {
		return configIndex(object["index"])
	}
	return configIndex(raw)
}

func installSelectedSecurityAlternative(doc *openapi3.T, op *openapi3.Operation, requirement openapi3.SecurityRequirement, plans []securityPlan) {
	if op == nil {
		return
	}
	copyRequirement := openapi3.SecurityRequirement{}
	for name, values := range requirement {
		copyRequirement[name] = append([]string(nil), values...)
	}
	selected := openapi3.SecurityRequirements{copyRequirement}
	op.Security = &selected
	if doc.Components == nil {
		doc.Components = &openapi3.Components{}
	}
	if doc.Components.SecuritySchemes == nil {
		doc.Components.SecuritySchemes = openapi3.SecuritySchemes{}
	}
	for _, plan := range plans {
		for _, named := range plan.schemes {
			doc.Components.SecuritySchemes[named.name] = &openapi3.SecuritySchemeRef{Value: named.scheme}
		}
	}
}

func requiredSelectedSecurityContext(plans []securityPlan, bindCtx map[string]any, baseURL string, handlers map[string]SecurityHandler) *invoke.ContextRequiredDetails {
	if len(plans) == 0 {
		return nil
	}
	alternatives := make([]invoke.ContextAlternative, 0, len(plans))
	for _, plan := range plans {
		if len(plan.context.Requirements) == 0 {
			return nil
		}
		if securityPlanHasRuntimeHandler(plan, handlers) {
			// The standalone engine owns mixed handler/builtin prerequisite
			// negotiation. It understands which named handler is already installed.
			return nil
		}
		alternatives = append(alternatives, plan.context)
	}
	details := &invoke.ContextRequiredDetails{Target: baseURL, Alternatives: alternatives}
	if invoke.ContextSatisfies(bindCtx, details) {
		return nil
	}
	return details
}

func securityPlanHasRuntimeHandler(plan securityPlan, handlers map[string]SecurityHandler) bool {
	for _, named := range plan.schemes {
		if handlers[named.name] != nil {
			return true
		}
	}
	return false
}

type credentialWirePolicy struct {
	query []credentialPlacement
}

func validateSelectedCredentials(plans []securityPlan, bindCtx map[string]any) (*credentialWirePolicy, error) {
	for _, plan := range plans {
		if len(plan.context.Requirements) > 0 && !invoke.ContextSatisfies(bindCtx, &invoke.ContextRequiredDetails{Alternatives: []invoke.ContextAlternative{plan.context}}) {
			continue
		}
		placements := credentialValues(plan, bindCtx)
		policy := &credentialWirePolicy{}
		for _, named := range plan.schemes {
			scheme := named.scheme
			switch scheme.Type {
			case "http":
				switch strings.ToLower(scheme.Scheme) {
				case "basic":
					user, password, ok := invoke.ContextBasicAuthFor(bindCtx, named.name)
					if ok {
						if strings.Contains(user, ":") || !validBasicCredentialText(user) || !validBasicCredentialText(password) {
							return nil, fmt.Errorf("basic credential %q violates RFC 7617 user-id or character-encoding constraints", named.name)
						}
					}
				case "bearer":
					if token := invoke.ContextBearerTokenFor(bindCtx, named.name); token != "" && !validBearerToken(token) {
						return nil, fmt.Errorf("bearer credential %q is not an RFC 6750 b64token", named.name)
					}
				}
			case "oauth2", "openIdConnect":
				token := invoke.ContextAccessTokenFor(bindCtx, named.name)
				if token == "" {
					token = invoke.ContextBearerToken(bindCtx)
				}
				if token != "" && !validBearerToken(token) {
					return nil, fmt.Errorf("access token for %q is not an RFC 6750 b64token", named.name)
				}
			}
		}
		for _, placement := range placements {
			switch placement.channel {
			case "cookie":
				if !validRFC6265CookieValue(placement.value) {
					return nil, fmt.Errorf("cookie credential %q cannot be carried as an RFC 6265 cookie-value", placement.name)
				}
			case "query":
				policy.query = append(policy.query, placement)
			}
		}
		return policy, nil
	}
	return &credentialWirePolicy{}, nil
}

func validBasicCredentialText(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func validBearerToken(value string) bool {
	if value == "" {
		return false
	}
	padding := false
	payload := false
	for _, char := range value {
		if char == '=' {
			padding = true
			continue
		}
		if padding {
			return false
		}
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || strings.ContainsRune("-._~+/", char)) {
			return false
		}
		payload = true
	}
	return payload
}

func validRFC6265CookieValue(value string) bool {
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char != 0x21 && !(char >= 0x23 && char <= 0x2b) && !(char >= 0x2d && char <= 0x3a) && !(char >= 0x3c && char <= 0x5b) && !(char >= 0x5d && char <= 0x7e) {
			return false
		}
	}
	return true
}

// securityAlternativesCollision reports an ownership conflict only when every
// complete runtime alternative is unusable. A later channel-safe alternative
// remains selectable and is the only one surfaced during context negotiation.
func securityAlternativesCollision(doc *openapi3.T, op *openapi3.Operation, baseURL string, params openapi3.Parameters) error {
	plans := securityPlans(doc, op, baseURL)
	if len(plans) == 0 {
		return nil
	}
	var first error
	for _, plan := range plans {
		err := checkCredentialCollisions(credentialDestinations(plan), params, nil)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	return first
}

// schemeToRequirement maps an OpenAPI security scheme to a context
// requirement, carrying the family-specific fields a resolver needs to act
// without out-of-band knowledge (notably oauth2 flow endpoints). Per the
// R2.c ruling, a scheme family the SDK cannot itself resolve is no longer
// dropped: it is surfaced with a type derived from the artifact ("auth.http."
// + the HTTP scheme for an unmapped "http" scheme, e.g. "auth.http.digest";
// "auth." + the artifact's own type otherwise, e.g. "auth.mutualTLS") so the
// alternative stays discoverable to a runtime with a resolver for it, rather
// than silently vanishing into an unauthenticated dispatch. Every switch arm
// now returns true; the bool is kept for signature stability (and as a hook
// for a genuinely inexpressible future case) rather than removed.
func schemeToRequirement(s *openapi3.SecurityScheme, baseURL string) (invoke.ContextRequirement, bool) {
	options := schemeRequirements(s, baseURL, nil)
	if len(options) == 0 {
		return invoke.ContextRequirement{}, false
	}
	return options[0], true
}

// schemeRequirements maps one security scheme to its runtime requirement
// choices. Only OAuth2 expands: every declared flow capable of granting the
// Security Requirement Object's authoritative scope array becomes a distinct
// alternative. Other schemes contribute exactly one requirement.
func schemeRequirements(s *openapi3.SecurityScheme, baseURL string, requiredScopes []string) []invoke.ContextRequirement {
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "basic":
			return []invoke.ContextRequirement{{Type: "auth.basic"}}
		case "bearer":
			return []invoke.ContextRequirement{{Type: "auth.bearer"}}
		default:
			// digest, negotiate, etc.: not a family this SDK resolves
			// itself, but SURFACED (R2.c ruling), not dropped. A missing
			// scheme value degrades to the bare family, never a trailing
			// dot (TS parity).
			return []invoke.ContextRequirement{{
				Type:        httpRequirementType(s.Scheme),
				Description: s.Description,
			}}
		}
	case "apiKey":
		return []invoke.ContextRequirement{{Type: "auth.apiKey"}}
	case "oauth2":
		return oauth2Requirements(s, baseURL, requiredScopes)
	case "openIdConnect":
		// OpenID Connect resolves to an OAuth2 access token; the discovery URL
		// lets a resolver fetch the authorize/token endpoints. No flow is
		// selected here (openIdConnect has no `flows` object), so this
		// requirement carries no grantType (R2.b ruling).
		req := invoke.ContextRequirement{Type: "auth.oauth2", Extra: map[string]any{
			"scopes": append([]string{}, requiredScopes...),
		}}
		if s.OpenIdConnectUrl != "" {
			req.Extra["openIdConnectUrl"] = absolutizeURL(s.OpenIdConnectUrl, baseURL)
		}
		return []invoke.ContextRequirement{req}
	default:
		// Any other artifact type this SDK doesn't itself resolve (e.g.
		// "mutualTLS"): surfaced verbatim (R2.c ruling) rather than dropped.
		return []invoke.ContextRequirement{{
			Type:        "auth." + s.Type,
			Description: s.Description,
		}}
	}
}

// httpRequirementType derives the surfaced type for an http scheme this SDK
// does not itself resolve: "auth.http.<scheme>" (lowercased), degrading to
// the bare "auth.http" when the artifact omits the scheme value (TS parity —
// never a trailing dot).
func httpRequirementType(scheme string) string {
	if scheme == "" {
		return "auth.http"
	}
	return "auth.http." + strings.ToLower(scheme)
}

// oauth2Requirements builds one auth.oauth2 requirement per usable declared
// flow. The scopes field carries the Security Requirement Object's required
// scopes, never the scheme's complete advertised catalogue. Canonical flow
// order is deterministic only; it does not invent a preference. When no
// declared flow can grant the required scopes, a bare scoped requirement
// remains discoverable so an already-acquired token may still satisfy it.
func oauth2Requirements(s *openapi3.SecurityScheme, baseURL string, requiredScopes []string) []invoke.ContextRequirement {
	type candidate struct {
		grantType string
		flow      *openapi3.OAuthFlow
	}
	var flows *openapi3.OAuthFlows
	if s != nil {
		flows = s.Flows
	}
	var candidates []candidate
	if flows != nil {
		candidates = []candidate{
			{grantType: "authorization_code", flow: flows.AuthorizationCode},
			{grantType: "implicit", flow: flows.Implicit},
			{grantType: "password", flow: flows.Password},
			{grantType: "client_credentials", flow: flows.ClientCredentials},
		}
	}
	var requirements []invoke.ContextRequirement
	for _, candidate := range candidates {
		if candidate.flow == nil || !oauthFlowUsable(candidate.grantType, candidate.flow, requiredScopes) {
			continue
		}
		extra := map[string]any{
			"grantType": candidate.grantType,
			"scopes":    append([]string{}, requiredScopes...),
		}
		if candidate.flow.AuthorizationURL != "" {
			extra["authorizeUrl"] = absolutizeURL(candidate.flow.AuthorizationURL, baseURL)
		}
		if candidate.flow.TokenURL != "" {
			extra["tokenUrl"] = absolutizeURL(candidate.flow.TokenURL, baseURL)
		}
		requirements = append(requirements, invoke.ContextRequirement{Type: "auth.oauth2", Extra: extra})
	}
	if len(requirements) == 0 {
		return []invoke.ContextRequirement{{
			Type:  "auth.oauth2",
			Extra: map[string]any{"scopes": append([]string{}, requiredScopes...)},
		}}
	}
	return requirements
}

func oauthFlowUsable(grantType string, flow *openapi3.OAuthFlow, requiredScopes []string) bool {
	switch grantType {
	case "authorization_code":
		if flow.AuthorizationURL == "" || flow.TokenURL == "" {
			return false
		}
	case "implicit":
		if flow.AuthorizationURL == "" {
			return false
		}
	case "password", "client_credentials":
		if flow.TokenURL == "" {
			return false
		}
	}
	for _, scope := range requiredScopes {
		if _, ok := flow.Scopes[scope]; !ok {
			return false
		}
	}
	return true
}

// absolutizeURL resolves a possibly-relative URL against the server base;
// absolute URLs pass through unchanged.
func absolutizeURL(ref, baseURL string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if u.IsAbs() {
		return ref
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

// validRefMethods are the OAS's HTTP method keys, lowercase exactly as the
// artifact spells them (OAPI-D-03). Acceptance never case-folds.
var validRefMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// parseSelector parses a binding selector per OAPI-D-03: a JSON Pointer of the exact
// form `#/paths/<escaped-path>/<method>` addressing an operation object. The
// path segment carries RFC 6901 escaping ("/" → "~1", "~" → "~0"), and the
// method is lowercase exactly as the artifact spells it — an uppercase
// method is non-conformant and refused, never case-folded.
func parseSelector(selector string) (path string, method string, err error) {
	const prefix = "#/paths/"
	if !strings.HasPrefix(selector, prefix) {
		return "", "", fmt.Errorf("selector %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method> (OAPI-D-03)", selector)
	}
	parts := strings.Split(selector[len(prefix):], "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("selector %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method>: the path segment carries RFC 6901 escaping (\"/\" → \"~1\") (OAPI-D-03)", selector)
	}
	escapedPath, method := parts[0], parts[1]
	if !validRefMethods[method] {
		if validRefMethods[strings.ToLower(method)] {
			return "", "", fmt.Errorf("selector %q: method %q must be lowercase exactly as the artifact spells it (OAPI-D-03)", selector, method)
		}
		return "", "", fmt.Errorf("invalid HTTP method %q in selector", method)
	}

	// RFC 6901 unescaping, in order: ~1 first, then ~0.
	path = strings.ReplaceAll(escapedPath, "~1", "/")
	path = strings.ReplaceAll(path, "~0", "~")
	return path, method, nil
}

func hasRequestBody(op *openapi3.Operation) bool {
	return op.RequestBody != nil && op.RequestBody.Value != nil
}

// encodePathValue percent-encodes one path parameter value with exactly
// JavaScript's encodeURIComponent byte set (every byte except ALPHA / DIGIT /
// "-" "_" "." "!" "~" "*" "'" "(" ")" is %XX-escaped, UTF-8 bytewise), so the
// Go and TS invokers substitute byte-identical URL path segments.
// url.PathEscape is NOT equivalent: it passes sub-delims like "$&+,:;=@"
// through unescaped.
func encodePathValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Credentials and channel assembly (§9.6: OAPI-P-09 wire application,
// OAPI-P-10 channel assembly)
// ---------------------------------------------------------------------------

// credentialPlacement is one credential's wire application: which channel
// it rides (header, query, or cookie) under which name.
type credentialPlacement struct {
	channel string
	name    string
	value   string
}

// selectCredentialPlacements chooses one complete, satisfiable OpenAPI
// Security Requirement Object in artifact order. Security Requirement Objects
// are alternatives (OR); schemes inside one object are conjunctive (AND).
// Consequently credentials are never pooled across alternatives. A
// collision makes that alternative unusable but does not prevent selecting a
// later complete alternative. No declared security means no credential wire
// application, even when unrelated credentials exist in context.
func selectCredentialPlacements(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string, params openapi3.Parameters, populated map[string]map[string]bool) ([]credentialPlacement, error) {
	for _, plan := range viableSecurityPlans(doc, op, baseURL, params) {
		if len(plan.context.Requirements) == 0 {
			return nil, nil // this complete alternative explicitly allows anonymous access
		}
		if !invoke.ContextSatisfies(bindCtx, &invoke.ContextRequiredDetails{
			Alternatives: []invoke.ContextAlternative{plan.context},
		}) {
			continue
		}
		placements := credentialValues(plan, bindCtx)
		if err := checkCredentialCollisions(placements, params, populated); err != nil {
			return nil, err
		}
		return placements, nil
	}
	// requiredContext prevents dispatch when no alternative is satisfied. This
	// path is therefore defensive for invalid or extension-only artifacts; it
	// must still not leak unrelated credentials onto the wire.
	return nil, nil
}

// credentialValues applies every scheme in exactly one selected security
// plan. It deliberately retains duplicate wire destinations so collision
// checks can refuse an impossible AND rather than silently dropping one.
func credentialValues(plan securityPlan, bindCtx map[string]any) []credentialPlacement {
	placements := make([]credentialPlacement, 0, len(plan.schemes))
	for _, named := range plan.schemes {
		s := named.scheme
		switch s.Type {
		case "apiKey":
			val := invoke.ContextAPIKeyFor(bindCtx, named.name)
			if val == "" {
				continue
			}
			switch s.In {
			case "header", "query", "cookie":
				placements = append(placements, credentialPlacement{channel: s.In, name: s.Name, value: val})
			}
		case "http":
			switch strings.ToLower(s.Scheme) {
			case "bearer":
				if token := invoke.ContextBearerTokenFor(bindCtx, named.name); token != "" {
					placements = append(placements, credentialPlacement{channel: "header", name: "Authorization", value: "Bearer " + token})
				}
			case "basic":
				if u, p, ok := invoke.ContextBasicAuthFor(bindCtx, named.name); ok {
					placements = append(placements, credentialPlacement{channel: "header", name: "Authorization", value: "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))})
				}
			}
		case "oauth2", "openIdConnect":
			token := invoke.ContextAccessTokenFor(bindCtx, named.name)
			if token == "" {
				token = invoke.ContextBearerToken(bindCtx)
			}
			if token != "" {
				placements = append(placements, credentialPlacement{channel: "header", name: "Authorization", value: "Bearer " + token})
			}
		}
	}
	return placements
}

// credentialDestinations is the artifact-only wire footprint of one security
// plan. It lets context negotiation discard OAPI-P-10-colliding alternatives
// before credentials exist, so an unusable alternative is never challenged.
func credentialDestinations(plan securityPlan) []credentialPlacement {
	placements := make([]credentialPlacement, 0, len(plan.schemes))
	for _, named := range plan.schemes {
		s := named.scheme
		switch s.Type {
		case "apiKey":
			switch s.In {
			case "header", "query", "cookie":
				if s.Name != "" {
					placements = append(placements, credentialPlacement{channel: s.In, name: s.Name})
				}
			}
		case "http":
			switch strings.ToLower(s.Scheme) {
			case "basic", "bearer":
				placements = append(placements, credentialPlacement{channel: "header", name: "Authorization"})
			}
		case "oauth2", "openIdConnect":
			placements = append(placements, credentialPlacement{channel: "header", name: "Authorization"})
		}
	}
	return placements
}

// checkCredentialCollisions enforces the OAPI-P-10 refusal: a name collision
// between a credential and a caller-populated declared parameter on the same
// channel is refused before dispatch — loud, never a silent overwrite in
// either direction. Header names compare case-insensitively.
func checkCredentialCollisions(placements []credentialPlacement, params openapi3.Parameters, populated map[string]map[string]bool) error {
	declared := map[string]map[string]bool{"header": {}, "query": {}, "cookie": {}, "path": {}}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		name := ref.Value.Name
		if ref.Value.In == openapi3.ParameterInHeader {
			name = http.CanonicalHeaderKey(name)
		}
		declared[ref.Value.In][name] = true
	}
	ownedHeaders := map[string]bool{"Host": true, "Content-Length": true, "Content-Type": true, "Accept": true}
	hasRawCookieOwner := populated != nil && populated[openapi3.ParameterInHeader]["Cookie"]
	hasStructuredCookieOwner := populated != nil && len(populated[openapi3.ParameterInCookie]) > 0
	for _, placement := range placements {
		if placement.channel == "header" && http.CanonicalHeaderKey(placement.name) == "Cookie" {
			hasRawCookieOwner = true
		}
		if placement.channel == "cookie" {
			hasStructuredCookieOwner = true
		}
	}
	if hasRawCookieOwner && hasStructuredCookieOwner {
		return fmt.Errorf("raw Cookie header source collides with structured cookie assembly (OAPI-P-10)")
	}
	seen := map[string]bool{}
	for _, pl := range placements {
		name := pl.name
		if pl.channel == "header" {
			name = http.CanonicalHeaderKey(name)
			if ownedHeaders[name] {
				return fmt.Errorf("credential %q collides with processor-owned request field %s (OAPI-P-10)", pl.name, name)
			}
		}
		if declared[pl.channel][name] || populated[pl.channel][name] {
			return fmt.Errorf("credential %q collides with an effective %s parameter of the same name (OAPI-P-10: refused before dispatch, never a silent overwrite in either direction)", pl.name, pl.channel)
		}
		key := pl.channel + "\x00" + name
		if seen[key] {
			return fmt.Errorf("two credentials collide at %s %q (OAPI-P-10)", pl.channel, pl.name)
		}
		seen[key] = true
	}
	return nil
}

// contextChannelCollision keeps the context transport-hint channel from
// overwriting the structured Cookie assembly. Cookie is one HTTP field with
// two intentionally distinct caller surfaces (`headers.Cookie` and
// `cookies`); ambiguous ownership is refused before dispatch.
func contextChannelCollision(bindCtx map[string]any, params openapi3.Parameters, placements []credentialPlacement) error {
	rawContextCookie := false
	for name := range invoke.ContextHeaders(bindCtx) {
		if http.CanonicalHeaderKey(name) == "Cookie" {
			rawContextCookie = true
			break
		}
	}
	hasRawCookieOwner := false
	hasStructuredCookie := len(invoke.ContextCookies(bindCtx)) > 0
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		if ref.Value.In == openapi3.ParameterInHeader && http.CanonicalHeaderKey(ref.Value.Name) == "Cookie" {
			hasRawCookieOwner = true
		}
		if ref.Value.In == openapi3.ParameterInCookie {
			hasStructuredCookie = true
		}
	}
	for _, placement := range placements {
		if placement.channel == "header" && http.CanonicalHeaderKey(placement.name) == "Cookie" {
			hasRawCookieOwner = true
		}
		if placement.channel == "cookie" {
			hasStructuredCookie = true
		}
	}
	if rawContextCookie && (hasRawCookieOwner || hasStructuredCookie) {
		return fmt.Errorf("raw Cookie context header collides with another raw or structured cookie source (OAPI-P-10: refused before dispatch, never a silent overwrite)")
	}
	if hasRawCookieOwner && len(invoke.ContextCookies(bindCtx)) > 0 {
		return fmt.Errorf("raw Cookie header source collides with structured context cookies (OAPI-P-10: refused before dispatch, never a silent overwrite)")
	}
	return nil
}
