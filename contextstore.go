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
