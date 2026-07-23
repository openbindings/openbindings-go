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
// "auth.apiKey" names only "apiKey" here (the unnamed, single-key
// convenience): a NAMED auth.apiKey requirement is satisfied and scoped
// through the separate 'apiKeys' map (see requirementSatisfied /
// admitRequirement), never wholesale — admitting the whole map here would
// leak every other alternative's key alongside the one actually needed.
var requirementFamilyFields = map[string][]string{
	"auth.bearer": {"bearerToken"},
	"auth.apiKey": {"apiKey"},
	"auth.basic":  {"basic"},
	"auth.oauth2": {"accessToken", "refreshToken", "clientSecret"},
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
}

// isCredentialField reports whether a context key names one of the standard
// credential fields. It does not classify unrecognized fields as non-secret.
func isCredentialField(key string) bool {
	return credentialFieldNames[key]
}

// requirementSatisfied reports whether ctx resolves one requirement. A NAMED
// auth.apiKey requirement (req.Name set) checks the scheme-scoped
// context.apiKeys[req.Name] first, falling back to the single context.apiKey
// convenience — the same priority ContextAPIKeyFor applies at
// credential-application time. A mapped family (auth.bearer/apiKey/basic/
// oauth2, or an unnamed auth.apiKey) checks the single well-known field it
// resolves into.
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
func requirementSatisfied(ctx map[string]any, req ContextRequirement) bool {
	if req.Type == "auth.apiKey" && req.Name != "" {
		return ContextAPIKeyFor(ctx, req.Name) != ""
	}
	if req.Type == "config.value" {
		point, _ := req.Extra["point"].(string)
		configuration, _ := ctx["configuration"].(map[string]any)
		v, present := configuration[point]
		return point != "" && present && v != nil && v != ""
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
			if !requirementSatisfied(ctx, req) {
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
			if !requirementSatisfied(stored, req) {
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
// satisfy one already-satisfied requirement. A NAMED auth.apiKey requirement
// admits only its single 'apiKeys[name]' entry (never the whole 'apiKeys'
// map, and never another alternative's key) — scoped provisioning per the
// binding-invoker contract's least-privilege rule (§ ContextRequiredDetails:
// "provisions only the context that satisfies the one selected
// alternative"). Falls back to the plain 'apiKey' field when the named entry
// is what actually satisfied the requirement (requirementSatisfied's own
// fallback). Every other requirement admits its whole family
// (requirementFamilyFields), or a single field named after req.Type when the
// family is unmapped.
func admitRequirement(out, stored map[string]any, req ContextRequirement) {
	if req.Type == "auth.apiKey" && req.Name != "" {
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
		configuration, _ := stored["configuration"].(map[string]any)
		if point == "" {
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
		scoped[point] = value
		return
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
// empty value) is a decline like any other: the challenge surfaces, and its
// error text names the requirement family and the context field that would
// satisfy it — check the stored entry's keys against that field name.
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
		if !ContextSatisfies(stored, details) {
			return nil, nil
		}
		return ScopeContext(stored, details), nil
	}
}
