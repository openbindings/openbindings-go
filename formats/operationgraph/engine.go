package operationgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/openbindings/openbindings-go/internal/value"
	"github.com/openbindings/openbindings-go/internal/valueio"
	"github.com/openbindings/openbindings-go/invoke"
)

const (
	// maxEvents bounds data events per graph invocation (the spec's
	// SHOULD-level amplification backstop; map-in-cycle is the primary
	// vector).
	maxEvents int64 = 100_000

	// maxErrorDepth bounds onError routing chains as defense in depth. The
	// normative bound is lineage: error events inherit it and onError routes
	// count as cycle edges (OG-V-09/OG-V-10).
	maxErrorDepth = 32
)

// event is one value flowing through the graph.
type event struct {
	data       any             // read-only logical view of packet (engine bookkeeping only)
	packet     *valueio.Packet // immutable snapshot; nil until retained, nil for markers
	held       bool            // this copy holds one reference on its root (see values.go)
	eng        *engine         // the engine whose root the copy holds
	source     string          // node key that produced this event (combine keys on it)
	root       int             // lineage root (key into engine.roots); noRoot = $input undefined
	lineage    map[string]int  // per-each-node invocation counts for maxIterations
	complete   bool            // completion marker, not a data event
	errorDepth int             // onError chain depth (defense-in-depth cap)

	// fatal is a conduit-fatal terminal marker (not a data event). When set,
	// the dispatcher fires it as the graph terminal — routed through the FIFO
	// so the failing conduit's already-enqueued outputs drain first.
	fatal *invoke.InvocationError
}

// cloneEvent copies an event without its root reference; retainEvent takes
// a reference for the copy.
func cloneEvent(ev *event) *event {
	lin := make(map[string]int, len(ev.lineage))
	for k, v := range ev.lineage {
		lin[k] = v
	}
	return &event{data: ev.data, packet: ev.packet, source: ev.source, root: ev.root, lineage: lin,
		complete: ev.complete, errorDepth: ev.errorDepth, fatal: ev.fatal}
}

func copyLineage(m map[string]int) map[string]int {
	cp := make(map[string]int, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// conduitState is an operation node's held invocation: one per graph
// invocation, created inert and driven by the first arriving event (or by
// incoming completion — the no-input case).
type conduitState struct {
	mu        sync.Mutex
	started   bool
	accepting bool
	call      invoke.Invocation[any, any]
	cancel    context.CancelFunc
	timeout   bool // a deadline context was attached (timeout field present)
	opCtx     context.Context
	lineage   map[string]int
	roots     rootTracker
	rootHeld  bool // the conduit holds a reference on its merged root
}

func newConduitState() *conduitState {
	return &conduitState{accepting: true, lineage: map[string]int{}}
}

func (c *conduitState) isAccepting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accepting
}

func (c *conduitState) setNonAccepting() {
	c.mu.Lock()
	c.accepting = false
	c.mu.Unlock()
}

// mergeEvent folds a written event into the conduit's merged lineage/root
// (the invocation's outputs follow from every event written into it). While
// the merged root is defined the conduit holds a reference on it, since an
// output carrying that root can be emitted after every contributing write's
// own event is gone; the hold is released when the root becomes undefined
// (a conflicting contributor) or the output pump ends.
func (c *conduitState) mergeEvent(eng *engine, ev *event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	mergeMaxInto(c.lineage, ev.lineage)
	before := c.roots.merged()
	c.roots.add(ev.root)
	after := c.roots.merged()
	if before == after {
		return
	}
	if c.rootHeld {
		eng.dropRoot(before)
		c.rootHeld = false
	}
	if after != noRoot {
		eng.holdRoot(after)
		c.rootHeld = true
	}
}

// releaseRoot gives up the conduit's reference on its merged root.
func (c *conduitState) releaseRoot(eng *engine) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rootHeld {
		eng.dropRoot(c.roots.merged())
		c.rootHeld = false
	}
}

func (c *conduitState) merged() (map[string]int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return copyLineage(c.lineage), c.roots.merged()
}

