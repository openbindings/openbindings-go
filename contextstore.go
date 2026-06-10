package openbindings

import (
	"context"
	"encoding/json"
	"sync"
)

// memoryStore is an in-memory ContextStore for session-scoped usage.
type memoryStore struct {
	mu   sync.RWMutex
	data map[string]map[string]any
}

// NewMemoryStore returns a thread-safe in-memory ContextStore.
// Suitable for session-scoped context that doesn't need to survive
// process restarts.
func NewMemoryStore() ContextStore {
	return &memoryStore{data: make(map[string]map[string]any)}
}

func (s *memoryStore) Get(_ context.Context, key string) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	return deepCopyMap(v)
}

func (s *memoryStore) Set(_ context.Context, key string, value map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == nil {
		delete(s.data, key)
		return nil
	}
	cp, err := deepCopyMap(value)
	if err != nil {
		return err
	}
	s.data[key] = cp
	return nil
}

func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

// deepCopyMap creates a deep copy of a map[string]any via JSON round-trip.
// This ensures nested structures (e.g. basic auth credentials) are fully
// isolated between callers and the store.
// Note: JSON round-trip converts int to float64. Context maps use string
// values in practice, so this is not an issue.
func deepCopyMap(m map[string]any) (map[string]any, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var cp map[string]any
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, err
	}
	return cp, nil
}

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
// roles. It looks up the challenge's Key, returns the stored context when it
// satisfies one of the challenge's alternatives, and declines otherwise — at
// which point the challenge surfaces to the caller unchanged.
//
// Apps that resolve interactively (prompt, browser redirect, keychain)
// supply their own resolver and MAY persist what they obtain for durable
// requirements under details.Key; non-durable context MUST NOT be persisted.
func StoreContextResolver(store ContextStore) ContextResolver {
	return func(ctx context.Context, details *ContextRequiredDetails) (map[string]any, error) {
		if details == nil {
			return nil, nil
		}
		stored, err := store.Get(ctx, details.Key)
		if err != nil || stored == nil {
			return nil, err
		}
		if !ContextSatisfies(stored, details) {
			return nil, nil
		}
		return stored, nil
	}
}
