package operationgraph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// msToDuration converts milliseconds to time.Duration.
func msToDuration(ms int) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// bufferState tracks accumulated events for a buffer node.
type bufferState struct {
	mu      sync.Mutex
	node    *Node
	acc     []any
	schemas *schemaCache
}

func newBufferState(node *Node, sc *schemaCache) *bufferState {
	return &bufferState{node: node, schemas: sc}
}

// add processes an incoming event and returns any batches to flush.
func (bs *bufferState) add(ev *event) []any {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if bs.node.Until != nil {
		if matches, _ := bs.schemas.match(bs.node.Until, ev.data); matches {
			if len(bs.acc) == 0 {
				return nil
			}
			batch := make([]any, len(bs.acc))
			copy(batch, bs.acc)
			bs.acc = bs.acc[:0]
			return []any{batch}
		}
	}

	if bs.node.Through != nil {
		bs.acc = append(bs.acc, ev.data)
		if matches, _ := bs.schemas.match(bs.node.Through, ev.data); matches {
			batch := make([]any, len(bs.acc))
			copy(batch, bs.acc)
			bs.acc = bs.acc[:0]
			return []any{batch}
		}
		return nil
	}

	bs.acc = append(bs.acc, ev.data)

	if bs.node.Limit != nil && len(bs.acc) >= *bs.node.Limit {
		batch := make([]any, len(bs.acc))
		copy(batch, bs.acc)
		bs.acc = bs.acc[:0]
		return []any{batch}
	}

	return nil
}

// flush returns the remaining accumulated events on completion.
func (bs *bufferState) flush() any {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if len(bs.acc) == 0 {
		return nil
	}
	batch := make([]any, len(bs.acc))
	copy(batch, bs.acc)
	bs.acc = bs.acc[:0]
	return batch
}

// combineState tracks the latest event from each source for a combine node.
// Emits a combined object every time any source produces a new event.
type combineState struct {
	mu       sync.Mutex
	expected map[string]bool
	latest   map[string]any // latest event per source (nil if not yet received)
	has      map[string]bool // whether a source has produced at least one event
}

func newCombineState(sources []string) *combineState {
	expected := make(map[string]bool, len(sources))
	for _, s := range sources {
		expected[s] = true
	}
	return &combineState{
		expected: expected,
		latest:   make(map[string]any),
		has:      make(map[string]bool),
	}
}

// add records a new event from a source and returns the combined snapshot to emit.
func (cs *combineState) add(ev *event) (map[string]any, bool) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.latest[ev.source] = ev.data
	cs.has[ev.source] = true
	return cs.snapshot(), true
}

// complete is called when all upstream sources are done. Nothing to emit;
// the combine node's completion is handled by the engine's completion propagation.
func (cs *combineState) complete() (map[string]any, bool) {
	return nil, false
}

func (cs *combineState) snapshot() map[string]any {
	result := make(map[string]any, len(cs.expected))
	for source := range cs.expected {
		if cs.has[source] {
			result[source] = cs.latest[source]
		} else {
			result[source] = nil
		}
	}
	return result
}

// schemaCache is a per-Executor cache of compiled JSON schemas.
// Storing it on the Executor avoids mutable package-level state.
type schemaCache struct {
	mu      sync.RWMutex
	schemas map[string]*jsonschema.Schema
}

func newSchemaCache() *schemaCache {
	return &schemaCache{schemas: make(map[string]*jsonschema.Schema)}
}

// match validates data against a JSON Schema, compiling and caching on first use.
// Compiled schemas are keyed by their raw JSON representation.
func (sc *schemaCache) match(schema *json.RawMessage, data any) (bool, error) {
	key := string(*schema)

	sc.mu.RLock()
	compiled, ok := sc.schemas[key]
	sc.mu.RUnlock()

	if !ok {
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("filter.json", bytes.NewReader(*schema)); err != nil {
			return false, fmt.Errorf("compile filter schema: %w", err)
		}
		var err error
		compiled, err = compiler.Compile("filter.json")
		if err != nil {
			return false, fmt.Errorf("compile filter schema: %w", err)
		}
		sc.mu.Lock()
		sc.schemas[key] = compiled
		sc.mu.Unlock()
	}

	if err := compiled.Validate(data); err != nil {
		return false, nil // validation failed = event doesn't match
	}
	return true, nil
}
