package operationgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// FormatToken identifies this package as an operation graph handler.
const FormatToken = "openbindings.operation-graph@0.2.0"

// Invoker handles binding invocation for operation graph sources.
type Invoker struct {
	invoker  *openbindings.OperationInvoker
	mu       sync.RWMutex
	docCache map[string]*Document
	schemas  *schemaCache
	client   *http.Client
}

// NewInvoker creates a new operation graph binding invoker.
// The OperationInvoker is used to invoke sub-operations referenced by
// operation nodes in the graph.
func NewInvoker(invoker *openbindings.OperationInvoker) *Invoker {
	return &Invoker{
		invoker:  invoker,
		docCache: make(map[string]*Document),
		schemas:  newSchemaCache(),
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("stopped after 10 redirects")
				}
				return nil
			},
		},
	}
}

// maxGraphDocBytes bounds a graph document fetched from a remote location.
const maxGraphDocBytes = 8 << 20 // 8 MiB

// Formats returns the binding format tokens this invoker supports.
func (e *Invoker) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "OpenBindings operation graphs"}}
}

// InvokeBinding invokes an operation graph binding. The handle is returned
// synchronously; the graph engine runs on its own goroutine. The graph's
// initial input is the first message written to the handle (or nil when the
// operation layer declares a no-input operation), and graph events stream
// out through the handle's output side.
func (e *Invoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	doc, err := e.loadDocument(args.Source.Location, args.Source.Content)
	if err != nil {
		return openbindings.NewErroredInvocation[any, any](&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceLoadFailed,
			Message: err.Error(),
		})
	}

	graph, ok := doc.Graphs[args.Ref]
	if !ok {
		return openbindings.NewErroredInvocation[any, any](&openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("operation graph %q not found in document", args.Ref),
		})
	}

	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go func() {
		eng := newEngine(graph, e.invoker, args, e.invoker.TransformEvaluator, e.schemas)
		eng.execute(ctx, inv)
	}()
	return inv
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
		if location == "" {
			return nil, fmt.Errorf("source must have location or content")
		}
		var err error
		data, err = e.loadLocation(location)
		if err != nil {
			return nil, err
		}
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

// loadLocation reads a graph document from an http(s) URL or a local file
// path (bare path or file:// URI). Remote responses are size-bounded.
func (e *Invoker) loadLocation(location string) ([]byte, error) {
	if openbindings.IsHTTPURL(location) {
		req, err := http.NewRequest(http.MethodGet, location, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid location %q: %w", location, err)
		}
		resp, err := e.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch operation graph %q: %w", location, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("fetch operation graph %q: HTTP %d", location, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxGraphDocBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read operation graph %q: %w", location, err)
		}
		if len(data) > maxGraphDocBytes {
			return nil, fmt.Errorf("operation graph %q exceeds %d bytes", location, maxGraphDocBytes)
		}
		return data, nil
	}

	path := strings.TrimPrefix(location, "file://")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read operation graph %q: %w", location, err)
	}
	return data, nil
}
