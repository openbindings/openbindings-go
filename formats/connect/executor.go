// Package connect implements the Connect (Buf) binding format for OpenBindings.
//
// The package handles:
//   - Discovering services from .proto files or inline protobuf definitions
//   - Converting protobuf service descriptors to OpenBindings interfaces
//   - Executing unary RPCs via the Connect protocol (HTTP POST with JSON)
//
// Connect uses the same protobuf service definitions and ref convention as gRPC
// (package.Service/Method) but communicates over HTTP/1.1 or HTTP/2 with a
// simpler wire format. See https://connectrpc.com for protocol details.
package connect

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

const FormatToken = "connect"
const DefaultSourceName = "connectServer"

// maxRedirects bounds the redirect chain a single request may follow.
// Prevents redirect loops without imposing any total request timeout
// (which is the caller's responsibility via context).
const maxRedirects = 10

// Executor handles binding execution for Connect sources.
type Executor struct {
	client *http.Client
}

// NewExecutor creates a new Connect binding executor.
func NewExecutor() *Executor {
	return &Executor{
		client: &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
	}
}

// Formats returns the source formats supported by the Connect executor.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Connect (Buf) via HTTP"}}
}

// ExecuteBinding executes a Connect binding. For unary methods it returns a
// single stream event. For server-streaming methods (detected via the inline
// proto descriptor) it returns a multi-event channel that yields one event per
// server-streamed message and closes when the server's end-stream envelope is
// received or the caller cancels via ctx.
//
// Server-streaming requires the source to provide inline proto `content` so
// the executor can determine that the method is streaming. If no proto content
// is available, the executor falls back to unary execution and the binding
// will fail at runtime if the method is actually streaming.
func (e *Executor) ExecuteBinding(ctx context.Context, in *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	enriched := enrichContext(ctx, in)
	start := time.Now()

	svcName, methodName, err := parseRef(enriched.Ref)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidRef, err.Error())), nil
	}

	// Resolve method descriptor from inline content. The descriptor lets the
	// executor distinguish unary from server-streaming methods and lets it use
	// proto-aware marshaling for accurate field names. If no content is
	// provided, we fall through as unary with generic JSON marshaling.
	var methodDesc *methodInfo
	if enriched.Source.Content != nil {
		desc, parseErr := resolveMethod(enriched.Source.Content, svcName, methodName)
		if parseErr != nil {
			return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeSourceLoadFailed, parseErr.Error())), nil
		}
		methodDesc = desc
	}

	// Server-streaming dispatch.
	if methodDesc != nil && methodDesc.method != nil && methodDesc.method.IsServerStreaming() {
		headers := buildHTTPHeaders(enriched.Context, enriched.Options)
		return executeConnectStreaming(ctx, e.client, enriched.Source.Location, svcName, methodName, enriched.Input, headers, methodDesc, start)
	}

	headers := buildHTTPHeaders(enriched.Context, enriched.Options)
	result := executeConnect(ctx, e.client, enriched.Source.Location, svcName, methodName, enriched.Input, headers, methodDesc, start)

	// Auth retry (unary path only — streaming auth retry is harder because
	// the server may have already started writing frames before the auth
	// failure surfaces, and we can't replay the stream).
	if result.Error != nil && result.Error.Code == openbindings.ErrCodeAuthRequired &&
		len(enriched.Security) > 0 && enriched.Callbacks != nil {
		creds, resolveErr := openbindings.ResolveSecurity(ctx, enriched.Security, enriched.Callbacks, nil)
		if resolveErr == nil && creds != nil {
			cp := *enriched
			merged := make(map[string]any)
			for k, v := range enriched.Context {
				merged[k] = v
			}
			for k, v := range creds {
				merged[k] = v
			}
			cp.Context = merged

			if cp.Store != nil {
				storeKey := normalizeEndpoint(cp.Source.Location)
				if storeKey != "" {
					_ = cp.Store.Set(ctx, storeKey, cp.Context)
				}
			}

			headers = buildHTTPHeaders(cp.Context, cp.Options)
			result = executeConnect(ctx, e.client, cp.Source.Location, svcName, methodName, cp.Input, headers, methodDesc, start)

			// On retry failure, wrap the error message with operation context
			// so the consumer knows which call failed. The original message
			// from the underlying RPC is preserved.
			if result.Error != nil {
				result.Error.Message = fmt.Sprintf("connect retry %s/%s at %s: %s", svcName, methodName, cp.Source.Location, result.Error.Message)
			}
		}
	}

	return openbindings.SingleEventChannel(result), nil
}

// Creator handles interface creation from protobuf definitions for the Connect format.
type Creator struct{}

// NewCreator creates a new Connect interface creator.
func NewCreator() *Creator { return &Creator{} }

// Formats returns the source formats supported by the Connect creator.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{Token: FormatToken, Description: "Connect (Buf) via HTTP"}}
}

// CreateInterface parses a .proto file or inline content and converts to an
// OpenBindings interface with Connect bindings.
func (c *Creator) CreateInterface(ctx context.Context, in *openbindings.CreateInput) (*openbindings.Interface, error) {
	if len(in.Sources) == 0 {
		return nil, openbindings.ErrNoSources
	}
	src := in.Sources[0]
	if src.Location == "" && src.Content == nil {
		return nil, fmt.Errorf("Connect source requires a location or content")
	}

	disc, err := discoverFromProto(src.Location, src.Content)
	if err != nil {
		return nil, fmt.Errorf("Connect proto parse: %w", err)
	}

	iface, err := convertToInterface(disc, src.Location)
	if err != nil {
		return nil, fmt.Errorf("Connect convert: %w", err)
	}
	if in.Name != "" {
		iface.Name = in.Name
	}
	if in.Version != "" {
		iface.Version = in.Version
	}
	if in.Description != "" {
		iface.Description = in.Description
	}
	return &iface, nil
}

func enrichContext(ctx context.Context, in *openbindings.BindingExecutionInput) *openbindings.BindingExecutionInput {
	if in.Store == nil {
		return in
	}
	key := normalizeEndpoint(in.Source.Location)
	if key == "" {
		return in
	}
	stored, err := in.Store.Get(ctx, key)
	if err != nil || len(stored) == 0 {
		return in
	}
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
	return &cp
}

func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return openbindings.NormalizeContextKey(u.Host)
	}
	return openbindings.NormalizeContextKey(endpoint)
}