// engine runs a single operation graph invocation.
type engine struct {
	graph     *Graph
	invoker   *invoke.OperationInvoker
	args      *invoke.BindingInvocationArgs
	transform invoke.TransformEvaluator
	handle    invoke.BindingHandle[any, any]
	schemas   *schemaCache

	outEdges map[string][]string
	inEdges  map[string][]string
	inputKey string

	// Per-value limits for every capture, view and construction. Root
	// values are retained only when usesInput (some expression can observe
	// $input), keyed by lineage root and dropped with their last holder.
	limits    value.Limits
	usesInput bool
	rootMu    sync.Mutex
	roots     map[int]*rootRef
	nextRoot  int
	ctx       context.Context
	workers   sync.WaitGroup

	conduits map[string]*conduitState

	exitFlag   atomic.Bool
	inflight   atomic.Int64
	eventCount atomic.Int64
	// idle receives a (coalesced) notification each time inflight reaches
	// zero: true quiescence, since the input pump holds a token while the
	// caller's input side is open and every live inner invocation holds one.
	idle chan struct{}

	// completionSent dedupes per-edge completion delivery, so the
	// quiescence pass (which resolves cyclic and error-route completion)
	// and natural completion propagation never double-count an edge.
	completionMu   sync.Mutex
	completionSent map[string]map[string]bool
}

func newEngine(g *Graph, invoker *invoke.OperationInvoker, args *invoke.BindingInvocationArgs, te invoke.TransformEvaluator, sc *schemaCache) *engine {
	outE := make(map[string][]string)
	inE := make(map[string][]string)
	var inputKey string
	for _, e := range g.Edges {
		outE[e.From] = append(outE[e.From], e.To)
		inE[e.To] = append(inE[e.To], e.From)
	}
	conduits := make(map[string]*conduitState)
	for k, n := range g.Nodes {
		switch n.Type {
		case "input":
			inputKey = k
		case "operation":
			conduits[k] = newConduitState()
		}
	}
	return &engine{
		graph:          g,
		invoker:        invoker,
		args:           args,
		transform:      te,
		schemas:        sc,
		outEdges:       outE,
		inEdges:        inE,
		inputKey:       inputKey,
		conduits:       conduits,
		usesInput:      referencesInput(g),
		roots:          make(map[int]*rootRef),
		idle:           make(chan struct{}, 1),
		completionSent: make(map[string]map[string]bool),
	}
}

// workQueue is the engine's single global FIFO of (node, event) work
// items, drained by ONE dispatcher. Global FIFO is what makes the spec's
// exit-preemption promise structural: a completion marker is always
// enqueued behind the data events produced before it (per producing
// goroutine), and the dispatcher processes strictly in enqueue order, so
// at every generation of the cascade the descendants of earlier data stay
// ahead of the descendants of later completion — a completion-triggered
// buffer flush can never overtake an in-flight exit-bound event, on any
// path. Every queued item is its own retained copy (see retainEvent); the
// dispatcher releases it once processed. maxEvents bounds amplification.
type workQueue struct {
	mu    sync.Mutex
	head  int
	items []queuedEvent
	wake  chan struct{} // coalesced not-empty notification
}

type queuedEvent struct {
	to string
	ev *event
}

func newWorkQueue() *workQueue {
	return &workQueue{wake: make(chan struct{}, 1)}
}

func (q *workQueue) push(to string, ev *event) {
	q.mu.Lock()
	q.items = append(q.items, queuedEvent{to: to, ev: ev})
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *workQueue) pop() (queuedEvent, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.head == len(q.items) {
		// Empty queues retain no high-water backing array.
		q.head = 0
		q.items = nil
		return queuedEvent{}, false
	}
	item := q.items[q.head]
	q.items[q.head] = queuedEvent{} // release the event for GC
	q.head++
	if q.head >= len(q.items)-q.head {
		// Bound obsolete slots by the still-queued events (amortized
		// compaction).
		q.items = append([]queuedEvent(nil), q.items[q.head:]...)
		q.head = 0
	}
	return item, true
}

func (eng *engine) incInflight() { eng.workers.Add(1); eng.inflight.Add(1) }

func (eng *engine) decInflight() {
	defer eng.workers.Done()
	if recover() != nil {
		eng.exitFlag.Store(true)
		eng.handle.FireError(invoke.NewInvocationError(invoke.ErrCodeRuntime))
	}
	if eng.inflight.Add(-1) == 0 {
		select {
		case eng.idle <- struct{}{}:
		default:
		}
	}
}

// errValue is the in-graph `error` value for a failure originating in an
// inner invocation: the inner abstract terminal record surfaced verbatim as
// a JSON-domain value. This preserves optional data, including explicit null,
// without carrying the local error object into the graph.
func errValue(err error) any {
	if ie := invoke.AsInvocationError(err); ie != nil {
		value := map[string]any{"code": ie.Code}
		if ie.HasData() {
			value["data"] = ie.Data
		}
		return value
	}
	return fmt.Sprintf("%v", err)
}

