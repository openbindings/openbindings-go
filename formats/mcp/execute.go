package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

const (
	refPrefixTools     = "tools/"
	refPrefixResources = "resources/"
	refPrefixPrompts   = "prompts/"
)

// execute dispatches an MCP binding to the appropriate entity-type handler
// and returns a stream of events. Tool calls return a multi-event channel
// that yields one event per `notifications/progress` notification followed by
// the final tool result. Resources and prompts return a single-event channel.
//
// Returns a channel rather than an *ExecuteOutput so the streaming tool path
// can surface progress notifications as intermediate events. The previous
// behavior (one event per call) is preserved for resources and prompts.
func execute(ctx context.Context, pool *sessionPool, clientVersion string, url string, ref string, input any, headers map[string]string) <-chan openbindings.StreamEvent {
	start := time.Now()

	entityType, name, err := parseRef(ref)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidRef, err.Error()))
	}

	switch entityType {
	case "tools":
		// Validate and shallow-copy the input map up front, then dispatch to
		// the streaming tool path. Argument validation lives here (rather
		// than inside executeToolStreaming) so it stays testable in
		// isolation and so invalid-input errors are emitted as a clean
		// single-event stream.
		args, ok := openbindings.ToStringAnyMap(input)
		if input != nil && !ok {
			return openbindings.SingleEventChannel(&openbindings.ExecuteOutput{
				Status: 1,
				Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeInvalidInput,
					Message: fmt.Sprintf("tool input must be an object, got %T", input),
				},
				DurationMs: time.Since(start).Milliseconds(),
			})
		}
		// MCP servers expect an object for arguments, never null. Defensive
		// shallow copy keeps the executor contract ("never mutate caller
		// input") even when the third-party MCP SDK passes args by reference.
		if args == nil {
			args = map[string]any{}
		} else {
			cp := make(map[string]any, len(args))
			for k, v := range args {
				cp[k] = v
			}
			args = cp
		}
		return executeToolStreaming(ctx, pool, clientVersion, url, name, args, headers)

	case "resources":
		out := executeResource(ctx, pool, clientVersion, url, name, headers)
		out.DurationMs = time.Since(start).Milliseconds()
		return openbindings.SingleEventChannel(out)

	case "prompts":
		out := executePrompt(ctx, pool, clientVersion, url, name, input, headers)
		out.DurationMs = time.Since(start).Milliseconds()
		return openbindings.SingleEventChannel(out)

	default:
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidRef, fmt.Sprintf("unsupported MCP entity type %q", entityType)))
	}
}

// parseRef extracts the entity type and name from an MCP ref.
// Returns (entityType, name, error).
// Examples:
//
//	"tools/get_weather"              → ("tools", "get_weather", nil)
//	"resources/file:///src/main.rs"  → ("resources", "file:///src/main.rs", nil)
//	"prompts/code_review"            → ("prompts", "code_review", nil)
func parseRef(ref string) (entityType string, name string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty MCP ref")
	}

	for _, prefix := range []string{refPrefixTools, refPrefixResources, refPrefixPrompts} {
		if strings.HasPrefix(ref, prefix) {
			name := strings.TrimPrefix(ref, prefix)
			if name == "" {
				return "", "", fmt.Errorf("empty name in MCP ref %q", ref)
			}
			entityType := strings.TrimSuffix(prefix, "/")
			return entityType, name, nil
		}
	}

	return "", "", fmt.Errorf("MCP ref %q must start with %q, %q, or %q",
		ref, refPrefixTools, refPrefixResources, refPrefixPrompts)
}

func executeResource(ctx context.Context, pool *sessionPool, clientVersion string, url string, uri string, headers map[string]string) *openbindings.ExecuteOutput {
	result, err := readResourcePooled(ctx, pool, clientVersion, url, uri, headers)
	if err != nil {
		return &openbindings.ExecuteOutput{
			Status: 1,
			Error: &openbindings.ExecuteError{
				Code:    mcpErrorCode(err),
				Message: err.Error(),
			},
		}
	}

	return readResourceResultToOutput(result)
}

func executePrompt(ctx context.Context, pool *sessionPool, clientVersion string, url string, promptName string, input any, headers map[string]string) *openbindings.ExecuteOutput {
	args, err := toStringStringMap(input)
	if err != nil {
		return &openbindings.ExecuteOutput{
			Status: 1,
			Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeInvalidInput,
				Message: fmt.Sprintf("prompt arguments must be an object with string values: %v", err),
			},
		}
	}

	result, err := getPromptPooled(ctx, pool, clientVersion, url, promptName, args, headers)
	if err != nil {
		return &openbindings.ExecuteOutput{
			Status: 1,
			Error: &openbindings.ExecuteError{
				Code:    mcpErrorCode(err),
				Message: err.Error(),
			},
		}
	}

	return getPromptResultToOutput(result)
}

