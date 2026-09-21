package operationgraph

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
	"github.com/openbindings/openbindings-go/internal/valueio"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

// msToDuration converts milliseconds to time.Duration.
func msToDuration(ms int) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// noRoot marks an event whose lineage root is undefined: it descends from a
// merge whose contributors disagree on a root (or had none), so $input is
// unbound during expression evaluation.
const noRoot = -1

// rootTracker accumulates the merged lineage root across contributing
// events: defined if and only if every contributor shares one root.
type rootTracker struct {
	set      bool
	root     int
	conflict bool
}

func (rt *rootTracker) add(root int) {
	if !rt.set {
		rt.set = true
		rt.root = root
		return
	}
	if rt.root != root {
		rt.conflict = true
	}
}

func (rt *rootTracker) merged() int {
	if !rt.set || rt.conflict || rt.root == noRoot {
		return noRoot
	}
	return rt.root
}

// mergeMaxInto merges src into dst taking the element-wise maximum (the
// spec's lineage merge rule: a merge never lowers a count).
func mergeMaxInto(dst map[string]int, src map[string]int) {
	for k, v := range src {
		if v > dst[k] {
			dst[k] = v
		}
	}
}

// batch is one merge-node emission (a buffer flush's array or a combine
// snapshot's object) plus the merged lineage and root of its contributors.
type batch struct {
	owners   []*event
	assembly *valueio.Reservation
	data     any
	lineage  map[string]int
	root     int
}

// bufferState tracks accumulated events for a buffer node: one accumulator
// instance per graph invocation, accumulating across lineages.
type bufferState struct {
	engine  *engine
	owners  []*event
	failure error
	mu      sync.Mutex
	node    *Node
	schemas *schemaCache
	acc     []any
	lineage map[string]int
	roots   rootTracker
}

func newBufferState(node *Node, sc *schemaCache) *bufferState {
	return &bufferState{node: node, schemas: sc, lineage: map[string]int{}}
}

// add processes one incoming event per the spec's flush precedence: the
// event is added, then limit is evaluated, then until/through — so an event
// that both reaches the limit and matches until/through flushes as part of
// the batch. An until-matched event is excluded and dropped (its lineage
// does not merge into the batch).
func (bs *bufferState) add(ev *event) *batch {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	limitHit := bs.node.Limit != nil && len(bs.acc)+1 >= *bs.node.Limit
	switch {
	case limitHit:
		if !bs.retain(ev) {
			return nil
		}
		return bs.takeBatch()
	case bs.node.Until != nil && bs.matches(bs.node.Until, ev.data):
		if len(bs.acc) == 0 {
			return nil
		}
		return bs.takeBatch()
	case bs.failure != nil:
		return nil
	case bs.node.Through != nil && bs.matches(bs.node.Through, ev.data):
		if !bs.retain(ev) {
			return nil
		}
		return bs.takeBatch()
	default:
		if bs.failure != nil {
			return nil
		}
		bs.retain(ev)
		return nil
	}
}

// flush returns any remaining partial batch on completion (nil when empty —
// a buffer that accumulated nothing emits nothing, not an empty array).
func (bs *bufferState) flush() *batch {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if len(bs.acc) == 0 {
		return nil
	}
	return bs.takeBatch()
}

func (bs *bufferState) retain(ev *event) bool {
	if bs.engine != nil {
		owned := bs.engine.retainEvent(ev)
		if owned == nil {
			return false
		}
		ev = owned
		bs.owners = append(bs.owners, owned)
	}
	bs.acc = append(bs.acc, ev.data)
	mergeMaxInto(bs.lineage, ev.lineage)
	bs.roots.add(ev.root)
	return true
}

func (bs *bufferState) takeBatch() *batch {
	b := &batch{data: bs.acc, lineage: bs.lineage, root: bs.roots.merged(), owners: bs.owners}
	bs.owners = nil
	bs.acc = nil
	bs.lineage = map[string]int{}
	bs.roots = rootTracker{}
	return b
}

func (bs *bufferState) matches(schema *json.RawMessage, data any) bool {
	ok, err := bs.schemas.match(schema, data)
	if err != nil {
		bs.failure = err
		if bs.engine != nil {
			bs.engine.failValue(err)
		}
	}
	return ok
}

// combineState implements the spec's readiness rule: a combine node emits
// nothing until every incoming source has produced at least one event or
// completed; it then emits a combined object, and again on every subsequent
// event from a still-active source. A source that completed without
// producing contributes null. One instance per graph invocation.
type combineState struct {
	engine    *engine
	owners    map[string]*event
	mu        sync.Mutex
	sources   []string
	latest    map[string]any
	lineages  map[string]map[string]int
	roots     map[string]int
	produced  map[string]bool
	completed map[string]bool
	ready     bool
}