// execute drives one graph invocation behind the binding handle: it pumps
// caller writes through the graph, runs the node workers, and settles the
// handle's terminal (CloseOutput on completion; FireError on terminal
// failure inside the engine). The graph is validated by the invoker before
// execute is reached.
func (eng *engine) execute(ctx context.Context, handle invoke.BindingHandle[any, any]) {
	eng.handle = handle
	defer func() {
		if recover() != nil {
			eng.exitFlag.Store(true)
			handle.FireError(invoke.NewInvocationError(invoke.ErrCodeRuntime))
		}
	}()
	eng.limits = resolvedLimits(handle)

	// runCtx tears down all workers when the invocation terminates (caller
	// Cancel, abandoned output stream, or upstream ctx cancellation).
	runCtx, stop := invoke.DoneContext(ctx, handle.Done())
	defer stop()
	eng.run(runCtx)

	// Normal completion. No-op when the engine already fired a terminal
	// error (exit-node error, unhandled conduit terminal, event limit) or
	// the invocation was cancelled.
	handle.CloseOutput()
}

func (eng *engine) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	eng.ctx = ctx
	defer cancel()

	// The global work queue (see workQueue): one dispatcher, global FIFO.
	queue := newWorkQueue()

	// Per-each-node spawn tracking: an each node's downstream completion is
	// deferred until its concurrently-running spawns finish (per-edge order:
	// a node's completion follows every output its data produced).
	eachWaits := make(map[string]*sync.WaitGroup)
	for key, node := range eng.graph.Nodes {
		if node.Type == "each" {
			eachWaits[key] = &sync.WaitGroup{}
		}
	}

	// Caller-write backpressure: the pump holds a token per unprocessed
	// input event (the old per-mailbox cap, applied at the graph's rim —
	// graph-internal amplification is bounded by maxEvents instead).
	inputTokens := make(chan struct{}, 256)

	bufferStates := make(map[string]*bufferState)
	combineStates := make(map[string]*combineState)
	for key, node := range eng.graph.Nodes {
		if node.Type == "buffer" {
			bufferStates[key] = newBufferState(node, eng.schemas)
			bufferStates[key].engine = eng
		}
		if node.Type == "combine" {
			combineStates[key] = newCombineState(eng.inEdges[key])
			combineStates[key].engine = eng
		}
	}

	defer func() {
		cancel()
		eng.workers.Wait()
		// Every holder releases its own root references, so a completed or
		// torn-down invocation retains no root value.
		for _, c := range eng.conduits {
			c.releaseRoot(eng)
			c.lineage = nil
		}
		for {
			item, ok := queue.pop()
			if !ok {
				break
			}
			item.ev.release()
		}
		for _, state := range bufferStates {
			state.release()
		}
		for _, state := range combineStates {
			state.release()
		}
	}()

	// Completion tracking for all nodes with incoming edges.
	completedSources := make(map[string]*atomic.Int32)
	for key := range eng.graph.Nodes {
		if len(eng.inEdges[key]) > 0 {
			completedSources[key] = &atomic.Int32{}
		}
	}

	// Enqueue never blocks (the dispatcher is transitively a producer);
	// inflight counts EXTERNAL producers only (the input pump, conduit
	// output pumps, each spawns and their completion waiters) — queued work
	// is visible to the dispatcher directly.
	sendToNode := func(toKey string, ev *event) {
		if owned := eng.retainEvent(ev); owned != nil {
			queue.push(toKey, owned)
		}
	}

	sendDownstream := func(fromKey string, ev *event) {
		base := eng.retainEvent(ev)
		if base == nil {
			return
		}
		defer base.release()
		for _, toKey := range eng.outEdges[fromKey] {
			if eng.exitFlag.Load() {
				return
			}
			base.source = fromKey
			sendToNode(toKey, base)
		}
	}

	// Completion markers travel through the same queue as data, so global
	// FIFO preserves "all data first, then complete" per edge — and, because
	// the dispatcher is single, across paths (the exit-preemption theorem;
	// see workQueue). Delivery is per-edge-once: the quiescence pass and
	// natural propagation share the same dedup, so an edge's completion is
	// never counted twice.
	deliverCompletion := func(fromKey, toKey string) bool {
		eng.completionMu.Lock()
		if eng.completionSent[fromKey][toKey] {
			eng.completionMu.Unlock()
			return false
		}
		if eng.completionSent[fromKey] == nil {
			eng.completionSent[fromKey] = make(map[string]bool)
		}
		eng.completionSent[fromKey][toKey] = true
		eng.completionMu.Unlock()
		sendToNode(toKey, &event{source: fromKey, complete: true})
		return true
	}

	sendCompletion := func(fromKey string) {
		for _, toKey := range eng.outEdges[fromKey] {
			if eng.exitFlag.Load() {
				return
			}
			deliverCompletion(fromKey, toKey)
		}
	}

	// sendFatal routes a conduit's unhandled terminal error through the global
	// FIFO instead of firing it out-of-band. Enqueued (from the conduit's pump
	// goroutine) behind the outputs that conduit already produced, it lets the
	// dispatcher emit those outputs before the terminal fires — the identity
	// law's ordering (the terminal follows the stream it terminates). This
	// mirrors the onError path, which likewise routes through the queue. It is
	// deliberately NOT accompanied by exitFlag/cancel here: doing that would
	// let the dispatcher halt at its exitFlag check and discard the conduit's
	// own queued outputs (the bug this replaces).
	sendFatal := func(ie *invoke.InvocationError) {
		sendToNode("", &event{fatal: ie})
	}

	// sendPerEventError routes a per-event failure ({error, event}) to the
	// node's onError target. Without an explicit route, the complete error
	// event terminates the graph; omission never silently discards failure.
	sendPerEventError := func(nodeKey string, errVal any, ev *event, lineage map[string]int) {
		node := eng.graph.Nodes[nodeKey]
		errorEvent := map[string]any{"error": errVal, "event": ev.data}
		if node.OnError == "" || ev.errorDepth >= maxErrorDepth {
			eng.exitFlag.Store(true)
			eng.handle.FireError(invoke.NewInvocationErrorWithData(
				invoke.ErrCodeOperationGraphExit,
				errorEvent,
			))
			cancel()
			return
		}
		sendToNode(node.OnError, &event{
			data:       errorEvent,
			source:     nodeKey,
			root:       ev.root,
			lineage:    copyLineage(lineage),
			errorDepth: ev.errorDepth + 1,
		})
	}

	// Back-closure: close the caller-facing input side once every node the
	// input node feeds is a non-accepting operation conduit. Built-in
	// consumers keep closure caller-owned (non-acceptance is defined for
	// operation nodes only).
	backClosure := func() {
		consumers := eng.outEdges[eng.inputKey]
		if len(consumers) == 0 {
			return
		}
		for _, k := range consumers {
			node := eng.graph.Nodes[k]
			if node.Type != "operation" {
				return
			}
			if eng.conduits[k].isAccepting() {
				return
			}
		}
		_ = eng.handle.CloseInput()
	}

	// startConduit opens an operation node's held invocation and
	// spawns its watcher (input closed from below) and output pump. The
	// output pump owns the node's downstream completion and its terminal
	// error handling: routed per onError when set, fatal to the graph when
	// not (the identity law's terminal-status clause).
	startConduit := func(key string, node *Node) {
		c := eng.conduits[key]
		c.mu.Lock()
		if c.started {
			c.mu.Unlock()
			return
		}
		c.started = true
		opCtx := ctx
		if node.Timeout != nil {
			opCtx, c.cancel = context.WithTimeout(ctx, msToDuration(*node.Timeout))
			c.timeout = true
		}
		c.opCtx = opCtx
		// Graph data is always generic JSON (input pump, transform/map/filter
		// results, sub-op outputs are all maps/slices/primitives), so the [any,
		// any] handle's JSON normalization on Write is a no-op here.
		call := invoke.Invoke(opCtx, eng.invoker, eng.args.Interface,
			invoke.NewOperationSignature[any, any](node.Operation),
			invoke.WithContext(eng.args.Context))
		c.call = call
		c.mu.Unlock()

		// Acceptance watcher: the inner binding closing its input from below
		// (or any terminal transition) makes the node non-accepting and may
		// back-close the graph's own input side.
		eng.workers.Add(1)
		go func() {
			defer eng.workers.Done()
			select {
			case <-call.InputClosed():
			case <-ctx.Done():
				return
			}
			c.setNonAccepting()
			backClosure()
		}()

		// Output pump. Holds an inflight token: the graph is not complete
		// while an inner invocation is in flight.
		eng.incInflight()
		go func() {
			defer eng.decInflight()
			defer func() {
				if c.cancel != nil {
					c.cancel()
				}
			}()
			// No output can follow the pump, so the conduit's hold on its
			// merged root ends with it.
			defer c.releaseRoot(eng)
			readOutput, stopOutput := eng.outputs(call)
			defer stopOutput()
			for {
				v, err := readOutput(opCtx)
				if err == io.EOF {
					sendCompletion(key)
					return
				}
				if err != nil {
					c.setNonAccepting()
					backClosure()
					if isValueFailure(err) {
						eng.failValue(err)
						return
					}
					ie := invoke.AsInvocationError(err)
					if c.timeout && opCtx.Err() == context.DeadlineExceeded {
						ie = &invoke.InvocationError{Code: TimeoutExceeded}
						call.Cancel()
					}
					if ie.Code == invoke.ErrCodeCancelled && ctx.Err() != nil {
						return // the graph itself is tearing down
					}
					if node.OnError != "" {
						// Opt-in handling: an error event without an `event`
						// member (the failure belongs to the invocation as a
						// whole), carrying the merged lineage of everything
						// written into it. The node completes; the graph
						// continues.
						lineage, root := c.merged()
						sendToNode(node.OnError, &event{
							data:    map[string]any{"error": errValue(ie)},
							source:  key,
							root:    root,
							lineage: lineage,
						})
						sendCompletion(key)
						return
					}
					// Fatal default: the graph invocation terminates with the
					// inner terminal error, verbatim — but routed through the
					// global FIFO so the outputs this conduit already enqueued
					// are drained and emitted before the terminal fires (the
					// identity law). Firing out-of-band here (exitFlag +
					// FireError + cancel) let the dispatcher halt at its
					// exitFlag check and discard those queued outputs.
					sendFatal(ie)
					return
				}
				if eng.exitFlag.Load() {
					v.release()
					stopOutput()
					return
				}
				lineage, root := c.merged()
				v.source, v.root, v.lineage = key, root, lineage
				sendDownstream(key, v)
				v.release()
			}
		}()
	}

	// An operation node denotes one unconditional held session. Open every
	// conduit with the graph, before waiting for caller input, so startup
	// output/failure preserve the causal availability of direct invocation.
	for key, node := range eng.graph.Nodes {
		if node.Type == "operation" {
			startConduit(key, node)
		}
	}

	// handleCompletion processes a completion marker arriving at a node.
	handleCompletion := func(key string, node *Node, ev *event) {
		// The input node's completion marker comes from the pump through its
		// own mailbox (so it can never overtake buffered writes); forward it.
		if node.Type == "input" {
			sendCompletion(key)
			return
		}
		// combine consumes per-source completion before the all-complete
		// transition (completion can be what makes it ready).
		if node.Type == "combine" {
			if snap := combineStates[key].sourceComplete(ev.source); snap != nil {
				sendDownstream(key, &event{data: snap.data, source: key, root: snap.root, lineage: snap.lineage})
				snap.release()
			}
		}
		counter, ok := completedSources[key]
		if !ok {
			return
		}
		if int(counter.Add(1)) < len(eng.inEdges[key]) {
			return
		}
		// All incoming edges complete.
		switch node.Type {
		case "operation":
			// Close the held invocation's input side; the output pump sends
			// this node's completion when the invocation's outputs finish.
			startConduit(key, node)
			_ = eng.conduits[key].call.Close()
			return
		case "each":
			// Spawns run concurrently; the node's completion must follow
			// every output they will produce. All spawns for this node were
			// registered before this marker was processed (global FIFO put
			// their data events ahead of it), so the waiter's count is
			// final. The waiter holds an inflight token: the graph is not
			// quiescent while spawns are outstanding.
			wg := eachWaits[key]
			eng.incInflight()
			go func() {
				defer eng.decInflight()
				wg.Wait()
				sendCompletion(key)
			}()
			return
		case "buffer":
			if b := bufferStates[key].flush(); b != nil {
				sendDownstream(key, &event{data: b.data, source: key, root: b.root, lineage: b.lineage})
				b.release()
			}
		}
		sendCompletion(key)
	}

	// Input pump: every caller write becomes one event at the input node, in
	// write order, each rooting a lineage. The pump's inflight token keeps
	// the graph alive while the caller's input side is open; back-closure
	// (or the caller's Close) ends the pump via ReadInput's EOF. The token
	// gate bounds unprocessed caller input (the dispatcher releases a slot
	// per input event it processes).
	eng.incInflight()
	go func() {
		defer eng.decInflight()
		// End-of-input travels through the queue like any event, so FIFO
		// ordering guarantees it never overtakes a write.
		defer sendToNode(eng.inputKey, &event{complete: true})
		for {
			v, err := eng.input(ctx)
			if err != nil {
				return // io.EOF (input side closed) or terminal
			}
			select {
			case inputTokens <- struct{}{}:
			case <-ctx.Done():
				return
			}
			eng.addRoot(v)
			v.lineage = map[string]int{}
			sendToNode(eng.inputKey, v)
			v.release()
		}
	}()

	// spawnEach runs one each-invocation per arriving event on its own
	// goroutine — the spec's concurrency point ("interleaving across
	// concurrent each invocations' outputs" is implementation-defined).
	// Each spawn holds an inflight token and registers on the node's
	// waiter, which defers the node's downstream completion until every
	// spawn has emitted (per-edge order: completion follows the data).
	spawnEach := func(key string, node *Node, ev *event) {
		ev = eng.retainEvent(ev)
		if ev == nil {
			return
		}
		wg := eachWaits[key]
		wg.Add(1)
		eng.incInflight()
		go func() {
			defer eng.decInflight()
			defer wg.Done()
			defer ev.release()
			eng.processEach(ctx, key, node, ev, sendDownstream, sendPerEventError)
		}()
	}

	// The dispatcher: ONE loop drains the global queue in FIFO order —
	// every node's bookkeeping (filters, transforms, buffers, completion
	// counters) runs here, serially, which is what makes cross-path
	// ordering deterministic where the spec pins it (an exit always
	// preempts the end-of-stream flush; see workQueue). Concurrency lives
	// where the spec puts it: operation conduits and each spawns run on
	// their own goroutines and feed the queue externally, so their output
	// interleaving stays implementation-defined.
	//
	// When the queue is empty, quiescence is judged by the external tokens
	// (pump, conduit pumps, each spawns/waiters): a zero crossing with an
	// empty queue means no event can ever flow again. Any edge whose
	// completion has not been delivered by then is starved by a cycle
	// (circular completion dependency) or feeds from an error route;
	// injecting those completions is the spec's implementation-defined
	// drain detection. Injected markers may flush buffers and complete
	// combines, producing new work; loop until a drain injects nothing.
	for {
		if ctx.Err() != nil || eng.exitFlag.Load() {
			break // remaining queued work is the exit's discarded in-flight
		}
		item, ok := queue.pop()
		if !ok {
			if eng.inflight.Load() == 0 {
				// External producers enqueue before releasing their token
				// (same goroutine), so at a zero crossing one more pop sees
				// everything; only then is an empty queue conclusive.
				if item, ok = queue.pop(); !ok {
					injected := false
					for _, e := range eng.graph.Edges {
						if deliverCompletion(e.From, e.To) {
							injected = true
						}
					}
					if !injected {
						break
					}
					continue
				}
			} else {
				select {
				case <-queue.wake:
				case <-eng.idle:
				case <-ctx.Done():
				}
				continue
			}
		}
		if item.ev.fatal != nil {
			// A conduit's unhandled terminal, reached in FIFO order after the
			// outputs it produced (which the dispatcher has now emitted): fire
			// it as the graph terminal and tear down. cancel() runs after the
			// loop breaks. Exactly one fatal marker is ever processed — the
			// break stops the dispatcher, and the top-of-loop exitFlag check
			// short-circuits any later marker.
			eng.exitFlag.Store(true)
			eng.handle.FireError(item.ev.fatal)
			item.ev.release()
			break
		}
		if item.to == eng.inputKey && !item.ev.complete {
			<-inputTokens // release the pump's backpressure slot
		}
		func() {
			defer item.ev.release()
			eng.processNode(ctx, item.to, eng.graph.Nodes[item.to], item.ev, cancel,
				sendDownstream, sendCompletion, sendPerEventError, handleCompletion, startConduit, backClosure, spawnEach, bufferStates, combineStates)
		}()
	}
	cancel()
}

