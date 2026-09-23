package invoke

import (
	"context"
	"strings"

	"github.com/openbindings/openbindings-go/schemavalidate"
)

// ---------------------------------------------------------------------------
// Store-backed context resolver
// ---------------------------------------------------------------------------

// requirementFields maps standard requirement families to the well-known
// context field that satisfies them, per the binding-invoker contract's
// BindingContext shape (interfaces/binding-invoker).
var requirementFields = map[string]string{
	"auth.bearer": "bearerToken",
	"auth.apiKey": "apiKey",
	"auth.basic":  "basic",
	"auth.oauth2": "accessToken",
}

// requirementFamilyFields maps a requirement family to every context field that
// belongs to it. requirementFields names only the field whose presence gates
// satisfaction; this names the whole family, so scoping can admit (for example)
// an oauth2 refresh token alongside its access token. Fields outside the
// selected requirement family are not admitted: this helper cannot infer
// whether an arbitrary header, cookie, environment value, metadata entry, or
// configuration point is sensitive.
//
// These fields are the unnamed, single-scheme conveniences. A named auth.*
// requirement is satisfied and scoped through credentials[name] first;
// auth.apiKey also accepts historical apiKeys[name]. Named maps are never
// admitted wholesale, which would leak another scheme's credential.
var requirementFamilyFields = map[string][]string{
	"auth.bearer": {"bearerToken"},
	"auth.apiKey": {"apiKey"},
	"auth.basic":  {"basic"},
	// `bearerToken` belongs here because requirementSatisfied's oauth2 arm
	// accepts one. Without it the two rules in this file contradicted each
	// other: the challenge validated against a stored bearer token and then
	// ScopeContext admitted nothing, so the caller supplied exactly what the
	// error asked for, the scope gate dropped it, and the invoker re-challenged
	// forever. A rule that says a value satisfies a requirement has to let that
	// value through.
	"auth.oauth2": {"accessToken", "bearerToken", "refreshToken", "clientSecret"},
}

// credentialFieldNames are the BindingContext fields RedactContext always
// treats as secret credential material. Other fields may also be sensitive
// according to their binding specification or application meaning; this list
// is not a claim that unlisted fields are safe. Listed independently of
// requirementFamilyFields because 'apiKeys' is a credential field but is
// never admitted wholesale by that map's generic per-family copy — it is
// scoped per name by admitRequirement.
var credentialFieldNames = map[string]bool{
	"bearerToken":  true,
	"apiKey":       true,
	"apiKeys":      true,
	"basic":        true,
	"accessToken":  true,
	"refreshToken": true,
	"clientSecret": true,
	"credentials":  true,
}

// isCredentialField reports whether a context key names one of the standard
// credential fields. It does not classify unrecognized fields as non-secret.
func isCredentialField(key string) bool {
	return credentialFieldNames[key]
}

// credentialValueSatisfies is shared by satisfaction and scoped admission.
func credentialValueSatisfies(value any, kind string) bool {
	switch kind {
	case "auth.bearer", "auth.apiKey":
		token, ok := value.(string)
		return ok && token != ""
	case "auth.basic":
		credential, ok := value.(map[string]any)
		if !ok {
			return false
		}
		username, hasUsername := credential["username"].(string)
		password, hasPassword := credential["password"].(string)
		return hasUsername && hasPassword && (username != "" || password != "")
	case "auth.oauth2":
		credential, ok := value.(map[string]any)
		if !ok {
			return false
		}
		token, ok := credential["accessToken"].(string)
		return ok && token != ""
	default:
		return false
	}
}

