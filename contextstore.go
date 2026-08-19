package openbindings

import (
	"context"
	"strings"
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

// requirementSatisfied reports whether ctx resolves one requirement. A named
// auth.* requirement checks context.credentials[req.Name] first. Named
// auth.apiKey additionally accepts historical context.apiKeys[req.Name]. A
// flat convenience may satisfy a named requirement only when that use is
// unambiguous within the selected alternative. This is the same priority the
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
// ContextAnonymous reports whether the caller has asserted that this
// invocation carries no credentials.
//
// An OpenAPI document's `security` describes what the API ACCEPTS; the server
// decides what it ENFORCES, and the two routinely disagree — public read
// endpoints under a blanket document-level requirement are ordinary. Without
// this, such an operation is unreachable: the challenge cannot be answered
// truthfully (there is no credential) and answering it falsely is worse than
// silence, because a rejected token earns a 401 where sending nothing would
// have been served.
//
// The assertion is the caller supplying, for this invocation, exactly what OAS
// itself spells as `security: []`. It is deliberately an explicit act rather
// than a fallback: guessing that a declared requirement is decorative would
// make every credentialed operation silently attempt an unauthenticated call
// first.
func ContextAnonymous(ctx map[string]any) bool {
	value, ok := ctx["anonymous"].(bool)
	return ok && value
}

func requirementSatisfied(ctx map[string]any, req ContextRequirement, allowFlatNamedCredential bool) bool {
	// Anonymity answers credential requirements and nothing else. A
	// `config.value` point — which server to talk to, which request media to
	// send — is not a credential and has no anonymous reading, so an
	// alternative mixing the two still has to answer the configuration half.
	if ContextAnonymous(ctx) && strings.HasPrefix(req.Type, "auth.") {
		return true
	}
	named := ContextNamedCredential(ctx, req.Name)
	switch req.Type {
	case "auth.bearer":
		if value, ok := named.(string); ok && value != "" {
			return true
		}
		return allowFlatNamedCredential && ContextBearerToken(ctx) != ""
	case "auth.apiKey":
		if value, ok := named.(string); ok && value != "" {
			return true
		}
		if req.Name != "" {
			if keys, ok := ctx["apiKeys"].(map[string]any); ok {
				if value, ok := keys[req.Name].(string); ok && value != "" {
					return true
				}
			}
		}
		return allowFlatNamedCredential && ContextAPIKey(ctx) != ""
	case "auth.basic":
		if value, ok := named.(map[string]any); ok {
			username, hasUsername := value["username"].(string)
			password, hasPassword := value["password"].(string)
			if hasUsername && hasPassword && (username != "" || password != "") {
				return true
			}
		}
		_, _, ok := ContextBasicAuth(ctx)
		return allowFlatNamedCredential && ok
	case "auth.oauth2":
		if value, ok := named.(map[string]any); ok {
			if token, ok := value["accessToken"].(string); ok && token != "" {
				return true
			}
		}
		if !allowFlatNamedCredential {
			return false
		}
		// A flat bearer token counts here because an OAuth2 access token
		// reaches the wire AS a Bearer credential, and the placement side
		// already knows it: credentialValues falls back to the flat bearer
		// token when no accessToken is present. Until these two agreed, an
		// artifact declaring oauth2 was a dead end -- the challenge asked for
		// context, the remedy it printed stored a bearerToken, and the next
		// attempt challenged identically because only `accessToken` counted.
		// OAuth2 is among the most common schemes in real documents, so that
		// disagreement closed off a large share of them.
		return ContextString(ctx, "accessToken") != "" || ContextBearerToken(ctx) != ""
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
		return selectedPresent && selected != nil && selected != ""
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

// ContextSatisfies reports whether the context can satisfy every requirement
// of at least one alternative of the challenge.
func ContextSatisfies(ctx map[string]any, details *ContextRequiredDetails) bool {
	if details == nil {
		return true
	}
	for _, alt := range details.Alternatives {
		ok := len(alt.Requirements) > 0
		for _, req := range alt.Requirements {
			if !requirementSatisfied(ctx, req, flatCredentialIsUnambiguous(details, req)) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
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
// the full context is returned (copied). Returns nil for nil input.
func ScopeContext(stored map[string]any, details *ContextRequiredDetails) map[string]any {
	if stored == nil {
		return nil
	}
	out := make(map[string]any, len(stored))
	if details == nil {
		for k, v := range stored {
			out[k] = v
		}
		return out
	}
	// Admit credentials only from the first alternative stored satisfies, using
	// the same satisfaction rule as ContextSatisfies.
	for _, alt := range details.Alternatives {
		if len(alt.Requirements) == 0 {
			continue
		}
		satisfied := true
		for _, req := range alt.Requirements {
			if !requirementSatisfied(stored, req, flatCredentialIsUnambiguous(details, req)) {
				satisfied = false
				break
			}
		}
		if !satisfied {
			continue
		}
		for _, req := range alt.Requirements {
			admitRequirement(out, stored, req)
		}
		return out
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
// alternative"). Falls back to the plain 'apiKey' field when the named entry
// is what actually satisfied the requirement (requirementSatisfied's own
// fallback). An unnamed standard requirement admits its flat family fields;
// an unmapped extension admits the single field named by req.Type.
func admitRequirement(out, stored map[string]any, req ContextRequirement) {
	if req.Type == "auth.apiKey" && req.Name != "" {
		if credentials, ok := stored["credentials"].(map[string]any); ok {
			if value, ok := credentials[req.Name].(string); ok && value != "" {
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
			if value, present := credentials[req.Name]; present {
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
// It derives the store key from the challenge's target by normalizing
// it (NormalizeEndpoint), returns the least-privilege subset of the stored
// context (ScopeContext) when it satisfies one of the challenge's alternatives,
// and declines otherwise — at which point the challenge surfaces to the caller
// unchanged. A CONTEXT_REQUIRED challenge is a scope, not a hint: the resolver
// returns only the context fields the satisfied alternative names. It does
// not classify or forward arbitrary stored fields.
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
		// Stored context may satisfy only a complete alternative whose every
		// requirement explicitly opts into reuse. Durability defaults to false;
		// filtering individual members of an AND-set would weaken the challenge.
		reusable := &ContextRequiredDetails{Target: details.Target}
		for _, alt := range details.Alternatives {
			whollyDurable := len(alt.Requirements) > 0
			for _, req := range alt.Requirements {
				if req.Durable == nil || !*req.Durable {
					whollyDurable = false
					break
				}
			}
			if whollyDurable {
				reusable.Alternatives = append(reusable.Alternatives, alt)
			}
		}
		if len(reusable.Alternatives) == 0 {
			return nil, nil
		}
		key := NormalizeEndpoint(details.Target)
		if key == "" {
			// An empty or unkeyable target cannot safely select reusable
			// stored context. Interactive or application-specific resolvers
			// may still satisfy the challenge.
			return nil, nil
		}
		stored, err := store.Get(ctx, key)
		if err != nil || stored == nil {
			return nil, err
		}
		if !ContextSatisfies(stored, reusable) {
			return nil, nil
		}
		return ScopeContext(stored, reusable), nil
	}
}