func (eng *engine) processNode(
	ctx context.Context,
	key string, node *Node, ev *event,
	cancel context.CancelFunc,
	sendDownstream func(string, *event),
	sendCompletion func(string),
	sendPerEventError func(string, any, *event, map[string]int),
	handleCompletion func(string, *Node, *event),
	startConduit func(string, *Node),
	backClosure func(),
	spawnEach func(string, *Node, *event),
	bufferStates map[string]*bufferState,
	combineStates map[string]*combineState,
) {
	if ev.complete {
		handleCompletion(key, node, ev)
		return
	}

	// Amplification backstop.
	if eng.eventCount.Add(1) > maxEvents {
		eng.exitFlag.Store(true)
		eng.handle.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeEventLimitExceeded,
		})
		cancel()
		return
	}

	switch node.Type {
	case "input":
		sendDownstream(key, ev)

	case "output":
		if err := eng.emit(ev); err != nil {
			eng.exitFlag.Store(true)
			cancel()
		}

	case "exit":
		eng.exitFlag.Store(true)
		if node.Error != nil && *node.Error {
			eng.handle.FireError(invoke.NewInvocationErrorWithData(
				invoke.ErrCodeOperationGraphExit,
				ev.data,
			))
		} else if err := eng.emit(ev); err != nil {
			_ = err // the cancel below tears the engine down either way
		}
		cancel()

	case "operation":
		eng.processConduitEvent(ctx, key, node, ev, startConduit, sendPerEventError, backClosure)

	case "each":
		spawnEach(key, node, ev)

	case "filter":
		eng.processFilter(key, node, ev, sendDownstream, sendPerEventError)

	case "transform":
		eng.processTransform(key, node, ev, sendDownstream, sendPerEventError)

	case "map":
		eng.processMap(key, node, ev, sendDownstream, sendPerEventError)

	case "buffer":
		if b := bufferStates[key].add(ev); b != nil {
			sendDownstream(key, &event{data: b.data, source: key, root: b.root, lineage: b.lineage})
			b.release()
		}

	case "combine":
		if snap := combineStates[key].add(ev); snap != nil {
			sendDownstream(key, &event{data: snap.data, source: key, root: snap.root, lineage: snap.lineage})
			snap.release()
		}
	}
}