// requirementSatisfied reports whether ctx resolves one requirement. A named
// auth.* requirement checks context.credentials[req.Name] first. Named
// auth.apiKey additionally accepts historical context.apiKeys[req.Name]. A
// flat convenience may satisfy a named requirement only when that use is
// unambiguous across the complete challenge. This is the same priority the
// credential-application helpers use.
//
// Every other "auth.*" type is UNMAPPED: a scheme an invoker surfaced from
// the artifact but has no resolver for (e.g. "auth.http.digest",
// "auth.mutualTLS", "auth.scramSha256" — the R2.c ruling). It is always
// unsatisfiable here — never checked against a field named after the type
// itself — because rule 10 of the binding-invoker contract makes an
// unrecognized requirement type unsatisfiable by a runtime with no resolver
// for it, and this layer must not invent a satisfaction convention for the
// reserved "auth." namespace.
//
// config.value is satisfied by the named point in context.configuration.
// Other non-"auth." extension families fall back to a context field named
// after their type; the extension family owns that convention.
func requirementSatisfied(ctx map[string]any, req ContextRequirement, allowFlatNamedCredential bool) bool {
	if credentialValueSatisfies(ContextNamedCredential(ctx, req.Name), req.Type) {
		return true
	}
	switch req.Type {
	case "auth.bearer":
		return allowFlatNamedCredential && ContextBearerToken(ctx) != ""
	case "auth.apiKey":
		if req.Name != "" {
			if keys, ok := ctx["apiKeys"].(map[string]any); ok {
				if value, ok := keys[req.Name].(string); ok && value != "" {
					return true
				}
			}
		}
		return allowFlatNamedCredential && ContextAPIKey(ctx) != ""
	case "auth.basic":
		_, _, ok := ContextBasicAuth(ctx)
		return allowFlatNamedCredential && ok
	case "auth.oauth2":
		// Flat OAuth credentials can use accessToken or bearerToken.
		return allowFlatNamedCredential && (ContextString(ctx, "accessToken") != "" || ContextBearerToken(ctx) != "")
	}
	if req.Type == "config.value" {
		point, _ := req.Extra["point"].(string)
		path, pathPresent := req.Extra["path"].(string)
		configuration, _ := ctx["configuration"].(map[string]any)
		v, present := configuration[point]
		if point == "" || !pathPresent || !present {
			return false
		}
		selected, selectedPresent := configurationValueAt(v, path)
		if !selectedPresent || selected == nil || selected == "" {
			return false
		}
		// When the requirement carries an engine-asserted schema, presence is
		// not enough: the selected value must validate against it (an `enum`
		// member is a closed admissible set). A schema that is present but
		// not an object is an invalid carriage (ValidContextRequiredDetails
		// refuses it); treat it conservatively as unsatisfiable here.
		if schemaRaw, schemaPresent := req.Extra["schema"]; schemaPresent {
			schema, ok := schemaRaw.(map[string]any)
			if !ok {
				return false
			}
			if schemavalidate.Validate(selected, schema) != nil {
				return false
			}
		}
		return true
	}
	field, mapped := requirementFields[req.Type]
	if !mapped {
		if strings.HasPrefix(req.Type, "auth.") {
			return false
		}
		field = req.Type
	}
	v, present := ctx[field]
	return present && v != nil && v != ""
}

// MatchContextAlternative returns the index of the first alternative the
// context satisfies, or ok=false when none matches (including nil details).
// Matching considers the entire challenge when deciding whether flat
// credentials unambiguously identify a scheme. Callers applying a resolution
// should retain this index rather than re-evaluating alternatives in isolation.
func MatchContextAlternative(ctx map[string]any, details *ContextRequiredDetails) (index int, ok bool) {
	if details == nil {
		return 0, false
	}
	return matchContextAlternative(ctx, details, details.Alternatives)
}

func matchContextAlternative(ctx map[string]any, details *ContextRequiredDetails, alternatives []ContextAlternative) (int, bool) {
	for index, alt := range alternatives {
		ok := len(alt.Requirements) > 0
		for _, req := range alt.Requirements {
			if !requirementSatisfied(ctx, req, flatCredentialIsUnambiguous(details, req)) {
				ok = false
				break
			}
		}
		if ok {
			return index, true
		}
	}
	return 0, false
}

// ContextSatisfies reports whether the context can satisfy every requirement
// of at least one alternative of the challenge. Nil details need no context.
// config.value paths currently traverse object members only: arrays may be
// selected as whole values, but array-element paths are not resolved here.
func ContextSatisfies(ctx map[string]any, details *ContextRequiredDetails) bool {
	if details == nil {
		return true
	}
	_, ok := MatchContextAlternative(ctx, details)
	return ok
}

func flatCredentialIsUnambiguous(details *ContextRequiredDetails, requirement ContextRequirement) bool {
	identities := map[string]struct{}{}
	unnamed := 0
	for _, alternative := range details.Alternatives {
		for _, candidate := range alternative.Requirements {
			if candidate.Type != requirement.Type {
				continue
			}
			if candidate.Name == "" {
				unnamed++
			} else {
				identities[candidate.Name] = struct{}{}
			}
		}
	}
	return len(identities)+unnamed == 1
}

