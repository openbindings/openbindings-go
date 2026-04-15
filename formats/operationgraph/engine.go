package operationgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	openbindings "github.com/openbindings/openbindings-go"
)

const (
	// maxEvents is the maximum number of data events processed per graph execution.
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

// engine runs a single operation graph execution.
type engine struct {
	graph     *Graph
	opExec    *openbindings.OperationExecutor
	bindingIn *openbindings.BindingExecutionInput
	transform openbindings.TransformEvaluator
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

func newEngine(g *Graph, opExec *openbindings.OperationExecutor, in *openbindings.BindingExecutionInput, te openbindings.TransformEvaluator, sc *schemaCache) *engine {
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
		opExec:    opExec,
		bindingIn: in,
		transform: te,
		origInput: in.Input,
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

// run validates and executes the graph, sending output events to out.
func (eng *engine) run(ctx context.Context, out chan<- openbindings.StreamEvent) {
	// Validate before executing.
	// When Interface is nil (e.g. direct binding execution via host), skip
	// operation key validation -- references will fail at runtime if invalid.
	var opKeys map[string]bool
	if eng.bindingIn.Interface != nil {
		opKeys = make(map[string]bool)
		for k := range eng.bindingIn.Interface.Operations {
			opKeys[k] = true
		}
	}
	if err := Validate(eng.graph, opKeys); err != nil {
		out <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: err.Error(),
		}}
		return
	}

	ctx, cancel := context.WithCancel(ctx)

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
					eng.processNode(ctx, nodeKey, node, ev, out, cancel,
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
	out chan<- openbindings.StreamEvent,
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
		out <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
			Code:    openbindings.ErrCodeEventLimitExceeded,
			Message: fmt.Sprintf("exceeded maximum event count (%d)", maxEvents),
		}}
		cancel()
		return
	}

	switch node.Type {
	case "input":
		sendDownstream(key, ev)
		sendCompletion(key)

	case "output":
		out <- openbindings.StreamEvent{Data: ev.data}

	case "exit":
		eng.exitFlag.Store(true)
		isError := node.Error != nil && *node.Error
		if isError {
			out <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeOperationGraphExit,
				Message: fmt.Sprintf("%v", ev.data),
			}}
		} else {
			out <- openbindings.StreamEvent{Data: ev.data}
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

	opCtx := ctx
	var opCancel context.CancelFunc
	if node.Timeout != nil {
		opCtx, opCancel = context.WithTimeout(ctx, msToDuration(*node.Timeout))
		defer opCancel()
	}

	ch, err := eng.opExec.ExecuteOperation(opCtx, &openbindings.OperationExecutionInput{
		Interface: eng.bindingIn.Interface,
		Operation: node.Operation,
		Input:     ev.data,
		Context:   eng.bindingIn.Context,
		Options:   eng.bindingIn.Options,
	})
	if err != nil {
		sendError(key, err.Error(), ev.data, ev.lineage, ev.errorDepth)
		return
	}

	for streamEv := range ch {
		if eng.exitFlag.Load() {
			return
		}
		if streamEv.Error != nil {
			sendError(key, streamEv.Error.Message, ev.data, ev.lineage, ev.errorDepth)
			continue
		}
		sendDownstream(key, &event{data: streamEv.Data, source: key, lineage: copyLineage(lineage)})
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
		result, err := eng.evaluateTransform(node.Transform, ev.data)
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
	result, err := eng.evaluateTransform(node.Transform, ev.data)
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
	result, err := eng.evaluateTransform(node.Transform, ev.data)
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

func (eng *engine) evaluateTransform(td *TransformDef, data any) (any, error) {
	if eb, ok := eng.transform.(openbindings.TransformEvaluatorWithBindings); ok {
		return eb.EvaluateWithBindings(td.Expression, data, map[string]any{
			"input": eng.origInput,
		})
	}
	return eng.transform.Evaluate(td.Expression, data)
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
