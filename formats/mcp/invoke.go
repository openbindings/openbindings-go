package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	openbindings "github.com/openbindings/openbindings-go"
)

const (
	refPrefixTools     = "tools/"
	refPrefixResources = "resources/"
	refPrefixPrompts   = "prompts/"
)

// nextProgressToken provides a unique progress token per tool invocation.
// MCP servers correlate notifications/progress messages back to the request
// that originated the token.
var nextProgressToken atomic.Int64

// run drives one MCP binding invocation against the handle. Pre-dispatch
// failures (bad ref, missing or non-HTTP endpoint, non-object input) fire
// BEFORE any network I/O. Tools and prompts read the operation's single
// arguments object from the handle's input channel; resource reads take no
// input (the input side closes on entry).
func (e *Invoker) run(ctx context.Context, args *openbindings.BindingInvocationArgs, inv *openbindings.InvocationImpl[any, any]) {
	// --- Pre-dispatch validation: no network I/O has happened yet. ---
	entityType, name, err := parseRef(args.Ref)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code: openbindings.ErrCodeInvalidRef, Message: err.Error(),
		})
		return
	}

	location := strings.TrimSpace(args.Source.Location)
	if location == "" {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: "MCP source requires a location (endpoint URL)",
		})
		return
	}
	if !openbindings.IsHTTPURL(location) {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: fmt.Sprintf("MCP source location must be an HTTP or HTTPS URL, got %q", location),
		})
		return
	}

	// bctx bounds the underlying MCP I/O to the invocation's lifetime:
	// caller Cancel() (or upstream ctx cancellation) tears down in-flight
	// JSON-RPC calls.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	// --- Collect input from the handle. ---
	// Tools and prompts take one named-arguments object; resource reads take
	// no input. Close input as early as possible so callers never have to.
	//
	// No-input convention: when the operation layer drives an operation that
	// declares no input (Binding set, InputSchema nil — e.g. a zero-argument
	// tool or prompt), close input on entry and dispatch with empty arguments
	// rather than reading. A caller of a no-input operation never writes nor
	// closes, so an unconditional ReadInput would park forever.
	noInput := args.Binding != nil && args.InputSchema == nil
	var toolArgs map[string]any
	var promptArgs map[string]string
	switch {
	case entityType == "resources", noInput:
		_ = inv.CloseInput()
		if entityType == "tools" {
			toolArgs = map[string]any{}
		}
	default:
		first, err := inv.ReadInput(bctx)
		_ = inv.CloseInput()
		if err != nil && err != io.EOF {
			// Terminal error or cancellation: the invocation is already
			// settled (or settling); nothing to fire.
			return
		}
		if entityType == "tools" {
			m, ok := openbindings.ToStringAnyMap(first)
			if first != nil && !ok {
				inv.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("MCP tool input must be an object, got %T", first),
				})
				return
			}
			// MCP servers expect an object for arguments, never null.
			// Defensive shallow copy keeps the invoker contract ("never
			// mutate caller input") even when the third-party MCP SDK passes
			// args by reference.
			toolArgs = map[string]any{}
			for k, v := range m {
				toolArgs[k] = v
			}
		} else {
			pa, perr := toStringStringMap(first)
			if perr != nil {
				inv.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("MCP prompt arguments must be an object: %v", perr),
				})
				return
			}
			promptArgs = pa
		}
	}

	// --- Connect: the MCP initialize handshake is the first network I/O. ---
	// The header capture rides the per-call context into the HTTP transport
	// and records the latest POST response (status + headers), giving us real
	// HTTP metadata the go-mcp SDK's stringly-typed errors don't carry.
	hc := &headerCapture{}
	callCtx := context.WithValue(bctx, headerCaptureKey{}, hc)
	headers := buildHTTPHeaders(args.Context)

	session, err := e.pool.acquire(callCtx, e.clientVersion, location, headers)
	if err != nil {
		inv.FireError(mapMCPError(err, hc, openbindings.ErrCodeConnectFailed))
		return
	}
	defer session.release()

	// SetHeader must precede the first emit and may only happen once. The
	// snapshot at first-emit time is the entity call's POST response (the
	// capture overwrites the handshake's).
	var headerOnce sync.Once
	setHeader := func() {
		headerOnce.Do(func() { _ = inv.SetHeader(hc.snapshot()) })
	}

	// --- Dispatch. ---
	site := siteFor(args, location)
	var derr *openbindings.InvocationError
	var suspect bool
	switch entityType {
	case "tools":
		derr, suspect = runTool(callCtx, session, name, toolArgs, inv, hc, setHeader, site, args.Hooks)
	case "resources":
		derr, suspect = runResource(callCtx, session, name, inv, hc, setHeader, site, args.Hooks)
	default: // prompts
		derr, suspect = runPrompt(callCtx, session, name, promptArgs, inv, hc, setHeader)
	}
	if derr != nil {
		// A transport- or HTTP-level failure may have poisoned the pooled
		// session (the go-mcp client fails its connection on non-transient
		// errors); evict it so the next invocation gets a fresh handshake.
		// JSON-RPC errors, application-level tool errors, and cancellations
		// leave the session healthy.
		if suspect {
			e.pool.invalidate(session)
		}
		inv.FireError(derr)
	}
}