// processConduitEvent writes one arriving event into the conduit's held
// invocation, or rejects it (WRITE_REJECTED) when the node is non-accepting.
func (eng *engine) processConduitEvent(
	ctx context.Context, key string, node *Node, ev *event,
	startConduit func(string, *Node),
	sendPerEventError func(string, any, *event, map[string]int),
	backClosure func(),
) {
	c := eng.conduits[key]
	if !c.isAccepting() {
		sendPerEventError(key, WriteRejected, ev, ev.lineage)
		return
	}
	startConduit(key, node)
	c.mergeEvent(eng, ev)
	if err := eng.writeChild(c.opCtx, c.call, ev); err != nil {
		if isValueFailure(err) {
			eng.failValue(err)
			return
		}
		ie := invoke.AsInvocationError(err)
		if ie.Code == invoke.ErrCodeInputClosed {
			// The write raced the inner binding closing its input from below.
			c.setNonAccepting()
			backClosure()
			sendPerEventError(key, WriteRejected, ev, ev.lineage)
		}
		// Terminal failures surface through the output pump, which owns
		// reporting; nothing further to do here.
	}
}

// processEach opens one invocation per arriving event, writing the event as
// its only input. maxIterations bounds invocations per event lineage.
func (eng *engine) processEach(
	ctx context.Context, key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendPerEventError func(string, any, *event, map[string]int),
) {
	lineage := copyLineage(ev.lineage)
	if node.MaxIterations != nil && lineage[key] >= *node.MaxIterations {
		return // safety bound: the event is dropped, not errored
	}
	lineage[key]++

	opCtx := ctx
	hasTimeout := node.Timeout != nil
	var opCancel context.CancelFunc
	if hasTimeout {
		opCtx, opCancel = context.WithTimeout(ctx, msToDuration(*node.Timeout))
		defer opCancel()
	}

	call := invoke.Invoke(opCtx, eng.invoker, eng.args.Interface,
		invoke.NewOperationSignature[any, any](node.Operation),
		invoke.WithContext(eng.args.Context))
	// One write, then close: each fixes the graph's contribution at one
	// write per session. Write/Close failures surface via the read loop.
	_ = eng.writeChild(opCtx, call, ev)
	_ = call.Close()

	readOutput, stopOutput := eng.outputs(call)
	defer stopOutput()
	for {
		v, err := readOutput(opCtx)
		if err == io.EOF {
			return
		}
		if err != nil {
			if isValueFailure(err) {
				eng.failValue(err)
				return
			}
			ie := invoke.AsInvocationError(err)
			if hasTimeout && opCtx.Err() == context.DeadlineExceeded {
				ie = &invoke.InvocationError{Code: TimeoutExceeded}
				call.Cancel()
			}
			if ie.Code == invoke.ErrCodeCancelled && ctx.Err() != nil {
				return // graph teardown, not a node failure
			}
			sendPerEventError(key, errValue(ie), ev, lineage)
			return
		}
		if eng.exitFlag.Load() {
			v.release()
			stopOutput()
			return
		}
		v.source, v.root, v.lineage = key, ev.root, copyLineage(lineage)
		sendDownstream(key, v)
		v.release()
	}
}

