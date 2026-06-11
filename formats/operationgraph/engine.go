package operationgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	openbindings "github.com/openbindings/openbindings-go"
)

const (
	// maxEvents is the maximum number of data events processed per graph invocation.
	// Protects against unbounded event amplification from map nodes in cycles.
	maxEvents int64 = 100_000

	// maxErrorDepth is the maximum depth of onError routing chains.
	// Protects against unbounded error processing cascades.
	maxErrorDepth = 32
)

// event is an internal event flowing through the graph.
type event struct {
	data       any
	source     string         // node key that produced this event (for combine)
	lineage    map[string]int // node key -> iteration count (for maxIterations)
	complete   bool           // true = completion marker, not a data event
	errorDepth int            // tracks onError chain depth
}

func cloneEvent(ev *event) *event {
	lin := make(map[string]int, len(ev.lineage))
	for k, v := range ev.lineage {
		lin[k] = v
	}
	return &event{data: ev.data, source: ev.source, lineage: lin, errorDepth: ev.errorDepth}
}

// engine runs a single operation graph invocation.
type engine struct {
	graph     *Graph
	invoker   *openbindings.OperationInvoker
	args      *openbindings.BindingInvocationArgs
	transform openbindings.TransformEvaluator
	handle    openbindings.BindingHandle[any, any]
	origInput any
	schemas   *schemaCache

	outEdges map[string][]string
	inEdges  map[string][]string
	inputKey string

	exitFlag   atomic.Bool
	inflight   atomic.Int64
	eventCount atomic.Int64
	doneOnce   sync.Once
	done       chan struct{}
}

func newEngine(g *Graph, invoker *openbindings.OperationInvoker, args *openbindings.BindingInvocationArgs, te openbindings.TransformEvaluator, sc *schemaCache) *engine {
	outE := make(map[string][]string)
	inE := make(map[string][]string)
	var inputKey string
	for _, e := range g.Edges {
		outE[e.From] = append(outE[e.From], e.To)
		inE[e.To] = append(inE[e.To], e.From)
	}
	for k, n := range g.Nodes {
		if n.Type == "input" {
			inputKey = k
		}
	}
	return &engine{
		graph:     g,
		invoker:   invoker,
		args:      args,
		transform: te,
		schemas:   sc,
		outEdges:  outE,
		inEdges:   inE,
		inputKey:  inputKey,
		done:      make(chan struct{}),
	}
}

func (eng *engine) incInflight() {
	eng.inflight.Add(1)
}

func (eng *engine) decInflight() {
	if eng.inflight.Add(-1) == 0 {
		eng.doneOnce.Do(func() { close(eng.done) })
	}
}

// execute drives one graph invocation behind the binding handle: it
// validates the graph, seeds the initial input, runs the node workers, and
// settles the handle's terminal (CloseOutput on completion; FireError on
// terminal failure inside the engine).
func (eng *engine) execute(ctx context.Context, handle openbindings.BindingHandle[any, any]) {
	eng.handle = handle

	// Validate before consuming any input, so validation failures surface
	// without the caller having to write anything.
	// When Interface is nil (e.g. direct binding invocation via host), skip
	// operation key validation -- references will fail at runtime if invalid.
	var opKeys map[string]bool
	if eng.args.Interface != nil {
		opKeys = make(map[string]bool)
		for k := range eng.args.Interface.Operations {
			opKeys[k] = true
		}
	}
	if err := Validate(eng.graph, opKeys); err != nil {
		handle.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: err.Error(),
		})
		return
	}

	// Seed the graph's initial input. No-input convention: when the call
	// came through the operation layer (Binding != nil) and the operation
	// declares no input (InputSchema == nil), close input on entry and seed
	// nil -- reading would deadlock a caller that never writes.
	if eng.args.Binding != nil && eng.args.InputSchema == nil {
		_ = handle.CloseInput()
	} else {
		v, err := handle.ReadInput(ctx)
		if err != nil {
			if err != io.EOF {
				return // invocation already terminal (cancelled/errored)
			}
			// Input closed with no message: the graph runs with nil input.
		} else {
			eng.origInput = v
			// The graph takes exactly one input.
			_ = handle.CloseInput()
		}
	}

	// runCtx tears down all workers when the invocation terminates (caller
	// Cancel, abandoned output stream, or upstream ctx cancellation).
	runCtx, stop := openbindings.DoneContext(ctx, handle.Done())
	defer stop()
	eng.run(runCtx)

	// Normal completion. No-op when the engine already fired a terminal
	// error (exit-node error, event limit) or the invocation was cancelled.
	handle.CloseOutput()
}

