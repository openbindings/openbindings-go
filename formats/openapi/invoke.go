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
	if !onlyRangePlans(plans) {
		return nil, nil
	}
	durable := true
	requirement := invoke.NewConfigValueRequirement(
		"requestMedia", "",
		"select a concrete request media type admitted by the OpenAPI declaration",
		nil, &durable,
	)
	return &invoke.ContextRequiredDetails{
		Alternatives: []invoke.ContextAlternative{{Requirements: []invoke.ContextRequirement{requirement}}},
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
			requirements = append(requirements, b.Requirements...)
			merged.Alternatives = append(merged.Alternatives, invoke.ContextAlternative{Requirements: requirements})
		}
	}
	return merged
}

// BuiltinClassify is the openapi builtin result classifier (OAPI-P-08):
// success iff the final HTTP status is 2xx (declared responses refine
// application failure DATA only, never classification).
func BuiltinClassify(_ invoke.InvokeSite, raw invoke.RawResult) (bool, error) {
	return raw.Status != nil && *raw.Status >= 200 && *raw.Status < 300, nil
}

// decodeByContentType returns the builtin decoder implementing the header
// rule (OAPI-P-07): strict JSON for application/json and +json suffixes (a
// declared-JSON body that fails to parse is a lying server — a loud
// ErrCodeResponseError, never a silent string); the text lane otherwise —
// bytes become a string per the header's charset parameter, defaulting to
// UTF-8, with invalid sequences a loud decode error. An empty body (204
// included) yields null.
func decodeByContentType(contentType string) invoke.OutputDecoder {
	return decodeByContentTypeFor(contentType, BindingSpec)
}

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

// decodeTextLane decodes response bytes as text per the Content-Type
// header's charset parameter, defaulting to UTF-8 (OAPI-P-07). Invalid
// sequences, and charsets this implementation cannot decode, are loud
// decode errors — a consumer needing another charset overrides at the
// decode configuration point.
func decodeTextLane(contentType string, body []byte) (any, error) {
	return decodeTextLaneFor(contentType, body, BindingSpec)
}

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

// isJSONContentType reports whether a Content-Type header declares a JSON
// body: application/json or any +json structured-suffix type. Absent or
// unparseable headers are NOT JSON (the text lane) — never sniffed.
func isJSONContentType(contentType string) bool {
	return isJSONContentTypeFor(contentType, BindingSpec)
}

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
	context invoke.ContextAlternative
	schemes []namedSecurityScheme
}

// securityConfigurationError refuses every undefined SecurityScheme name.
// An unresolved name is invalid OpenAPI source configuration, not an
// anonymous or skippable OR alternative: dropping it would silently weaken
// the API author's security declaration and diverge from the TS processor.
func securityConfigurationError(doc *openapi3.T, op *openapi3.Operation) error {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	missing := map[string]bool{}
	for _, requirement := range *requirements {
		for name := range requirement {
			if doc.Components == nil || doc.Components.SecuritySchemes == nil {
				missing[name] = true
				continue
			}
			ref, found := doc.Components.SecuritySchemes[name]
			if !found || ref == nil || ref.Value == nil {
				missing[name] = true
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	names := make([]string, 0, len(missing))
	for name := range missing {
		names = append(names, name)
	}
	sort.Strings(names)
	suffix := ""
	if len(names) > 1 {
		suffix = "s"
	}
	return fmt.Errorf("OpenAPI security requirement references undefined security scheme%s: %s", suffix, strings.Join(names, ", "))
}

// securityPlans expands the artifact's OR-of-AND Security Requirement
// Objects without flattening them. An OAuth scheme may contribute multiple
// usable declared flows, so one authored AND-set expands to the Cartesian
// product of its schemes' runtime alternatives. Every expanded plan still
// represents exactly one complete artifact-declared requirement object.
func securityPlans(doc *openapi3.T, op *openapi3.Operation, baseURL string) []securityPlan {
	requirements := effectiveSecurityRequirements(doc, op)
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}
	var plans []securityPlan
	for _, secReq := range *requirements {
		if len(secReq) == 0 {
			plans = append(plans, securityPlan{})
			continue
		}
		expanded := []securityPlan{{}}
		expressible := true
		names := make([]string, 0, len(secReq))
		for schemeName := range secReq {
			names = append(names, schemeName)
		}
		sort.Strings(names)
		for _, schemeName := range names {
			if doc.Components == nil || doc.Components.SecuritySchemes == nil {
				expressible = false
				break
			}
			ref, ok := doc.Components.SecuritySchemes[schemeName]
			if !ok || ref.Value == nil {
				expressible = false
				break
			}
			requiredScopes := append([]string(nil), secReq[schemeName]...)
			options := schemeRequirements(ref.Value, baseURL, requiredScopes)
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
					if ref.Value.Description != "" {
						option.Description = ref.Value.Description
					}
					reqs := append([]invoke.ContextRequirement(nil), plan.context.Requirements...)
					reqs = append(reqs, option)
					schemes := append([]namedSecurityScheme(nil), plan.schemes...)
					schemes = append(schemes, namedSecurityScheme{name: schemeName, scheme: ref.Value})
					next = append(next, securityPlan{
						context: invoke.ContextAlternative{Requirements: reqs},
						schemes: schemes,
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

func viableSecurityPlans(doc *openapi3.T, op *openapi3.Operation, baseURL string, params openapi3.Parameters) []securityPlan {
	plans := securityPlans(doc, op, baseURL)
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

// parseRef parses a binding ref per OAPI-D-03: a JSON Pointer of the exact
// form `#/paths/<escaped-path>/<method>` addressing an operation object. The
// path segment carries RFC 6901 escaping ("/" → "~1", "~" → "~0"), and the
// method is lowercase exactly as the artifact spells it — an uppercase
// method is non-conformant and refused, never case-folded.
func parseRef(ref string) (path string, method string, err error) {
	const prefix = "#/paths/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", fmt.Errorf("ref %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method> (OAPI-D-03)", ref)
	}
	parts := strings.Split(ref[len(prefix):], "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("ref %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method>: the path segment carries RFC 6901 escaping (\"/\" → \"~1\") (OAPI-D-03)", ref)
	}
	escapedPath, method := parts[0], parts[1]
	if !validRefMethods[method] {
		if validRefMethods[strings.ToLower(method)] {
			return "", "", fmt.Errorf("ref %q: method %q must be lowercase exactly as the artifact spells it (OAPI-D-03)", ref, method)
		}
		return "", "", fmt.Errorf("invalid HTTP method %q in ref", method)
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
	rawCookieHeader := false
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		name := ref.Value.Name
		if ref.Value.In == openapi3.ParameterInHeader {
			name = http.CanonicalHeaderKey(name)
			if name == "Cookie" {
				rawCookieHeader = true
			}
		}
		declared[ref.Value.In][name] = true
	}
	ownedHeaders := map[string]bool{"Host": true, "Content-Length": true, "Content-Type": true, "Accept": true}
	hasRawCookieOwner := rawCookieHeader
	hasStructuredCookieOwner := len(declared[openapi3.ParameterInCookie]) > 0
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
		if pl.channel == "cookie" && rawCookieHeader {
			return fmt.Errorf("cookie credential %q conflicts with a raw Cookie header parameter (OAPI-P-10: refused before dispatch)", pl.name)
		}
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
