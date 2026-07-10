package openbindings

import "context"

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
// an oauth2 refresh token alongside its access token. Any field not listed in a
// family is treated as non-secret configuration.
var requirementFamilyFields = map[string][]string{
	"auth.bearer": {"bearerToken"},
	"auth.apiKey": {"apiKey"},
	"auth.basic":  {"basic"},
	"auth.oauth2": {"accessToken", "refreshToken", "clientSecret"},
}

// isCredentialField reports whether a context key names secret credential
// material, as opposed to non-secret configuration (headers, cookies,
// environment, metadata, ...).
func isCredentialField(key string) bool {
	for _, fields := range requirementFamilyFields {
		for _, f := range fields {
			if f == key {
				return true
			}
		}
	}
	return false
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
			field, mapped := requirementFields[req.Type]
			if !mapped {
				field = req.Type
			}
			v, present := ctx[field]
			if !present || v == nil || v == "" {
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
// not a hint: the invoker receives only what it declared it needs. Every
// non-secret configuration field passes through unchanged; among the secret
// credential fields, only those belonging to the requirement families of the
// first alternative that stored satisfies are admitted, and all other stored
// credentials are withheld. With a nil challenge there is nothing to scope, so
// the full context is returned (copied). Returns nil for nil input.
func ScopeContext(stored map[string]any, details *ContextRequiredDetails) map[string]any {
	if stored == nil {
		return nil
	}
	out := make(map[string]any, len(stored))
	// Non-secret configuration always passes through.
	for k, v := range stored {
		if !isCredentialField(k) {
			out[k] = v
		}
	}
	if details == nil {
		for k, v := range stored {
			if isCredentialField(k) {
				out[k] = v
			}
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
			field, mapped := requirementFields[req.Type]
			if !mapped {
				field = req.Type
			}
			if v, present := stored[field]; !present || v == nil || v == "" {
				satisfied = false
				break
			}
		}
		if !satisfied {
			continue
		}
		for _, req := range alt.Requirements {
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
		return out
	}
	return out
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
// returns only the credentials the satisfied alternative needs plus non-secret
// config, never other stored credentials.
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
		stored, err := store.Get(ctx, NormalizeEndpoint(details.Target))
		if err != nil || stored == nil {
			return nil, err
		}
		if !ContextSatisfies(stored, details) {
			return nil, nil
		}
		return ScopeContext(stored, details), nil
	}
}
