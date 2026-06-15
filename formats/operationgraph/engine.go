package operationgraph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	openbindings "github.com/openbindings/openbindings-go"
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
	data       any
	source     string         // node key that produced this event (combine keys on it)
	root       int            // lineage root (index into engine.rootValues); noRoot = $input undefined
	lineage    map[string]int // per-each-node invocation counts for maxIterations
	complete   bool           // completion marker, not a data event
	errorDepth int            // onError chain depth (defense-in-depth cap)
}

func cloneEvent(ev *event) *event {
	lin := make(map[string]int, len(ev.lineage))
	for k, v := range ev.lineage {
		lin[k] = v
	}
	return &event{data: ev.data, source: ev.source, root: ev.root, lineage: lin, errorDepth: ev.errorDepth}
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
	call      openbindings.Invocation[any, any]
	cancel    context.CancelFunc
	timeout   bool // a deadline context was attached (timeout field present)
	opCtx     context.Context
	lineage   map[string]int
	roots     rootTracker
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
// (the invocation's outputs follow from every event written into it).
func (c *conduitState) mergeEvent(ev *event) {
	c.mu.Lock()
	mergeMaxInto(c.lineage, ev.lineage)
	c.roots.add(ev.root)
	c.mu.Unlock()
}

func (c *conduitState) merged() (map[string]int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return copyLineage(c.lineage), c.roots.merged()
}

// engine runs a single operation graph invocation.
type engine struct {
	graph     *Graph
	invoker   *openbindings.OperationInvoker
	args      *openbindings.BindingInvocationArgs
	transform openbindings.TransformEvaluator
	handle    openbindings.BindingHandle[any, any]
	schemas   *schemaCache

	outEdges map[string][]string
	inEdges  map[string][]string
	inputKey string

	rootMu     sync.Mutex
	rootValues []any

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

func newEngine(g *Graph, invoker *openbindings.OperationInvoker, args *openbindings.BindingInvocationArgs, te openbindings.TransformEvaluator, sc *schemaCache) *engine {
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
		idle:           make(chan struct{}, 1),
		completionSent: make(map[string]map[string]bool),
	}
}

func (eng *engine) incInflight() { eng.inflight.Add(1) }

func (eng *engine) decInflight() {
	if eng.inflight.Add(-1) == 0 {
		select {
		case eng.idle <- struct{}{}:
		default:
		}
	}
}

func (eng *engine) addRoot(v any) int {
	eng.rootMu.Lock()
	defer eng.rootMu.Unlock()
	eng.rootValues = append(eng.rootValues, v)
	return len(eng.rootValues) - 1
}

func (eng *engine) rootValue(root int) (any, bool) {
	if root == noRoot {
		return nil, false
	}
	eng.rootMu.Lock()
	defer eng.rootMu.Unlock()
	return eng.rootValues[root], true
}

// errValue is the in-graph `error` value for a failure originating in an
// inner invocation: the inner terminal error surfaced verbatim. The
// InvocationError's code is its routable identity.
func errValue(err error) any {
	if ie := openbindings.AsInvocationError(err); ie != nil {
		return ie.Code
	}
	return fmt.Sprintf("%v", err)
}

// execute drives one graph invocation behind the binding handle: it pumps
// caller writes through the graph, runs the node workers, and settles the
// handle's terminal (CloseOutput on completion; FireError on terminal
// failure inside the engine). The graph is validated by the invoker before
// execute is reached.
func (eng *engine) execute(ctx context.Context, handle openbindings.BindingHandle[any, any]) {
	eng.handle = handle

	// runCtx tears down all workers when the invocation terminates (caller
	// Cancel, abandoned output stream, or upstream ctx cancellation).
	runCtx, stop := openbindings.DoneContext(ctx, handle.Done())
	defer stop()
	eng.run(runCtx)

	// Normal completion. No-op when the engine already fired a terminal
	// error (exit-node error, unhandled conduit terminal, event limit) or
	// the invocation was cancelled.
	handle.CloseOutput()
}

func (eng *engine) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Node mailboxes.
	mailboxes := make(map[string]chan *event)
	for key := range eng.graph.Nodes {
		mailboxes[key] = make(chan *event, 256)
	}

	bufferStates := make(map[string]*bufferState)
	combineStates := make(map[string]*combineState)
	for key, node := range eng.graph.Nodes {
		if node.Type == "buffer" {
			bufferStates[key] = newBufferState(node, eng.schemas)
		}
		if node.Type == "combine" {
			combineStates[key] = newCombineState(eng.inEdges[key])
		}
	}

	// Completion tracking for all nodes with incoming edges.
	completedSources := make(map[string]*atomic.Int32)
	for key := range eng.graph.Nodes {
		if len(eng.inEdges[key]) > 0 {
			completedSources[key] = &atomic.Int32{}
		}
	}

	sendToNode := func(toKey string, ev *event) {
		eng.incInflight()
		select {
		case mailboxes[toKey] <- ev:
		case <-ctx.Done():
			eng.decInflight()
		}
	}

	sendDownstream := func(fromKey string, ev *event) {
		for _, toKey := range eng.outEdges[fromKey] {
			if eng.exitFlag.Load() {
				return
			}
			c := cloneEvent(ev)
			c.source = fromKey
			sendToNode(toKey, c)
		}
	}

	// Completion markers travel through the same mailboxes as data so FIFO
	// ordering preserves "all data first, then complete" per edge. Delivery
	// is per-edge-once: the quiescence pass and natural propagation share
	// the same dedup, so an edge's completion is never counted twice.
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

	// sendPerEventError routes a per-event failure ({error, event}) to the
	// node's onError target, or drops it. The error event inherits the
	// failing event's lineage and root.
	sendPerEventError := func(nodeKey string, errVal any, ev *event, lineage map[string]int) {
		node := eng.graph.Nodes[nodeKey]
		if node.OnError == "" {
			return
		}
		if ev.errorDepth >= maxErrorDepth {
			return
		}
		sendToNode(node.OnError, &event{
			data:       map[string]any{"error": errVal, "event": ev.data},
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

	// startConduit lazily drives an operation node's held invocation and
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
		call := eng.invoker.Invoke(opCtx, &openbindings.OperationInvocationArgs{
			Interface: eng.args.Interface,
			Operation: node.Operation,
			Context:   eng.args.Context,
		})
		c.call = call
		c.mu.Unlock()

		// Acceptance watcher: the inner binding closing its input from below
		// (or any terminal transition) makes the node non-accepting and may
		// back-close the graph's own input side.
		go func() {
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
			out := call.Outputs()
			for {
				v, err := out.Read(opCtx)
				if err == io.EOF {
					sendCompletion(key)
					return
				}
				if err != nil {
					c.setNonAccepting()
					backClosure()
					ie := openbindings.AsInvocationError(err)
					if c.timeout && opCtx.Err() == context.DeadlineExceeded {
						ie = &openbindings.InvocationError{Code: TimeoutExceeded, Message: fmt.Sprintf("operation %q exceeded its %dms budget", node.Operation, *node.Timeout)}
						call.Cancel()
					}
					if ie.Code == openbindings.ErrCodeCancelled && ctx.Err() != nil {
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
					// inner terminal error, verbatim.
					eng.exitFlag.Store(true)
					eng.handle.FireError(ie)
					cancel()
					return
				}
				if eng.exitFlag.Load() {
					out.Stop()
					return
				}
				lineage, root := c.merged()
				sendDownstream(key, &event{data: v, source: key, root: root, lineage: lineage})
			}
		}()
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
		case "buffer":
			if b := bufferStates[key].flush(); b != nil {
				sendDownstream(key, &event{data: b.data, source: key, root: b.root, lineage: b.lineage})
			}
		}
		sendCompletion(key)
	}

	// Worker per node.
	var wg sync.WaitGroup
	for key := range eng.graph.Nodes {
		nodeKey := key
		node := eng.graph.Nodes[nodeKey]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-mailboxes[nodeKey]:
					if eng.exitFlag.Load() {
						eng.decInflight()
						return
					}
					eng.processNode(ctx, nodeKey, node, ev, cancel,
						sendDownstream, sendCompletion, sendPerEventError,
						handleCompletion, startConduit, backClosure,
						bufferStates, combineStates)
					eng.decInflight()
				}
			}
		}()
	}

	// Input pump: every caller write becomes one event at the input node, in
	// write order, each rooting a lineage. The pump's inflight token keeps
	// the graph alive while the caller's input side is open; back-closure
	// (or the caller's Close) ends the pump via ReadInput's EOF.
	eng.incInflight()
	go func() {
		defer eng.decInflight()
		// End-of-input travels through the input node's mailbox like any
		// event, so FIFO ordering guarantees it never overtakes a write.
		defer sendToNode(eng.inputKey, &event{complete: true})
		for {
			v, err := eng.handle.ReadInput(ctx)
			if err != nil {
				return // io.EOF (input side closed) or terminal
			}
			rid := eng.addRoot(v)
			sendToNode(eng.inputKey, &event{data: v, root: rid, lineage: map[string]int{}})
		}
	}()

	// Quiescence loop. The pump's token (input side open) and every live
	// inner invocation's token keep inflight above zero, so a zero crossing
	// means no event can ever flow again. Any edge whose completion has not
	// been delivered by then is starved by a cycle (circular completion
	// dependency) or feeds from an error route; injecting those completions
	// is the spec's implementation-defined drain detection. The injected
	// markers may flush buffers and complete combines, producing new work;
	// loop until a zero crossing injects nothing.
	for {
		select {
		case <-eng.idle:
		case <-ctx.Done():
		}
		if ctx.Err() != nil || eng.exitFlag.Load() {
			break
		}
		if eng.inflight.Load() != 0 {
			continue // stale notification; new work appeared
		}
		injected := false
		for _, e := range eng.graph.Edges {
			if deliverCompletion(e.From, e.To) {
				injected = true
			}
		}
		if !injected {
			break
		}
	}
	cancel()
	wg.Wait()
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
		eng.handle.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeEventLimitExceeded,
			Message: fmt.Sprintf("exceeded maximum event count (%d)", maxEvents),
		})
		cancel()
		return
	}

	switch node.Type {
	case "input":
		sendDownstream(key, ev)

	case "output":
		if err := eng.handle.EmitOutput(ev.data); err != nil {
			eng.exitFlag.Store(true)
			cancel()
		}

	case "exit":
		eng.exitFlag.Store(true)
		if node.Error != nil && *node.Error {
			eng.handle.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeOperationGraphExit,
				Message: "operation graph exit",
				Details: ev.data,
			})
		} else if err := eng.handle.EmitOutput(ev.data); err != nil {
			_ = err // the cancel below tears the engine down either way
		}
		cancel()

	case "operation":
		eng.processConduitEvent(ctx, key, node, ev, startConduit, sendPerEventError, backClosure)

	case "each":
		eng.processEach(ctx, key, node, ev, sendDownstream, sendPerEventError)

	case "filter":
		eng.processFilter(key, node, ev, sendDownstream, sendPerEventError)

	case "transform":
		eng.processTransform(key, node, ev, sendDownstream, sendPerEventError)

	case "map":
		eng.processMap(key, node, ev, sendDownstream, sendPerEventError)

	case "buffer":
		if b := bufferStates[key].add(ev); b != nil {
			sendDownstream(key, &event{data: b.data, source: key, root: b.root, lineage: b.lineage})
		}

	case "combine":
		if snap := combineStates[key].add(ev); snap != nil {
			sendDownstream(key, &event{data: snap.data, source: key, root: snap.root, lineage: snap.lineage})
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
	c.mergeEvent(ev)
	if err := c.call.Write(c.opCtx, ev.data); err != nil {
		ie := openbindings.AsInvocationError(err)
		if ie.Code == openbindings.ErrCodeInputClosed {
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

	call := eng.invoker.Invoke(opCtx, &openbindings.OperationInvocationArgs{
		Interface: eng.args.Interface,
		Operation: node.Operation,
		Context:   eng.args.Context,
	})
	// One write, then close: each fixes the graph's contribution at one
	// write per session. Write/Close failures surface via the read loop.
	_ = call.Write(opCtx, ev.data)
	_ = call.Close()

	out := call.Outputs()
	for {
		v, err := out.Read(opCtx)
		if err == io.EOF {
			return
		}
		if err != nil {
			ie := openbindings.AsInvocationError(err)
			if hasTimeout && opCtx.Err() == context.DeadlineExceeded {
				ie = &openbindings.InvocationError{Code: TimeoutExceeded, Message: fmt.Sprintf("operation %q exceeded its %dms budget", node.Operation, *node.Timeout)}
				call.Cancel()
			}
			if ie.Code == openbindings.ErrCodeCancelled && ctx.Err() != nil {
				return // graph teardown, not a node failure
			}
			sendPerEventError(key, errValue(ie), ev, lineage)
			return
		}
		if eng.exitFlag.Load() {
			out.Stop()
			return
		}
		sendDownstream(key, &event{data: v, source: key, root: ev.root, lineage: copyLineage(lineage)})
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
			sendPerEventError(key, err.Error(), ev, ev.lineage)
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
	if isTruthy(result) {
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
	sendDownstream(key, &event{data: result, source: key, root: ev.root, lineage: copyLineage(ev.lineage)})
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
	arr, ok := toSlice(result)
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
// lineage's root input as $input. An undefined result fails the node with
// TRANSFORM_UNDEFINED; other evaluation failures fail it with their message.
// failed=true means a per-event error was already routed (or dropped).
func (eng *engine) evalOrFail(
	key, expression string, ev *event,
	sendPerEventError func(string, any, *event, map[string]int),
) (any, bool) {
	if eng.transform == nil {
		sendPerEventError(key, "no transform evaluator available", ev, ev.lineage)
		return nil, true
	}
	var result any
	var err error
	if eb, ok := eng.transform.(openbindings.TransformEvaluatorWithBindings); ok {
		bindings := map[string]any{}
		if rv, defined := eng.rootValue(ev.root); defined {
			bindings["input"] = rv
		}
		result, err = eb.EvaluateWithBindings(expression, ev.data, bindings)
	} else {
		result, err = eng.transform.Evaluate(expression, ev.data)
	}
	if err != nil {
		if errors.Is(err, openbindings.ErrTransformUndefined) {
			sendPerEventError(key, TransformUndefined, ev, ev.lineage)
		} else {
			sendPerEventError(key, err.Error(), ev, ev.lineage)
		}
		return nil, true
	}
	return result, false
}

// isTruthy implements JSONata 2.0's boolean cast ($boolean) for filter
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

func toSlice(v any) ([]any, bool) {
	if arr, ok := v.([]any); ok {
		return arr, true
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var arr []any
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, false
	}
	return arr, true
}