func callToolResultToOutput(result *gomcp.CallToolResult) *openbindings.ExecuteOutput {
	// IsError is an application-level tool error, not a transport error.
	// Status is always 200 for a successful MCP call; the caller can inspect
	// the output or Error field to distinguish application-level failures.

	// Prefer structured content if available.
	if result.StructuredContent != nil {
		switch sc := result.StructuredContent.(type) {
		case json.RawMessage:
			var structured any
			if json.Unmarshal(sc, &structured) == nil {
				return &openbindings.ExecuteOutput{Output: structured, Status: 200}
			}
		default:
			return &openbindings.ExecuteOutput{Output: sc, Status: 200}
		}
	}

	output := extractContent(result.Content)
	return &openbindings.ExecuteOutput{
		Output: output,
		Status: 200,
	}
}

func readResourceResultToOutput(result *gomcp.ReadResourceResult) *openbindings.ExecuteOutput {
	if len(result.Contents) == 0 {
		return &openbindings.ExecuteOutput{Status: 200}
	}

	if len(result.Contents) == 1 {
		c := result.Contents[0]
		if c.Text != "" {
			var parsed any
			if json.Unmarshal([]byte(c.Text), &parsed) == nil {
				return &openbindings.ExecuteOutput{Output: parsed, Status: 200}
			}
			return &openbindings.ExecuteOutput{Output: c.Text, Status: 200}
		}
		return &openbindings.ExecuteOutput{Output: map[string]any{"uri": c.URI, "mimeType": c.MIMEType}, Status: 200}
	}

	var items []any
	for _, c := range result.Contents {
		items = append(items, map[string]any{
			"uri":      c.URI,
			"mimeType": c.MIMEType,
			"text":     c.Text,
		})
	}
	return &openbindings.ExecuteOutput{Output: items, Status: 200}
}

func getPromptResultToOutput(result *gomcp.GetPromptResult) *openbindings.ExecuteOutput {
	var messages []any
	for _, msg := range result.Messages {
		if msg == nil {
			continue
		}
		entry := map[string]any{
			"role": string(msg.Role),
		}
		if msg.Content != nil {
			entry["content"] = contentToMap(msg.Content)
		}
		messages = append(messages, entry)
	}

	output := map[string]any{
		"messages": messages,
	}
	if result.Description != "" {
		output["description"] = result.Description
	}

	return &openbindings.ExecuteOutput{Output: output, Status: 200}
}

func extractContent(content []gomcp.Content) any {
	if len(content) == 0 {
		return nil
	}

	if len(content) == 1 {
		if tc, ok := content[0].(*gomcp.TextContent); ok {
			var parsed any
			if json.Unmarshal([]byte(tc.Text), &parsed) == nil {
				return parsed
			}
			return tc.Text
		}
	}

	allText := true
	for _, c := range content {
		if _, ok := c.(*gomcp.TextContent); !ok {
			allText = false
			break
		}
	}
	if allText {
		var texts []string
		for _, c := range content {
			texts = append(texts, c.(*gomcp.TextContent).Text)
		}
		return strings.Join(texts, "\n")
	}

	var items []any
	for _, c := range content {
		items = append(items, contentToMap(c))
	}
	return items
}

func contentToMap(c gomcp.Content) map[string]any {
	switch v := c.(type) {
	case *gomcp.TextContent:
		return map[string]any{"type": "text", "text": v.Text}
	case *gomcp.ImageContent:
		return map[string]any{"type": "image", "mimeType": v.MIMEType, "data": string(v.Data)}
	case *gomcp.AudioContent:
		return map[string]any{"type": "audio", "mimeType": v.MIMEType, "data": string(v.Data)}
	case *gomcp.ResourceLink:
		m := map[string]any{"type": "resource_link", "uri": v.URI}
		if v.Name != "" {
			m["name"] = v.Name
		}
		if v.MIMEType != "" {
			m["mimeType"] = v.MIMEType
		}
		return m
	case *gomcp.EmbeddedResource:
		m := map[string]any{"type": "resource"}
		if v.Resource != nil {
			m["uri"] = v.Resource.URI
			if v.Resource.MIMEType != "" {
				m["mimeType"] = v.Resource.MIMEType
			}
			if v.Resource.Text != "" {
				m["text"] = v.Resource.Text
			}
		}
		return m
	default:
		return map[string]any{"type": "unknown"}
	}
}

// mcpErrorCode maps an MCP error to a standard error code constant.
func mcpErrorCode(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401") || strings.Contains(msg, "unauthorized"):
		return openbindings.ErrCodeAuthRequired
	case strings.Contains(msg, "403") || strings.Contains(msg, "forbidden"):
		return openbindings.ErrCodePermissionDenied
	case strings.Contains(msg, "404") || strings.Contains(msg, "not found"):
		return openbindings.ErrCodeRefNotFound
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "dial tcp"):
		return openbindings.ErrCodeConnectFailed
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return openbindings.ErrCodeTimeout
	case strings.Contains(msg, "context canceled"):
		return openbindings.ErrCodeCancelled
	default:
		return openbindings.ErrCodeExecutionFailed
	}
}

func toStringStringMap(v any) (map[string]string, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected map[string]any, got %T", v)
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		result[k] = fmt.Sprintf("%v", val)
	}
	return result, nil
}