// ScopeContext returns the least-privilege subset of a stored context for a
// challenge (the published binding-invoker contract). A CONTEXT_REQUIRED
// challenge is a scope,
// not a hint: the invoker receives only what the first satisfied alternative
// declares. Standard credential requirements admit their corresponding
// credential family; config.value admits only its named configuration point;
// an extension requirement admits its type-named field. No other stored field
// passes by default because this generic helper cannot determine its
// sensitivity or relevance. With a nil challenge there is nothing to scope, so
// the full context is returned (shallow-copied). Returns nil for nil input.
// Scoping does not authorize storage reuse or persistence: the resolver must
// enforce durability before supplying stored values. This helper understands
// the standard credential families, config.value and type-named non-auth
// extensions; unrecognized auth.* requirements are not satisfiable here.
// config.value paths traverse object members only. Arrays can be scoped as
// whole values, but array-element paths need application-specific handling.
func ScopeContext(stored map[string]any, details *ContextRequiredDetails) map[string]any {
	if stored == nil {
		return nil
	}
	if details == nil {
		out := make(map[string]any, len(stored))
		for k, v := range stored {
			out[k] = v
		}
		return out
	}
	return scopeContextAlternatives(stored, details, details.Alternatives)
}

// candidates may restrict eligibility, but matching retains the complete
// challenge's credential-identity rules (including non-durable alternatives).
func scopeContextAlternatives(stored map[string]any, details *ContextRequiredDetails, candidates []ContextAlternative) map[string]any {
	out := make(map[string]any)
	index, ok := matchContextAlternative(stored, details, candidates)
	if ok {
		for _, req := range candidates[index].Requirements {
			admitRequirement(out, stored, req)
		}
	}
	return out
}

// admitRequirement copies into out the stored credential field(s) that
// satisfy one already-satisfied requirement. A named auth.* requirement
// admits only its single credentials[name] entry; named auth.apiKey may use
// its historical apiKeys[name] entry. Neither named map is admitted wholesale,
// so another alternative's credential cannot cross the scope — per the
// binding-invoker contract's least-privilege rule (§ ContextRequiredDetails:
// "provisions only the context that satisfies the one selected
// alternative"). Falls back to the flat credential family when the named
// entry is unusable, following the same validity checks as requirementSatisfied.
// An unnamed standard requirement admits its flat family fields;
// an unmapped extension admits the single field named by req.Type.
func admitRequirement(out, stored map[string]any, req ContextRequirement) {
	if req.Type == "auth.apiKey" && req.Name != "" {
		if credentials, ok := stored["credentials"].(map[string]any); ok {
			if value := credentials[req.Name]; credentialValueSatisfies(value, req.Type) {
				scoped, _ := out["credentials"].(map[string]any)
				if scoped == nil {
					scoped = map[string]any{}
					out["credentials"] = scoped
				}
				scoped[req.Name] = value
				return
			}
		}
		if keys, ok := stored["apiKeys"].(map[string]any); ok {
			if v, ok := keys[req.Name].(string); ok && v != "" {
				scoped, _ := out["apiKeys"].(map[string]any)
				if scoped == nil {
					scoped = map[string]any{}
					out["apiKeys"] = scoped
				}
				scoped[req.Name] = v
				return
			}
		}
		if v, present := stored["apiKey"]; present {
			out["apiKey"] = v
		}
		return
	}
	if req.Type == "config.value" {
		point, _ := req.Extra["point"].(string)
		path, pathPresent := req.Extra["path"].(string)
		configuration, _ := stored["configuration"].(map[string]any)
		if point == "" || !pathPresent {
			return
		}
		value, present := configuration[point]
		if !present {
			return
		}
		scoped, _ := out["configuration"].(map[string]any)
		if scoped == nil {
			scoped = map[string]any{}
			out["configuration"] = scoped
		}
		selected, selectedPresent := configurationValueAt(value, path)
		if !selectedPresent {
			return
		}
		if path == "" {
			scoped[point] = value
			return
		}
		pointOut, _ := scoped[point].(map[string]any)
		if pointOut == nil {
			pointOut = map[string]any{}
			scoped[point] = pointOut
		}
		mergeConfigurationFragment(pointOut, configurationFragment(path, selected))
		return
	}
	if req.Name != "" && strings.HasPrefix(req.Type, "auth.") {
		if credentials, ok := stored["credentials"].(map[string]any); ok {
			if value := credentials[req.Name]; credentialValueSatisfies(value, req.Type) {
				scoped, _ := out["credentials"].(map[string]any)
				if scoped == nil {
					scoped = map[string]any{}
					out["credentials"] = scoped
				}
				scoped[req.Name] = value
				return
			}
		}
	}
	fields, ok := requirementFamilyFields[req.Type]
	if !ok {
		fields = []string{req.Type}
	}
	for _, f := range fields {
		if v, present := stored[f]; present {
			out[f] = v
		}
	}
}

func configurationPointerTokens(path string) ([]string, bool) {
	if !validConfigurationPointer(path) {
		return nil, false
	}
	if path == "" {
		return []string{}, true
	}
	parts := strings.Split(path[1:], "/")
	for index, token := range parts {
		parts[index] = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
	}
	return parts, true
}

