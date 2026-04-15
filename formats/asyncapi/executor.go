package asyncapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// maxRedirects bounds redirect chains for HTTP fetches and SSE/POST executions.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

func newDefaultHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}
}

// Executor handles binding execution for AsyncAPI 3.x sources.
type Executor struct {
	httpClient *http.Client
	mu         sync.RWMutex
	docCache   map[string]*Document
	wsPool     *wsPool
}

// NewExecutor creates a new AsyncAPI binding executor.
func NewExecutor() *Executor {
	return &Executor{
		httpClient: newDefaultHTTPClient(),
		docCache:   make(map[string]*Document),
		wsPool:     newWSPool(),
	}
}

// Close shuts down all pooled WebSocket connections. After Close returns, the
// Executor should not be used for new executions.
func (e *Executor) Close() {
	e.wsPool.closeAll()
}

// cachedLoadDocument loads an AsyncAPI doc, caching by location within a process.
// When content is provided, the cache is bypassed and updated with the fresh parse.
func (e *Executor) cachedLoadDocument(ctx context.Context, location string, content any) (*Document, error) {
	if location != "" && content == nil {
		e.mu.RLock()
		if doc, ok := e.docCache[location]; ok {
			e.mu.RUnlock()
			return doc, nil
		}
		e.mu.RUnlock()
	}

	doc, err := loadDocument(ctx, e.httpClient, location, content)
	if err != nil {
		return nil, err
	}

	if location != "" {
		e.mu.Lock()
		e.docCache[location] = doc
		e.mu.Unlock()
	}
	return doc, nil
}

func resolveServerKey(doc *Document) string {
	serverNames := make([]string, 0, len(doc.Servers))
	for name := range doc.Servers {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)

	for _, name := range serverNames {
		server := doc.Servers[name]
		proto := strings.ToLower(server.Protocol)
		switch proto {
		case "http", "https", "ws", "wss":
			u := proto + "://" + server.Host
			if server.PathName != "" {
				u += server.PathName
			}
			return openbindings.NormalizeContextKey(strings.TrimRight(u, "/"))
		}
	}
	return ""
}

// Formats returns the source formats supported by the AsyncAPI executor.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "AsyncAPI 3.x event-driven APIs"}}
}

// ExecuteBinding executes an AsyncAPI binding, returning a channel of stream events.
// For send actions it performs a unary HTTP POST; for receive actions it subscribes
// via SSE or WebSocket depending on the server protocol.
func (e *Executor) ExecuteBinding(ctx context.Context, in *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	doc, err := e.cachedLoadDocument(ctx, in.Source.Location, in.Source.Content)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(time.Now(), openbindings.ErrCodeSourceLoadFailed, err.Error())), nil
	}

	enriched := in
	if in.Store != nil {
		key := resolveServerKey(doc)
		if key != "" {
			if stored, sErr := in.Store.Get(ctx, key); sErr == nil && len(stored) > 0 {
				cp := *in
				if len(in.Context) == 0 {
					cp.Context = stored
				} else {
					merged := make(map[string]any, len(stored)+len(in.Context))
					for k, v := range stored {
						merged[k] = v
					}
					for k, v := range in.Context {
						merged[k] = v
					}
					cp.Context = merged
				}
				enriched = &cp
			}
		}
	}

	// Determine action from the AsyncAPI doc to choose unary vs streaming path.
	opID, parseErr := parseRef(enriched.Ref)
	if parseErr == nil {
		if asyncOp, ok := doc.Operations[opID]; ok {
			if asyncOp.Action == "receive" {
				return subscribeBindingWithDoc(ctx, e.httpClient, enriched, doc, e.wsPool)
			}
			if asyncOp.Action == "send" {
				_, protocol, _ := resolveServer(doc, enriched.Options)
				if protocol == "ws" || protocol == "wss" {
					return subscribeBindingWithDoc(ctx, e.httpClient, enriched, doc, e.wsPool)
				}
			}
		}
	}

	// Unary path — wrap the ExecuteOutput as a single-event channel.
	result := executeBindingWithDoc(ctx, e.httpClient, enriched, doc)

	// Auth retry: if the API returned auth_required and we have security methods
	// and callbacks, resolve credentials and retry once.
	if result.Error != nil && result.Error.Code == openbindings.ErrCodeAuthRequired &&
		len(enriched.Security) > 0 && enriched.Callbacks != nil {
		creds, resolveErr := openbindings.ResolveSecurity(ctx, enriched.Security, enriched.Callbacks, nil)
		if resolveErr == nil && creds != nil {
			if enriched == in {
				cp := *in
				enriched = &cp
			}
			merged := make(map[string]any)
			for k, v := range enriched.Context {
				merged[k] = v
			}
			for k, v := range creds {
				merged[k] = v
			}
			enriched.Context = merged

			if enriched.Store != nil {
				storeKey := resolveServerKey(doc)
				if storeKey != "" {
					_ = enriched.Store.Set(ctx, storeKey, enriched.Context)
				}
			}

			result = executeBindingWithDoc(ctx, e.httpClient, enriched, doc)
		}
	}

	return openbindings.SingleEventChannel(result), nil
}

// Creator handles interface creation from AsyncAPI documents.
type Creator struct {
	httpClient *http.Client
}

// NewCreator creates a new AsyncAPI interface creator.
func NewCreator() *Creator {
	return &Creator{
		httpClient: newDefaultHTTPClient(),
	}
}

// Formats returns the source formats supported by the AsyncAPI creator.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "AsyncAPI 3.x event-driven APIs"}}
}

// CreateInterface converts an AsyncAPI document to an OpenBindings interface.
func (c *Creator) CreateInterface(ctx context.Context, in *openbindings.CreateInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]
	doc, err := loadDocument(ctx, c.httpClient, src.Location, src.Content)
	if err != nil {
		return nil, err
	}
	return createInterfaceWithDoc(ctx, in, doc)
}
