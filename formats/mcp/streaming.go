package mcp

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

// nextProgressToken provides a unique progress token per executeToolStreaming
// invocation. MCP servers correlate notifications/progress messages back to
// the request that originated the token.
var nextProgressToken atomic.Int64

// executeToolStreaming calls an MCP tool using a pooled session and forwards any
// `notifications/progress` events the server sends during the call as
// intermediate stream events. The final tool result (or error) is emitted as
// the last stream event before the channel closes.
//
// Progress events are surfaced as a JSON object containing the MCP
// ProgressNotificationParams fields (`progressToken`, `progress`, `total`,
// `message`). Consumers can distinguish a progress event from the final
// result by the presence of the `progressToken` field.
//
// If the underlying MCP server does not emit progress notifications (the
// common case for fast tools), the channel simply yields one event -- the
// final result -- and closes, behaviorally identical to a unary tool call.
//
// The session is acquired from the pool on entry and released when the call
// completes. Multiple concurrent tool calls to the same server share a single
// MCP session and initialize handshake.
func executeToolStreaming(ctx context.Context, pool *sessionPool, clientVersion string, url string, toolName string, args map[string]any, headers map[string]string) <-chan openbindings.StreamEvent {
	ch := make(chan openbindings.StreamEvent, 32)

	// sendMu + closed guard the channel against concurrent sends from the
	// progress handler and the main goroutine. The progress handler acquires
	// sendMu and checks closed before every send; the main goroutine sets
	// closed=true under sendMu before closing the channel. This eliminates
	// the data race that existed with the atomic-bool approach.
	var sendMu sync.Mutex
	closed := false

	trySend := func(ev openbindings.StreamEvent) {
		sendMu.Lock()
		defer sendMu.Unlock()
		if closed {
			return
		}
		// Non-blocking: if the buffer is full, drop. Progress notifications
		// are advisory and losing one is acceptable.
		select {
		case ch <- ev:
		default:
		}
	}

	closeCh := func() {
		sendMu.Lock()
		defer sendMu.Unlock()
		closed = true
		close(ch)
	}

	// Each call gets a fresh progress token so the server can correlate
	// notifications to this specific invocation.
	progressToken := fmt.Sprintf("ob-progress-%d", nextProgressToken.Add(1))

	go func() {
		session, err := pool.acquire(ctx, clientVersion, url, headers)
		if err != nil {
			ch <- openbindings.StreamEvent{
				Status: 1,
				Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeConnectFailed,
					Message: fmt.Sprintf("connect to MCP server: %v", err),
				},
			}
			closeCh()
			return
		}

		// Register a per-call progress handler keyed by this call's unique
		// progress token. The session's demux handler routes notifications
		// to the right call.
		session.registerProgress(progressToken, func(handlerCtx context.Context, req *gomcp.ProgressNotificationClientRequest) {
			if req == nil || req.Params == nil {
				return
			}

			params := req.Params
			trySend(openbindings.StreamEvent{Data: map[string]any{
				"progressToken": params.ProgressToken,
				"progress":      params.Progress,
				"total":         params.Total,
				"message":       params.Message,
			}})
		})

		// Cleanup order matters: unregister the progress handler first so no
		// new handler calls begin, then close the channel (trySend guards
		// any in-flight handler from sending on a closed channel), then
		// release the session.
		defer session.release()
		defer closeCh()
		defer session.unregisterProgress(progressToken)

		// Note: Meta must be pre-initialized so SetProgressToken's mutation
		// is visible. The upstream gomcp helper allocates a local map when
		// Meta is nil but never writes it back to the params struct, so a
		// nil Meta silently swallows the token.
		params := &gomcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
			Meta:      gomcp.Meta{},
		}
		params.SetProgressToken(progressToken)

		result, callErr := session.session.CallTool(ctx, params)

		if callErr != nil {
			ch <- openbindings.StreamEvent{
				Status: 1,
				Error: &openbindings.ExecuteError{
					Code:    mcpErrorCode(callErr),
					Message: callErr.Error(),
				},
			}
			return
		}

		out := callToolResultToOutput(result)
		ev := openbindings.StreamEvent{
			Data:   out.Output,
			Status: out.Status,
		}
		if out.Error != nil {
			ev.Error = out.Error
		}
		ch <- ev
	}()

	return ch
}

// startsWithHTTP checks whether a string starts with http:// or https://.
func startsWithHTTP(s string) bool {
	return len(s) >= 7 && (s[:7] == "http://" || (len(s) >= 8 && s[:8] == "https://"))
}