// run executes the graph, emitting output events through eng.handle.
func (eng *engine) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Node mailboxes.
	mailboxes := make(map[string]chan *event)
	for key := range eng.graph.Nodes {
		mailboxes[key] = make(chan *event, 256)
	}

	// Buffer and combine state.
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

	// sendToNode sends an event to a node's mailbox, incrementing in-flight first.
	sendToNode := func(toKey string, ev *event) {
		eng.incInflight()
		select {
		case mailboxes[toKey] <- ev:
		case <-ctx.Done():
			eng.decInflight()
		}
	}

	// sendDownstream fans out an event to all downstream nodes.
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

	// sendCompletion sends completion markers to all downstream nodes via mailboxes.
	// Using mailboxes ensures FIFO ordering: all data events from this source
	// arrive before the completion marker at the downstream node.
	sendCompletion := func(fromKey string) {
		for _, toKey := range eng.outEdges[fromKey] {
			if eng.exitFlag.Load() {
				return
			}
			sendToNode(toKey, &event{source: fromKey, complete: true})
		}
	}

	// sendError routes an error to the onError target or drops it silently.
	// Preserves the failing event's lineage for correct maxIterations in error paths.
	sendError := func(nodeKey, errMsg string, input any, lineage map[string]int, errorDepth int) {
		node := eng.graph.Nodes[nodeKey]
		if node.OnError == "" {
			return
		}
		if errorDepth >= maxErrorDepth {
			return // drop to prevent unbounded error chains
		}
		sendToNode(node.OnError, &event{
			data:       map[string]any{"error": errMsg, "input": input},
			source:     nodeKey,
			lineage:    copyLineage(lineage),
			errorDepth: errorDepth + 1,
		})
	}

	// handleCompletion processes a completion marker arriving at a node.
	// When all of a node's upstream sources have completed, it flushes
	// buffer/combine state and propagates completion downstream.
	handleCompletion := func(key string, node *Node) {
		counter, ok := completedSources[key]
		if !ok {
			return
		}
		newCount := int(counter.Add(1))
		if newCount < len(eng.inEdges[key]) {
			return // not all upstream sources complete yet
		}
		// All upstream sources complete.
		switch node.Type {
		case "buffer":
			if batch := bufferStates[key].flush(); batch != nil {
				sendDownstream(key, &event{data: batch, source: key, lineage: make(map[string]int)})
			}
		case "combine":
			if result, ok := combineStates[key].complete(); ok {
				sendDownstream(key, &event{data: result, source: key, lineage: make(map[string]int)})
			}
		}
		sendCompletion(key)
	}

	// Start a worker goroutine per node.
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
				case ev, ok := <-mailboxes[nodeKey]:
					if !ok {
						return
					}
					if eng.exitFlag.Load() {
						eng.decInflight()
						return
					}
					eng.processNode(ctx, nodeKey, node, ev, cancel,
						sendDownstream, sendCompletion, sendError, handleCompletion,
						bufferStates, combineStates)
					eng.decInflight()
				}
			}
		}()
	}

	// Inject initial event.
	eng.incInflight()
	mailboxes[eng.inputKey] <- &event{data: eng.origInput, lineage: make(map[string]int)}

	// Wait for completion or cancellation.
	select {
	case <-eng.done:
	case <-ctx.Done():
	}

	// Cancel context to stop all workers, then wait for them to exit.
	// Do not close mailbox channels directly -- goroutines may still be
	// sending to them. Context cancellation is the safe shutdown signal.
	cancel()
	wg.Wait()
}

// processNode handles a single event arriving at a node.
func (eng *engine) processNode(
	ctx context.Context,
	key string, node *Node, ev *event,
	cancel context.CancelFunc,
	sendDownstream func(string, *event),
	sendCompletion func(string),
	sendError func(string, string, any, map[string]int, int),
	handleCompletion func(string, *Node),
	bufferStates map[string]*bufferState,
	combineStates map[string]*combineState,
) {
	// Handle completion markers.
	if ev.complete {
		handleCompletion(key, node)
		return
	}

	// Check event amplification limit.
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
		sendCompletion(key)

	case "output":
		if err := eng.handle.EmitOutput(ev.data); err != nil {
			// The invocation terminated while emitting: abort the engine.
			eng.exitFlag.Store(true)
			cancel()
		}

	case "exit":
		eng.exitFlag.Store(true)
		isError := node.Error != nil && *node.Error
		if isError {
			eng.handle.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeOperationGraphExit,
				Message: fmt.Sprintf("%v", ev.data),
			})
		} else if err := eng.handle.EmitOutput(ev.data); err != nil {
			// The invocation terminated while emitting the exit value. The
			// exit flag is already set and the cancel() below tears the engine
			// down; observing the error keeps parity with the output node
			// rather than silently discarding it with `_ =`.
			_ = err
		}
		cancel()

	case "operation":
		eng.processOperation(ctx, key, node, ev, sendDownstream, sendError)

	case "filter":
		eng.processFilter(key, node, ev, sendDownstream, sendError)

	case "transform":
		eng.processTransform(key, node, ev, sendDownstream, sendError)

	case "map":
		eng.processMap(key, node, ev, sendDownstream, sendError)

	case "buffer":
		bs := bufferStates[key]
		for _, batch := range bs.add(ev) {
			sendDownstream(key, &event{data: batch, source: key, lineage: make(map[string]int)})
		}

	case "combine":
		cs := combineStates[key]
		if result, ok := cs.add(ev); ok {
			sendDownstream(key, &event{data: result, source: key, lineage: make(map[string]int)})
		}
	}
}

