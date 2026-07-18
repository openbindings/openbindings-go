package mcp

import (
	"context"
	"encoding/base64"
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
	"github.com/yosida95/uritemplate/v3"
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
// failures (bad ref, missing or non-HTTP endpoint, invalid pin, non-object
// input, unresolvable ref) fire BEFORE the entity request is dispatched
// (openbindings.mcp@1 §7, §9.1); ref/location/pin/input-shape failures fire
// before any network I/O at all. Tools and prompts read the operation's
// single arguments object from the handle's input channel; static resource
// reads take no input, and resource templates read their variables object
// once resolution has decided the ref names a template.
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
	if err := validateEndpoint(location); err != nil {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: err.Error(),
		})
		return
	}

	// A pinned listing (source content) validates up front: an invalid pin
	// is refused loudly before input collection and before any network I/O
	// (MCP-D-01).
	var pin *listing
	if args.Source.Content != nil {
		p, perr := parsePinnedListing(args.Source.Content)
		if perr != nil {
			inv.FireError(&openbindings.InvocationError{
				Code: openbindings.ErrCodeSourceLoadFailed, Message: perr.Error(),
			})
			return
		}
		pin = p
	}

	// bctx bounds the underlying MCP I/O to the invocation's lifetime:
	// caller Cancel() (or upstream ctx cancellation) tears down in-flight
	// JSON-RPC calls.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	// --- Resolution before dispatch (§7, MCP-P-02). ---
	// With a pin, resolution is offline-checkable: it happens here, before
	// input collection and before any connection, and the list requests are
	// never consulted (§6 content primacy). Without a pin it needs the live
	// exhausted listing and happens right after the handshake — still before
	// dispatch.
	var kind targetKind
	resolved := false
	if pin != nil {
		k, rerr := resolveRef(pin, entityType, name)
		if rerr != nil {
			inv.FireError(rerr)
			return
		}
		kind, resolved = k, true
	}

	// --- Collect input from the handle (tools and prompts). ---
	// Tools and prompts take one named-arguments object. Resource input
	// handling waits for resolution below: a template takes one input (its
	// variables), a static resource none, and only the listing can say which
	// the ref names.
	//
	// No-input convention: when the operation layer drives an operation that
	// declares no input (Binding set, InputSchema nil — e.g. a zero-argument
	// tool or prompt), close input on entry and dispatch with the arguments
	// member omitted rather than reading. A caller of a no-input operation
	// never writes nor closes, so an unconditional ReadInput would park
	// forever.
	noInput := args.Binding != nil && args.InputSchema == nil
	var toolArgs map[string]any      // nil means absent: the arguments member is omitted (§9.1)
	var promptArgs map[string]string // nil means absent: the arguments member is omitted (§9.1)
	if entityType != "resources" {
		if noInput {
			_ = inv.CloseInput()
		} else {
			first, rerr := inv.ReadInput(bctx)
			_ = inv.CloseInput()
			switch {
			case rerr != nil && rerr != io.EOF:
				// Terminal error or cancellation: the invocation is already
				// settled (or settling); nothing to fire.
				return
			case rerr == io.EOF:
				// Absent input value: omit the arguments member entirely
				// (§9.1) — never send arguments: {}.
			case entityType == "tools":
				m, ok := openbindings.ToStringAnyMap(first)
				if !ok {
					// A supplied input MUST be a JSON object (§9.1,
					// MCP-P-03); null included — absent means "never
					// written", not "written as null".
					inv.FireError(&openbindings.InvocationError{
						Code:    openbindings.ErrCodeValidationFailed,
						Message: fmt.Sprintf("MCP tool input must be an object when supplied, got %T", first),
					})
					return
				}
				// Defensive shallow copy keeps the invoker contract ("never
				// mutate caller input") even when the third-party MCP SDK
				// passes args by reference.
				toolArgs = make(map[string]any, len(m))
				for k, v := range m {
					toolArgs[k] = v
				}
			default: // prompts
				pa, perr := promptArguments(first)
				if perr != nil {
					inv.FireError(perr)
					return
				}
				promptArgs = pa
			}
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

	// --- Live resolution (no pin): the capability-gated, pagination-
	// exhausted listing for the ref's entity family, then the same byte-exact
	// match the pin path ran above (MCP-P-02). The entity request is never
	// dispatched blind on the ref name.
	if !resolved {
		l, lerr := liveListing(callCtx, session, entityType)
		if lerr != nil {
			if sessionSuspect(lerr) {
				e.pool.invalidate(session)
			}
			inv.FireError(mapMCPError(lerr, hc, openbindings.ErrCodeSourceLoadFailed))
			return
		}
		k, rerr := resolveRef(l, entityType, name)
		if rerr != nil {
			inv.FireError(rerr)
			return
		}
		kind = k
	}

	// --- Resource input (post-resolution: static vs template decides the
	// interaction shape, §8/§9.1). ---
	targetURI := name
	if entityType == "resources" {
		switch kind {
		case targetStaticResource:
			// Static resources take no input (§9.1): the input side closes
			// without reading. A caller that then supplies a value gets a
			// loud ERR_INPUT_CLOSED on its Write — the refusal surface the
			// handle model gives a value this binding may never read.
			_ = inv.CloseInput()
		case targetTemplateResource:
			uri, terr, settled := expandTemplateInput(bctx, inv, name, noInput)
			if settled {
				return
			}
			if terr != nil {
				inv.FireError(terr)
				return
			}
			targetURI = uri
		}
	}

	// SetHeader must precede the first emit and may only happen once. The
	// snapshot at first-emit time is the entity call's POST response (the
	// capture overwrites the handshake's and the list calls').
	var headerOnce sync.Once
	setHeader := func() {
		headerOnce.Do(func() { _ = inv.SetHeader(hc.snapshot()) })
	}

	// --- Dispatch. ---
	site := siteFor(args, location)
	var derr *openbindings.InvocationError
	var suspect bool
	switch kind {
	case targetTool:
		solicit := resolveSolicit(args.Context, e.solicitProgress)
		derr, suspect = runTool(callCtx, session, name, toolArgs, solicit, inv, hc, setHeader, site, args.Hooks)
	case targetPrompt:
		derr, suspect = runPrompt(callCtx, session, name, promptArgs, inv, hc, setHeader)
	default: // static resource, or a template expanded to targetURI
		derr, suspect = runResource(callCtx, session, targetURI, inv, hc, setHeader, site, args.Hooks)
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

// resolveSolicit consults the family's `solicit` configuration point in its
// defined order (openbindings.mcp@1 §9.3): per-invocation
// context.configuration["solicit"] → consumer-level WithSolicitProgress →
// the default, NOT solicited. A non-bool per-invocation value is a declined
// override and falls through. The default is content-independent and keeps a
// binding's observable stream realization-neutral: no progressToken rides
// the call, and the output stream is the result value alone (§9.2).
func resolveSolicit(bindCtx map[string]any, consumer *bool) bool {
	if cfg := openbindings.ContextConfiguration(bindCtx); cfg != nil {
		if v, ok := cfg["solicit"].(bool); ok {
			return v
		}
	}
	if consumer != nil {
		return *consumer
	}
	return false
}

// runTool calls an MCP tool. When progress is solicited (§9.3's `solicit`
// point — default off), correlated progress notifications stream as outputs
// ahead of the final result; the result is always last (§9.2, MCP-P-04).
func runTool(
	ctx context.Context,
	session *mcpSession,
	toolName string,
	toolArgs map[string]any,
	solicit bool,
	inv *openbindings.InvocationImpl[any, any],
	hc *headerCapture,
	setHeader func(),
	site openbindings.InvokeSite,
	hooks *openbindings.InvokeHooks,
) (*openbindings.InvocationError, bool) {
	params := &gomcp.CallToolParams{Name: toolName}
	if toolArgs != nil {
		// A supplied input maps whole and verbatim (§9.1).
		params.Arguments = toolArgs
	} else {
		// Absent input: the arguments member is omitted ENTIRELY (§9.1) —
		// never arguments: {}. The go-mcp client injects an empty object
		// when Arguments is nil, so the transport strips that injected
		// member for this call (see omitEmptyArgumentsKey).
		ctx = context.WithValue(ctx, omitEmptyArgumentsKey{}, true)
	}

	if !solicit {
		// Solicitation off (the default): no progressToken rides the call
		// and the stream is exactly the result value (§9.2, MCP-P-05).
		result, callErr := session.session.CallTool(ctx, params)
		if callErr != nil {
			return mapMCPError(callErr, hc, openbindings.ErrCodeExecutionFailed), sessionSuspect(callErr)
		}
		return emitToolResult(result, toolName, inv, setHeader, site, hooks)
	}

	// Each solicited call gets a fresh progress token so the server can
	// correlate notifications to this specific invocation.
	progressToken := fmt.Sprintf("ob-progress-%d", nextProgressToken.Add(1))

	// emitMu serializes progress emits against the final-result emit: after
	// CallTool returns we unregister the handler and take the mutex, so the
	// result is guaranteed to be the last output; a correlated notification
	// arriving after that is discarded — §9.2's defined disposal.
	// EmitOutput's parking IS the backpressure (no side buffer); it returns
	// the terminal error if the invocation ends while parked, which stops
	// the handler cleanly.
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
		// The progress value is the notification's params object with
		// progressToken removed, PRESENCE-PRESERVING (§9.2, MCP-P-04): an
		// explicit total:0 survives, an absent total stays absent. The raw
		// params are observed on the wire (session.observeSSEData) because
		// the go-mcp typed struct cannot represent that distinction (its
		// Total is a plain float64 whose zero means "unknown").
		out := session.popRawProgress(progressToken)
		if out == nil {
			// Wire observation missed the notification (not expected on the
			// Streamable HTTP transport): fall back to the typed params,
			// which cannot preserve explicit-zero presence.
			p := req.Params
			out = map[string]any{"progress": p.Progress}
			if p.Total != 0 {
				out["total"] = p.Total
			}
			if p.Message != "" {
				out["message"] = p.Message
			}
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
	params.Meta = gomcp.Meta{}
	params.SetProgressToken(progressToken)

	result, callErr := session.session.CallTool(ctx, params)

	session.unregisterProgress(progressToken)
	emitMu.Lock()
	defer emitMu.Unlock()

	if callErr != nil {
		return mapMCPError(callErr, hc, openbindings.ErrCodeExecutionFailed), sessionSuspect(callErr)
	}
	return emitToolResult(result, toolName, inv, setHeader, site, hooks)
}

// emitToolResult classifies and emits a completed tools/call result: an
// isError result is a failure outcome whatever its content (§9.4, MCP-P-06);
// every other completed result decodes per §9.3 and terminates the stream.
func emitToolResult(
	result *gomcp.CallToolResult,
	toolName string,
	inv *openbindings.InvocationImpl[any, any],
	setHeader func(),
	site openbindings.InvokeSite,
	hooks *openbindings.InvokeHooks,
) (*openbindings.InvocationError, bool) {
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

// expandTemplateInput reads and validates a resource template's input value
// and expands the template per RFC 6570 (§9.1, MCP-P-03). The input, when
// supplied, is a JSON object of the template's variables: every member value
// MUST be a string and every member MUST name a declared variable — each
// violation is refused loudly before resources/read is dispatched. An absent
// input (or a no-input operation) expands with all variables undefined,
// which follows RFC 6570's undefined-value expansion. settled reports that
// the invocation already terminated while reading input (nothing to fire).
func expandTemplateInput(
	ctx context.Context,
	inv *openbindings.InvocationImpl[any, any],
	template string,
	noInput bool,
) (uri string, ierr *openbindings.InvocationError, settled bool) {
	tmpl, terr := uritemplate.New(template)
	if terr != nil {
		return "", &openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceLoadFailed,
			Message: fmt.Sprintf("MCP listing declares resource template %q, which is not a valid RFC 6570 URI template: %v", template, terr),
		}, false
	}

	var first any
	supplied := false
	if noInput {
		_ = inv.CloseInput()
	} else {
		v, rerr := inv.ReadInput(ctx)
		_ = inv.CloseInput()
		if rerr != nil && rerr != io.EOF {
			return "", nil, true
		}
		if rerr == nil {
			first, supplied = v, true
		}
	}

	values := uritemplate.Values{}
	if supplied {
		m, ok := openbindings.ToStringAnyMap(first)
		if !ok {
			return "", &openbindings.InvocationError{
				Code:    openbindings.ErrCodeValidationFailed,
				Message: fmt.Sprintf("MCP resource-template input must be an object of template variables when supplied, got %T", first),
			}, false
		}
		declared := map[string]bool{}
		for _, n := range tmpl.Varnames() {
			declared[n] = true
		}
		for k, v := range m {
			if !declared[k] {
				return "", &openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("MCP resource-template input names variable %q, which template %q does not declare", k, template),
				}, false
			}
			s, ok := v.(string)
			if !ok {
				return "", &openbindings.InvocationError{
					Code:    openbindings.ErrCodeValidationFailed,
					Message: fmt.Sprintf("MCP resource-template variable %q must be a string, got %T (template variables are never coerced)", k, v),
				}, false
			}
			values[k] = uritemplate.String(s)
		}
	}

	expanded, xerr := tmpl.Expand(values)
	if xerr != nil {
		return "", &openbindings.InvocationError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: fmt.Sprintf("RFC 6570 expansion of template %q failed: %v", template, xerr),
		}, false
	}
	return expanded, nil, false
}

// promptArguments maps a supplied prompt input value (§9.1, MCP-P-03): it
// MUST be a JSON object, and MCP prompt arguments are string-typed, so every
// member value MUST be a string — a non-object input or a non-string member
// is refused loudly before prompts/get is dispatched, never coerced.
func promptArguments(v any) (map[string]string, *openbindings.InvocationError) {
	m, ok := openbindings.ToStringAnyMap(v)
	if !ok {
		return nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeValidationFailed,
			Message: fmt.Sprintf("MCP prompt input must be an object when supplied, got %T", v),
		}
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		s, ok := val.(string)
		if !ok {
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeValidationFailed,
				Message: fmt.Sprintf("MCP prompt argument %q must be a string, got %T (prompt arguments are string-typed and are never coerced)", k, val),
			}
		}
		out[k] = s
	}
	return out, nil
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

// validateEndpoint checks MCP-D-02's location requirement offline, without
// connecting: `location` is REQUIRED — this family is service-addressed, so
// a content-only source addresses nothing — and must be an absolute
// http/https URI addressing a Streamable HTTP endpoint.
func validateEndpoint(location string) error {
	if location == "" {
		return fmt.Errorf("MCP source requires a location (endpoint URL): a content-only source addresses nothing (MCP-D-02)")
	}
	if !openbindings.IsHTTPURL(location) {
		return fmt.Errorf("MCP source location must be an absolute HTTP or HTTPS URL, got %q (MCP-D-02)", location)
	}
	return nil
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

	if errors.Is(err, context.DeadlineExceeded) {
		// A deadline is a TIMEOUT (transient / effects: possible), agreeing with
		// the handle's AfterFunc so a deadline that races both terminals settles
		// deterministically as ERR_TIMEOUT. An explicit cancel stays ERR_CANCELLED.
		return &openbindings.InvocationError{
			Code: openbindings.ErrCodeTimeout, Message: err.Error(),
		}
	}
	if errors.Is(err, context.Canceled) {
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
		site.BindingSpec = args.Source.BindingSpec
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

// resourceValue converts a ReadResourceResult into the output value and its
// decode-provenance stamp. The value is ALWAYS the array of decoded contents
// items, in order (§9.3, MCP-P-05) — uniformly, so the value's shape never
// depends on how many items the server returned: contents: [] yields [], and
// authors who want a bare single value declare an outputTransform. Each item
// decodes by protocol structure FIRST: a blob item passes as its Base64
// string as MCP carries it, whatever mimeType it declares; a text item
// decodes by its DECLARED mimeType, exactly the HTTP header rule — json/+json
// parses strictly (a parse failure is a loud error, never a silent
// fall-through), anything else is text, and the payload's shape never picks
// the lane.
func resourceValue(result *gomcp.ReadResourceResult, site openbindings.InvokeSite, hooks *openbindings.InvokeHooks) (any, string, *openbindings.InvocationError) {
	items := make([]any, 0, len(result.Contents))
	for _, c := range result.Contents {
		if c == nil {
			continue
		}
		if c.Blob != nil {
			// Structural: the wire item carried a blob member. go-mcp has
			// already Base64-decoded it, so re-encode to pass the string as
			// MCP carries it.
			items = append(items, base64.StdEncoding.EncodeToString(c.Blob))
			continue
		}
		out, err := hooks.DecodeOutput(site, openbindings.RawResult{Body: []byte(c.Text)}, builtinMIMEDecode(c.MIMEType))
		if err != nil {
			return nil, "", openbindings.AsInvocationError(err)
		}
		items = append(items, out)
	}
	return items, decodeStampFor(hooks, "contents/declared"), nil
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
