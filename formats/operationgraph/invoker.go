package operationgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// FormatToken identifies this package as an operation graph handler.
const FormatToken = "openbindings.operation-graph@0.2.0"

// Invoker handles binding invocation for operation graph sources.
type Invoker struct {
	invoker *openbindings.OperationInvoker
	mu         sync.RWMutex
	docCache   map[string]*Document
	schemas    *schemaCache
}

// NewInvoker creates a new operation graph binding invoker.
// The OperationInvoker is used to invoke sub-operations referenced by
// operation nodes in the graph.
func NewInvoker(invoker *openbindings.OperationInvoker) *Invoker {
	return &Invoker{
		invoker: invoker,
		docCache:   make(map[string]*Document),
		schemas:    newSchemaCache(),
	}
}

// Formats returns the binding format tokens this driver supports.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "OpenBindings operation graphs"}}
}

// InvokeBinding invokes an operation graph binding.
func (e *Invoker) InvokeBinding(ctx context.Context, in *openbindings.BindingInvocationInput) (<-chan openbindings.InvocationOutput, error) {
	doc, err := e.loadDocument(in.Source.Location, in.Source.Content)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(
			time.Now(), openbindings.ErrCodeSourceLoadFailed, err.Error(),
		)), nil
	}

	graph, ok := doc.Graphs[in.Ref]
	if !ok {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(
			time.Now(), openbindings.ErrCodeRefNotFound,
			fmt.Sprintf("operation graph %q not found in document", in.Ref),
		)), nil
	}

	out := make(chan openbindings.InvocationOutput)
	go func() {
		defer close(out)
		eng := newEngine(graph, e.invoker, in, e.invoker.TransformEvaluator, e.schemas)
		eng.run(ctx, out)
	}()
	return out, nil
}

func (e *Invoker) loadDocument(location string, content any) (*Document, error) {
	if location != "" && content == nil {
		e.mu.RLock()
		if doc, ok := e.docCache[location]; ok {
			e.mu.RUnlock()
			return doc, nil
		}
		e.mu.RUnlock()
	}

	var data []byte
	switch v := content.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	case json.RawMessage:
		data = []byte(v)
	case map[string]any:
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal inline content: %w", err)
		}
	default:
		if content != nil {
			var err error
			data, err = json.Marshal(content)
			if err != nil {
				return nil, fmt.Errorf("marshal content: %w", err)
			}
		}
	}

	if data == nil {
		return nil, fmt.Errorf("no content or location provided")
	}

	doc, err := ParseDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse operation graph: %w", err)
	}

	if location != "" {
		e.mu.Lock()
		e.docCache[location] = doc
		e.mu.Unlock()
	}
	return doc, nil
}