func (eng *engine) processOperation(
	ctx context.Context, key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendError func(string, string, any, map[string]int, int),
) {
	// Check maxIterations. Copy lineage before mutating.
	lineage := copyLineage(ev.lineage)
	if node.MaxIterations != nil {
		count := lineage[key]
		if count >= *node.MaxIterations {
			return // safety bound, not an error
		}
		lineage[key] = count + 1
	}

	// opCtx chains the graph's cancellation (and the per-node timeout) into
	// the sub-operation: tearing down the graph tears down in-flight calls.
	opCtx := ctx
	var opCancel context.CancelFunc
	if node.Timeout != nil {
		opCtx, opCancel = context.WithTimeout(ctx, msToDuration(*node.Timeout))
		defer opCancel()
	}

	call := eng.invoker.Invoke(opCtx, &openbindings.OperationInvocationArgs{
		Interface: eng.args.Interface,
		Operation: node.Operation,
		Context:   eng.args.Context,
	})
	if ev.data != nil {
		// A Write failure is either ERR_INPUT_CLOSED (the binding closed its
		// input deliberately; benign) or the invocation's terminal error,
		// which the read loop below surfaces. Either way the read loop owns
		// reporting.
		_ = call.Write(opCtx, ev.data)
	}
	_ = call.Close() // idempotent; safe even when the binding already closed input

	out := call.Outputs()
	for {
		v, err := out.Read(opCtx)
		if err == io.EOF {
			return
		}
		if err != nil {
			sendError(key, openbindings.AsInvocationError(err).Error(), ev.data, ev.lineage, ev.errorDepth)
			return
		}
		if eng.exitFlag.Load() {
			out.Stop()
			return
		}
		sendDownstream(key, &event{data: v, source: key, lineage: copyLineage(lineage)})
	}
}

func (eng *engine) processFilter(
	key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendError func(string, string, any, map[string]int, int),
) {
	if node.Schema != nil {
		passes, err := eng.schemas.match(node.Schema, ev.data)
		if err != nil {
			sendError(key, err.Error(), ev.data, ev.lineage, ev.errorDepth)
			return
		}
		if passes {
			sendDownstream(key, ev)
		}
		return
	}
	if node.Transform != nil {
		if eng.transform == nil {
			sendError(key, "no transform evaluator available", ev.data, ev.lineage, ev.errorDepth)
			return
		}
		result, err := eng.evaluateTransform(*node.Transform, ev.data)
		if err != nil {
			sendError(key, err.Error(), ev.data, ev.lineage, ev.errorDepth)
			return
		}
		if isTruthy(result) {
			sendDownstream(key, ev)
		}
	}
}

func (eng *engine) processTransform(
	key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendError func(string, string, any, map[string]int, int),
) {
	if eng.transform == nil {
		sendError(key, "no transform evaluator available", ev.data, ev.lineage, ev.errorDepth)
		return
	}
	result, err := eng.evaluateTransform(*node.Transform, ev.data)
	if err != nil {
		sendError(key, err.Error(), ev.data, ev.lineage, ev.errorDepth)
		return
	}
	sendDownstream(key, &event{data: result, source: key, lineage: copyLineage(ev.lineage)})
}

func (eng *engine) processMap(
	key string, node *Node, ev *event,
	sendDownstream func(string, *event),
	sendError func(string, string, any, map[string]int, int),
) {
	if eng.transform == nil {
		sendError(key, "no transform evaluator available", ev.data, ev.lineage, ev.errorDepth)
		return
	}
	result, err := eng.evaluateTransform(*node.Transform, ev.data)
	if err != nil {
		sendError(key, err.Error(), ev.data, ev.lineage, ev.errorDepth)
		return
	}
	arr, ok := toSlice(result)
	if !ok {
		sendError(key, openbindings.ErrCodeMapNotArray, ev.data, ev.lineage, ev.errorDepth)
		return
	}
	for _, item := range arr {
		if eng.exitFlag.Load() {
			return
		}
		sendDownstream(key, &event{data: item, source: key, lineage: copyLineage(ev.lineage)})
	}
}

func (eng *engine) evaluateTransform(expression string, data any) (any, error) {
	if eb, ok := eng.transform.(openbindings.TransformEvaluatorWithBindings); ok {
		return eb.EvaluateWithBindings(expression, data, map[string]any{
			"input": eng.origInput,
		})
	}
	return eng.transform.Evaluate(expression, data)
}

func copyLineage(m map[string]int) map[string]int {
	cp := make(map[string]int, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

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
