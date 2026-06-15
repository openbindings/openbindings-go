package openbindings

import "context"

// ---------------------------------------------------------------------------
// Store-backed context resolver
// ---------------------------------------------------------------------------

// requirementFields maps standard requirement families (binding-invoker
// role) to the well-known context field that satisfies them (context-store
// role).
var requirementFields = map[string]string{
	"auth.bearer": "bearerToken",
	"auth.apiKey": "apiKey",
	"auth.basic":  "basic",
	"auth.oauth2": "accessToken",
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

// StoreContextResolver builds a read-only ContextResolver backed by a
// ContextStore: the composition of the binding-invoker and context-store
// roles. It derives the store key from the challenge's target by normalizing
// it (NormalizeEndpoint), returns the stored context when it satisfies one of
// the challenge's alternatives, and declines otherwise — at which point the
// challenge surfaces to the caller unchanged.
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
		return stored, nil
	}
}