func configurationValueAt(root any, path string) (any, bool) {
	tokens, ok := configurationPointerTokens(path)
	if !ok {
		return nil, false
	}
	current := root
	if len(tokens) == 0 {
		return current, true
	}
	for _, token := range tokens {
		record, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = record[token]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func configurationFragment(path string, value any) map[string]any {
	tokens, _ := configurationPointerTokens(path)
	var fragment any = value
	for index := len(tokens) - 1; index >= 0; index-- {
		fragment = map[string]any{tokens[index]: fragment}
	}
	result, _ := fragment.(map[string]any)
	return result
}

func mergeConfigurationFragment(left, right map[string]any) {
	for key, value := range right {
		prior, priorOK := left[key].(map[string]any)
		incoming, incomingOK := value.(map[string]any)
		if priorOK && incomingOK {
			mergeConfigurationFragment(prior, incoming)
			continue
		}
		left[key] = value
	}
}

// StoreContextResolver builds a read-only ContextResolver backed by a
// ContextStore: it resolves the binding-invoker contract's CONTEXT_REQUIRED
// challenges from stored BindingContext entries (the well-known fields live
// in that contract, interfaces/binding-invoker).
// It derives the store key from the challenge's target per alternative
// (see the keying rule below), returns the least-privilege subset of the
// stored context (ScopeContext) when it satisfies one of the challenge's
// alternatives,
// and declines otherwise — at which point the challenge surfaces to the caller
// unchanged. A CONTEXT_REQUIRED challenge is a scope, not a hint: the resolver
// returns only the context fields the satisfied alternative names. It does
// not classify or forward arbitrary stored fields.
//
// Keying rule (context-scope model, ratified 2026-08-19): the target is an
// engine-asserted, opaque scope, and the requirement family decides how it
// keys the store. An alternative consisting solely of config.value
// requirements is artifact-bound configuration: it fetches under the EXACT
// asserted target string, verbatim. Endpoint normalization would conflate a
// canonicalized source URL with its origin — many artifacts can live on one
// host, and one artifact's configuration answers must not resolve another's
// challenge. An alternative carrying any credential-family requirement keeps
// the endpoint-normalized convention (NormalizeEndpoint): a credential's
// natural scope is the destination origin.
//
// A stored entry that does NOT satisfy the challenge (wrong field name,
// empty value) is a decline like any other: the challenge surfaces with its
// structured requirement data intact.
//
// This generic helper treats the co-located invoker that produced Target as
// inside the application's trust boundary. An untrusted remote or delegate
// assertion must be independently validated before this resolver can release
// reusable stored secrets.
//
// Apps that resolve interactively (prompt, browser redirect, keychain)
// supply their own resolver and MAY persist what they obtain for durable
// requirements under the target-derived key; non-durable context MUST NOT be
// persisted.
func StoreContextResolver(store ContextStore) ContextResolver {
	return func(ctx context.Context, details *ContextRequiredDetails) (map[string]any, error) {
		if details == nil {
			return nil, nil
		}
		// Visit eligible alternatives in their original order. Cache reads by
		// key without grouping candidates, which would change that order.
		cached := map[string]map[string]any{}
		for _, alt := range details.Alternatives {
			whollyDurable := len(alt.Requirements) > 0
			for _, req := range alt.Requirements {
				if req.Durable == nil || !*req.Durable {
					whollyDurable = false
					break
				}
			}
			if !whollyDurable {
				continue
			}
			key := NormalizeEndpoint(details.Target)
			if configValueOnlyAlternative(alt) {
				key = details.Target
			}
			if key == "" {
				continue // No safe reusable-storage key; an interactive resolver may help.
			}
			stored, read := cached[key]
			if !read {
				var err error
				stored, err = store.Get(ctx, key)
				if err != nil {
					return nil, err
				}
				cached[key] = stored
			}
			scoped := scopeContextAlternatives(stored, details, []ContextAlternative{alt})
			if len(scoped) > 0 {
				return scoped, nil
			}
		}
		return nil, nil
	}
}

// configValueOnlyAlternative reports whether every requirement of the
// alternative is config.value — an artifact-bound configuration alternative,
// which the keying rule files and fetches under the exact asserted target
// rather than the normalized endpoint.
func configValueOnlyAlternative(alt ContextAlternative) bool {
	if len(alt.Requirements) == 0 {
		return false
	}
	for _, req := range alt.Requirements {
		if req.Type != "config.value" {
			return false
		}
	}
	return true
}