func (eng *engine) processFilter(
	key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendPerEventError func(string, any, *event, map[string]int),
) {
	if node.Schema != nil {
		passes, err := eng.schemas.match(node.Schema, ev.data)
		if err != nil {
			eng.failValue(err)
			return
		}
		if passes {
			sendDownstream(key, ev)
		}
		return
	}
	result, failed := eng.evalOrFail(key, *node.Transform, ev, sendPerEventError)
	if failed {
		return
	}
	defer result.release()
	if isTruthy(result.data) {
		sendDownstream(key, ev)
	}
}

func (eng *engine) processTransform(
	key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendPerEventError func(string, any, *event, map[string]int),
) {
	result, failed := eng.evalOrFail(key, *node.Transform, ev, sendPerEventError)
	if failed {
		return
	}
	defer result.release()
	result.source, result.root, result.lineage = key, ev.root, ev.lineage
	sendDownstream(key, result)
}

func (eng *engine) processMap(
	key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendPerEventError func(string, any, *event, map[string]int),
) {
	result, failed := eng.evalOrFail(key, *node.Transform, ev, sendPerEventError)
	if failed {
		return
	}
	defer result.release()
	arr, ok := toSlice(result.data)
	if !ok {
		sendPerEventError(key, MapNotArray, ev, ev.lineage)
		return
	}
	for _, item := range arr {
		if eng.exitFlag.Load() {
			return
		}
		sendDownstream(key, &event{data: item, source: key, root: ev.root, lineage: copyLineage(ev.lineage)})
	}
}