func newCombineState(sources []string) *combineState {
	return &combineState{
		sources:   sources,
		owners:    make(map[string]*event),
		latest:    make(map[string]any),
		lineages:  make(map[string]map[string]int),
		roots:     make(map[string]int),
		produced:  make(map[string]bool),
		completed: make(map[string]bool),
	}
}

// add records an event from a source. It returns a snapshot to emit when the
// node is ready (every source produced-or-completed), nil otherwise.
func (cs *combineState) add(ev *event) *batch {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.engine != nil {
		owned := cs.engine.retainEvent(ev)
		if owned == nil {
			return nil
		}
		cs.owners[ev.source].release()
		cs.owners[ev.source] = owned
		ev = owned
	}
	cs.latest[ev.source] = ev.data
	cs.lineages[ev.source] = ev.lineage
	cs.roots[ev.source] = ev.root
	cs.produced[ev.source] = true
	cs.refreshReady()
	if !cs.ready {
		return nil
	}
	return cs.snapshot()
}

// sourceComplete records one source's completion. When that completion is
// what makes the node ready, it returns the one readiness-triggered snapshot
// to emit (null for every source that completed without producing).
func (cs *combineState) sourceComplete(source string) *batch {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.completed[source] = true
	if cs.ready {
		return nil
	}
	cs.refreshReady()
	if !cs.ready {
		return nil
	}
	return cs.snapshot()
}

func (cs *combineState) refreshReady() {
	for _, s := range cs.sources {
		if !cs.produced[s] && !cs.completed[s] {
			return
		}
	}
	cs.ready = true
}

func (cs *combineState) snapshot() *batch {
	var assembly *valueio.Reservation
	if cs.engine != nil {
		units := int64(128)
		for _, source := range cs.sources {
			units += int64(128 + len(source))
			for key := range cs.lineages[source] {
				units += int64(64 + len(key))
			}
		}
		assembly = cs.engine.scope.NewReservation()
		if err := assembly.Adjust(cs.engine.ctx, nil, units); err != nil {
			cs.engine.failValue(err)
			return nil
		}
	}
	obj := make(map[string]any, len(cs.sources))
	lineage := map[string]int{}
	roots := rootTracker{}
	for _, s := range cs.sources {
		if cs.produced[s] {
			obj[s] = cs.latest[s]
			mergeMaxInto(lineage, cs.lineages[s])
			roots.add(cs.roots[s])
		} else {
			obj[s] = nil
		}
	}
	return &batch{data: obj, lineage: lineage, root: roots.merged(), assembly: assembly}
}

// schemaCache is a per-Invoker cache of compiled JSON schemas shared by
// filter and buffer schema matching.
type schemaCache struct {
	mu      sync.RWMutex
	schemas map[string]*jsonschema.Schema
}

func newSchemaCache() *schemaCache {
	return &schemaCache{schemas: make(map[string]*jsonschema.Schema)}
}

// match validates data against a JSON Schema, compiling and caching on first
// use. Compiled schemas are keyed by their raw JSON representation.
func (sc *schemaCache) match(schema *json.RawMessage, data any) (bool, error) {
	key := string(*schema)

	sc.mu.RLock()
	compiled, ok := sc.schemas[key]
	sc.mu.RUnlock()

	if !ok {
		var schemaDoc any
		if err := jsonvalue.Unmarshal(*schema, &schemaDoc); err != nil {
			return false, fmt.Errorf("compile embedded schema: %w", err)
		}
		compiler := schemacompiler.New()
		if err := compiler.AddResource("embedded.json", schemaDoc); err != nil {
			return false, fmt.Errorf("compile embedded schema: %w", err)
		}
		var err error
		compiled, err = compiler.Compile("embedded.json")
		if err != nil {
			return false, fmt.Errorf("compile embedded schema: %w", err)
		}
		sc.mu.Lock()
		sc.schemas[key] = compiled
		sc.mu.Unlock()
	}

	if err := compiled.Validate(data); err != nil {
		var mismatch *jsonschema.ValidationError
		if errors.As(err, &mismatch) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *batch) release() {
	for _, owner := range b.owners {
		owner.release()
	}
	if b.assembly != nil {
		b.assembly.Release()
	}
}
func (bs *bufferState) release() {
	for _, owner := range bs.owners {
		owner.release()
	}
	bs.owners = nil
	bs.acc = nil
}
func (cs *combineState) release() {
	for _, owner := range cs.owners {
		owner.release()
	}
	cs.owners = nil
	cs.latest = nil
	cs.lineages = nil
}