// sessionSuspect reports whether an error from the go-mcp SDK may have left
// the pooled session unusable. Structured JSON-RPC errors mean the server
// replied normally; cancellation is caller-driven. Anything else (HTTP error
// responses, transport breakage) is suspect.
func sessionSuspect(err error) bool {
	if serverJSONRPCError(err) != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// transportSentinels are the go-mcp SDK's LOCAL jsonrpc2 sentinel errors
// (implementation-defined codes that never come from the server): rejected
// by transport, client closing, server closing. jsonrpc.Error.Is compares
// by code, so errors.Is matches them anywhere in a wrapped chain.
var transportSentinels = []*jsonrpc.Error{
	{Code: -32005}, // rejected by transport (write/dial failures)
	{Code: -32003}, // client is closing
	{Code: -32004}, // server is closing
}

// serverJSONRPCError returns the JSON-RPC error the SERVER replied with, or
// nil. Both server replies and local transport failures carry a
// *jsonrpc.Error in their chain, so the SDK's local sentinels are excluded
// by code first.
func serverJSONRPCError(err error) *jsonrpc.Error {
	for _, s := range transportSentinels {
		if errors.Is(err, s) {
			return nil
		}
	}
	var werr *jsonrpc.Error
	if errors.As(err, &werr) {
		return werr
	}
	return nil
}

// runTool calls an MCP tool. Progress notifications stream as outputs ahead
// of the final result — multiple outputs are first-class on the handle.
func runTool(
	ctx context.Context,
	session *mcpSession,
	toolName string,
	toolArgs map[string]any,
	inv *openbindings.InvocationImpl[any, any],
	hc *headerCapture,
	setHeader func(),
	site openbindings.InvokeSite,
	hooks *openbindings.InvokeHooks,
) (*openbindings.InvocationError, bool) {
	// Each call gets a fresh progress token so the server can correlate
	// notifications to this specific invocation.
	progressToken := fmt.Sprintf("ob-progress-%d", nextProgressToken.Add(1))

	// emitMu serializes progress emits against the final-result emit: after
	// CallTool returns we unregister the handler and take the mutex, so the
	// result is guaranteed to be the last output. EmitOutput's parking IS the
	// backpressure (no side buffer); it returns the terminal error if the
	// invocation ends while parked, which stops the handler cleanly.
	var emitMu sync.Mutex
	var emitFailed bool
	session.registerProgress(progressToken, func(_ context.Context, req *gomcp.ProgressNotificationClientRequest) {
		if req == nil || req.Params == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		if emitFailed {
			// A prior emit observed the invocation terminate; stop emitting
			// (parking, the backpressure side effect, would otherwise resume).
			return
		}
		setHeader()
		p := req.Params
		// Shape mirrors the TS SDK: {progress, total?, message?}. The internal
		// progressToken is correlation plumbing, not part of the output.
		out := map[string]any{"progress": p.Progress}
		if p.Total != 0 {
			out["total"] = p.Total
		}
		if p.Message != "" {
			out["message"] = p.Message
		}
		if err := inv.EmitOutput(out); err != nil {
			emitFailed = true
		}
	})
	defer session.unregisterProgress(progressToken)

	// Note: Meta must be pre-initialized so SetProgressToken's mutation is
	// visible. The upstream gomcp helper allocates a local map when Meta is
	// nil but never writes it back to the params struct, so a nil Meta
	// silently swallows the token.
	params := &gomcp.CallToolParams{
		Name:      toolName,
		Arguments: toolArgs,
		Meta:      gomcp.Meta{},
	}
	params.SetProgressToken(progressToken)

	result, callErr := session.session.CallTool(ctx, params)

	session.unregisterProgress(progressToken)
	emitMu.Lock()
	defer emitMu.Unlock()

	if callErr != nil {
		return mapMCPError(callErr, hc, openbindings.ErrCodeExecutionFailed), sessionSuspect(callErr)
	}
	if result.IsError {
		// Application-level tool failure (CallToolResult.isError). The
		// session is healthy: the server replied normally.
		msg := contentText(result.Content)
		if msg == "" {
			msg = fmt.Sprintf("MCP tool %q reported an error", toolName)
		}
		return &openbindings.InvocationError{
			Code: openbindings.ErrCodeExecutionFailed, Message: msg,
		}, false
	}

	setHeader()
	value, decodeStamp, derr := toolResultValue(result, site, hooks)
	if derr != nil {
		return derr, false
	}
	// Success provenance stamps (conventions record): decode provenance
	// names the lane that produced the value; classification is
	// protocol-native (CallToolResult.isError), stamped as such.
	inv.SetTrailer(openbindings.Metadata{
		"x-ob-decode":   {decodeStamp},
		"x-ob-classify": {"protocol/isError"},
	})
	if err := inv.EmitOutput(value); err != nil {
		return nil, false // invocation terminated while parked; already settled
	}
	inv.CloseOutput()
	return nil, false
}

// runResource reads an MCP resource. Resource reads take no input.
func runResource(
	ctx context.Context,
	session *mcpSession,
	uri string,
	inv *openbindings.InvocationImpl[any, any],
	hc *headerCapture,
	setHeader func(),
	site openbindings.InvokeSite,
	hooks *openbindings.InvokeHooks,
) (*openbindings.InvocationError, bool) {
	result, err := session.session.ReadResource(ctx, &gomcp.ReadResourceParams{URI: uri})
	if err != nil {
		return mapMCPError(err, hc, openbindings.ErrCodeExecutionFailed), sessionSuspect(err)
	}

	setHeader()
	value, decodeStamp, derr := resourceValue(result, site, hooks)
	if derr != nil {
		return derr, false
	}
	inv.SetTrailer(openbindings.Metadata{"x-ob-decode": {decodeStamp}})
	if err := inv.EmitOutput(value); err != nil {
		return nil, false
	}
	inv.CloseOutput()
	return nil, false
}

// runPrompt gets an MCP prompt.
func runPrompt(
	ctx context.Context,
	session *mcpSession,
	promptName string,
	promptArgs map[string]string,
	inv *openbindings.InvocationImpl[any, any],
	hc *headerCapture,
	setHeader func(),
) (*openbindings.InvocationError, bool) {
	result, err := session.session.GetPrompt(ctx, &gomcp.GetPromptParams{
		Name:      promptName,
		Arguments: promptArgs,
	})
	if err != nil {
		return mapMCPError(err, hc, openbindings.ErrCodeExecutionFailed), sessionSuspect(err)
	}

	setHeader()
	if err := inv.EmitOutput(promptValue(result)); err != nil {
		return nil, false
	}
	inv.CloseOutput()
	return nil, false
}

// ---------------------------------------------------------------------------
// Ref parsing
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// mapMCPError converts an error from the go-mcp SDK into a terminal
// *InvocationError. JSON-RPC errors carry the MCP error code/data in
// Details (ERR_EXECUTION_FAILED); HTTP error responses map via the captured
// status code (401 → ERR_AUTH_REQUIRED, 403 → ERR_PERMISSION_DENIED);
// cancellation maps to ERR_CANCELLED; anything else falls back to the
// phase's code (ERR_CONNECT_FAILED during the initialize handshake,
// ERR_EXECUTION_FAILED during dispatch).
func mapMCPError(err error, hc *headerCapture, fallback string) *openbindings.InvocationError {
	var ie *openbindings.InvocationError
	if errors.As(err, &ie) {
		return ie
	}

	// JSON-RPC error: the server replied with a structured error.
	if werr := serverJSONRPCError(err); werr != nil {
		details := map[string]any{"code": werr.Code}
		if len(werr.Data) > 0 {
			var data any
			if json.Unmarshal(werr.Data, &data) == nil {
				details["data"] = data
			}
		}
		return &openbindings.InvocationError{
			Code:    openbindings.ErrCodeExecutionFailed,
			Message: err.Error(),
			Details: details,
		}
	}

	// HTTP error response: the capture saw the failing POST's status.
	if status, statusText := hc.lastStatus(); status >= 400 {
		return openbindings.HTTPError(status, statusText)
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &openbindings.InvocationError{
			Code: openbindings.ErrCodeCancelled, Message: err.Error(),
		}
	}

	return &openbindings.InvocationError{Code: fallback, Message: err.Error()}
}

// ---------------------------------------------------------------------------
// HTTP response capture
// ---------------------------------------------------------------------------

// headerCaptureKey keys a *headerCapture in a per-call context. The
// header-injecting transport records each POST response into the capture,
// because per-call ctx values propagate through the go-mcp SDK's JSON-RPC
// writes into the HTTP request.
type headerCaptureKey struct{}

// headerCapture records the latest POST response observed for one call:
// status (for HTTP error mapping) and headers (for Invocation metadata).
type headerCapture struct {
	mu         sync.Mutex
	statusCode int
	status     string
	header     openbindings.Metadata
}

func (hc *headerCapture) record(resp *http.Response) {
	md := make(openbindings.Metadata, len(resp.Header))
	for k, vs := range resp.Header {
		md[k] = append([]string(nil), vs...)
	}
	hc.mu.Lock()
	hc.statusCode = resp.StatusCode
	hc.status = resp.Status
	hc.header = md
	hc.mu.Unlock()
}

func (hc *headerCapture) snapshot() openbindings.Metadata {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.header == nil {
		return openbindings.Metadata{}
	}
	return hc.header
}

func (hc *headerCapture) lastStatus() (int, string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	return hc.statusCode, hc.status
}

// ---------------------------------------------------------------------------
// Result conversion
// ---------------------------------------------------------------------------

// siteFor builds the hook-consultation site for an MCP binding.
func siteFor(args *openbindings.BindingInvocationArgs, target string) openbindings.InvokeSite {
	var site openbindings.InvokeSite
	if args.Site != nil {
		site = *args.Site
	} else {
		site.Format = args.Source.Format
		site.Ref = args.Ref
	}
	if site.Target == "" {
		site.Target = target
	}
	return site
}

// toolResultValue converts a CallToolResult into the output value and its
// decode-provenance stamp. structuredContent is MCP's declared structured
// lane (2025-11-25: servers MUST conform it to outputSchema) and wins
// outright — there are no bytes to decode. A single text content is a
// STRING by the content-independent builtin: MCP defines JSON-in-text as
// the backwards-compatibility shadow of structuredContent, so parsing it
// is a consumer choice, opted into through the decode seam, never a
// payload sniff. Other content shapes are protocol-structured and pass
// through as generic values.
func toolResultValue(result *gomcp.CallToolResult, site openbindings.InvokeSite, hooks *openbindings.InvokeHooks) (any, string, *openbindings.InvocationError) {
	if result.StructuredContent != nil {
		switch sc := result.StructuredContent.(type) {
		case json.RawMessage:
			var structured any
			if json.Unmarshal(sc, &structured) == nil {
				return structured, "structuredContent", nil
			}
		default:
			return sc, "structuredContent", nil
		}
	}
	if len(result.Content) == 1 {
		if tc, ok := result.Content[0].(*gomcp.TextContent); ok {
			out, err := hooks.DecodeOutput(site, openbindings.RawResult{Body: []byte(tc.Text)}, builtinTextDecode)
			if err != nil {
				return nil, "", openbindings.AsInvocationError(err)
			}
			return out, decodeStampFor(hooks, "text"), nil
		}
	}
	return extractContent(result.Content), "content", nil
}

// builtinTextDecode is the MCP text builtin: the value is the text,
// verbatim. Content-independent per the conventions record; JSON-in-text
// consumers opt in with a decode hook.
func builtinTextDecode(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
	return string(raw.Body), nil
}

// decodeStampFor names the decode lane for the x-ob-decode provenance
// stamp: the builtin's token, or "hook" when a hook decided.
func decodeStampFor(hooks *openbindings.InvokeHooks, builtin string) string {
	if hooks.DecodeDecidedBy() == "hook" {
		return "hook"
	}
	return builtin
}

// resourceValue converts a ReadResourceResult into the output value and
// its decode-provenance stamp. Resources carry a DECLARED mimeType, so the
// builtin is the same header-driven lane HTTP uses: json/+json parses
// strictly (a parse failure is a loud error, never a silent fall-through),
// anything else is text, and the payload's shape never picks the lane.
func resourceValue(result *gomcp.ReadResourceResult, site openbindings.InvokeSite, hooks *openbindings.InvokeHooks) (any, string, *openbindings.InvocationError) {
	if len(result.Contents) == 0 {
		return nil, "content", nil
	}

	if len(result.Contents) == 1 {
		c := result.Contents[0]
		if c.Text != "" {
			out, err := hooks.DecodeOutput(site, openbindings.RawResult{Body: []byte(c.Text)}, builtinMIMEDecode(c.MIMEType))
			if err != nil {
				return nil, "", openbindings.AsInvocationError(err)
			}
			return out, decodeStampFor(hooks, "declared/mime-type"), nil
		}
		// Non-text (e.g. blob) content: return the whole content object so
		// binary data survives, matching the TS SDK which returns `c` as-is.
		return contentToGeneric(c), "content", nil
	}

	var items []any
	for _, c := range result.Contents {
		items = append(items, contentToGeneric(c))
	}
	return items, "content", nil
}

// builtinMIMEDecode is the resource builtin: the declared mimeType decides
// the lane. application/json and +json parse strictly; a declared-JSON
// body that does not parse is a loud invocation error.
func builtinMIMEDecode(mimeType string) openbindings.OutputDecoder {
	return func(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
		if isJSONMIME(mimeType) {
			var parsed any
			if err := json.Unmarshal(raw.Body, &parsed); err != nil {
				return nil, &openbindings.InvocationError{
					Code:    openbindings.ErrCodeExecutionFailed,
					Message: fmt.Sprintf("resource declares %s but its text is not valid JSON: %v", mimeType, err),
				}
			}
			return parsed, nil
		}
		return string(raw.Body), nil
	}
}

// isJSONMIME reports whether a declared media type selects the JSON lane
// (application/json or any +json suffix), matching the HTTP lane's rule.
func isJSONMIME(mimeType string) bool {
	mt := strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0])
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// contentToGeneric round-trips a resource content through JSON so every field
// (uri, mimeType, text, blob, _meta) survives into the generic output value,
// rather than hand-copying a subset and silently dropping blob data.
func contentToGeneric(c *gomcp.ResourceContents) any {
	b, err := json.Marshal(c)
	if err != nil {
		return map[string]any{"uri": c.URI, "mimeType": c.MIMEType}
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return map[string]any{"uri": c.URI, "mimeType": c.MIMEType}
	}
	return m
}

// promptValue converts a GetPromptResult into the output value.
func promptValue(result *gomcp.GetPromptResult) map[string]any {
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
	return output
}

func extractContent(content []gomcp.Content) any {
	if len(content) == 0 {
		return nil
	}

	if len(content) == 1 {
		if tc, ok := content[0].(*gomcp.TextContent); ok {
			// Reached only outside the tool text lane (which consults the
			// decode seam); text is text — never sniffed for JSON.
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

// contentText joins the text items of a content array, for error messages.
func contentText(content []gomcp.Content) string {
	var texts []string
	for _, c := range content {
		if tc, ok := c.(*gomcp.TextContent); ok && tc.Text != "" {
			texts = append(texts, tc.Text)
		}
	}
	return strings.Join(texts, "\n")
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