// evalOrFail evaluates a node expression with the event as $ and the
// lineage's root input as $input. The evaluator receives its own mutable
// trees: the event's and, when the root is defined and retained, the root's.
// An undefined result fails the node with TRANSFORM_UNDEFINED; other
// evaluation failures fail it with their message. The result is admitted as
// a new unrooted event. failed=true means a per-event error was already
// routed (or dropped).
func (eng *engine) evalOrFail(
	key, expression string, ev *event,
	sendPerEventError func(string, any, *event, map[string]int),
) (*event, bool) {
	if eng.transform == nil {
		sendPerEventError(key, ExpressionEvaluationFailed, ev, ev.lineage)
		return nil, true
	}
	input, err := ev.packet.View(eng.ctx, eng.limits, true)
	if err != nil {
		eng.failValue(err)
		return nil, true
	}
	var result any
	if eb, ok := eng.transform.(invoke.TransformEvaluatorWithBindings); ok {
		bindings := map[string]any{}
		if rp, defined := eng.rootPacket(ev.root); defined {
			root, viewErr := rp.View(eng.ctx, eng.limits, true)
			if viewErr != nil {
				eng.failValue(viewErr)
				return nil, true
			}
			bindings["input"] = root
		}
		result, err = eb.EvaluateWithBindings(eng.ctx, expression, input, bindings)
	} else {
		result, err = eng.transform.Evaluate(eng.ctx, expression, input)
	}
	if eng.ctx.Err() != nil {
		return nil, true
	}
	if err != nil {
		if errors.Is(err, invoke.ErrTransformUndefined) {
			sendPerEventError(key, TransformUndefined, ev, ev.lineage)
		} else {
			sendPerEventError(key, ExpressionEvaluationFailed, ev, ev.lineage)
		}
		return nil, true
	}
	p, err := valueio.Capture(eng.ctx, eng.limits, result)
	if err != nil {
		if isValueFailure(err) {
			eng.failValue(err)
		} else {
			sendPerEventError(key, ExpressionEvaluationFailed, ev, ev.lineage)
		}
		return nil, true
	}
	out, err := eng.packetEvent(eng.ctx, p)
	if err != nil {
		return nil, true
	}
	return out, false
}

// isTruthy implements JSONata 2.1's boolean cast ($boolean) for filter
// expression results: empty composites are false, and an array is true only
// if some member casts to true. (Undefined never reaches here: it fails the
// node with TRANSFORM_UNDEFINED per the Transforms rule.)
func isTruthy(v any) bool {
	if v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case json.Number:
		for _, ch := range string(val) {
			if ch == 'e' || ch == 'E' {
				break
			}
			if ch >= '1' && ch <= '9' {
				return true
			}
		}
		return false
	case float64:
		return val != 0
	case string:
		return val != ""
	case int:
		return val != 0
	case []any:
		for _, m := range val {
			if isTruthy(m) {
				return true
			}
		}
		return false
	case map[string]any:
		return len(val) > 0
	default:
		return true
	}
}

func toSlice(v any) ([]any, bool) { arr, ok := v.([]any); return arr, ok }
