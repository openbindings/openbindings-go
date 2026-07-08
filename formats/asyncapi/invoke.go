package asyncapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	openbindings "github.com/openbindings/openbindings-go"
)

// AsyncAPI binding execution over the cardinality-agnostic invocation handle.
//
// One entrypoint (runBinding) drives every channel shape against the
// binding-facing BindingHandle:
//
//	send + http/https     unary HTTP POST: first input -> request body,
//	                      response -> single output
//	send + ws/wss         client-streaming publish: every input -> one
//	                      socket frame; closing input closes the call
//	receive + http/https  SSE subscribe: server events -> outputs
//	receive + ws/wss      WebSocket subscribe (bidi-capable): socket
//	                      frames -> outputs, caller inputs -> socket frames
//
// All pre-dispatch failures (bad ref, missing server, missing context) are
// raised via FireError BEFORE any network I/O, per the binding-author
// contract.

const maxResponseBytes = 10 * 1024 * 1024 // 10 MB

// sseMaxLineBytes bounds individual SSE line length to prevent runaway memory
// use from a misbehaving server (parity with openapi/sse.go).
const sseMaxLineBytes = 16 * 1024 * 1024

type handle = openbindings.BindingHandle[any, any]

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// runBinding resolves the operation, checks runtime context, and dispatches
// to the protocol-specific runner. Terminates the handle exactly once. ctx is
// expected to be bound to the invocation's lifetime (DoneContext).
func runBinding(ctx context.Context, client *http.Client, pool *wsPool, args *openbindings.BindingInvocationArgs, h handle, doc *document) {
	opID, err := parseRef(args.Ref)
	if err != nil {
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeInvalidRef, Message: err.Error()})
		return
	}

	asyncOp, ok := doc.Operations[opID]
	if !ok {
		h.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("operation %q not in AsyncAPI doc", opID),
		})
		return
	}

	serverURL, protocol, err := resolveServer(doc, args.Context)
	if err != nil {
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: err.Error()})
		return
	}

	// Context negotiation: challenge BEFORE any connection is opened.
	if details := requiredContext(doc, &asyncOp, serverURL, args.Context); details != nil {
		h.FireError(openbindings.NewContextRequiredError(
			fmt.Sprintf("operation %q requires credentials the context does not provide", opID), details))
		return
	}

	channelName := extractRefName(asyncOp.Channel.Ref)
	address := channelName
	if channel, hasChannel := doc.Channels[channelName]; hasChannel && channel.Address != "" {
		address = channel.Address
	}

	switch asyncOp.Action {
	case "receive":
		switch protocol {
		case "ws", "wss":
			runWSReceive(ctx, pool, serverURL, address, doc, &asyncOp, args, h)
		case "http", "https":
			runSSEReceive(ctx, client, serverURL, address, doc, &asyncOp, args, h)
		default:
			h.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceConfigError,
				Message: fmt.Sprintf("receive not supported for protocol %q (supported: http, https, ws, wss)", protocol),
			})
		}
	case "send":
		switch protocol {
		case "ws", "wss":
			runWSSend(ctx, pool, serverURL, address, doc, &asyncOp, args, h)
		case "http", "https":
			runHTTPSend(ctx, client, serverURL, address, doc, &asyncOp, args, h)
		default:
			h.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceConfigError,
				Message: fmt.Sprintf("send not supported for protocol %q (supported: http, https, ws, wss)", protocol),
			})
		}
	default:
		h.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: fmt.Sprintf("unknown action %q", asyncOp.Action),
		})
	}
}

// ---------------------------------------------------------------------------
// Ref parsing & server resolution
// ---------------------------------------------------------------------------

func parseRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty ref")
	}

	const prefix = "#/operations/"
	if strings.HasPrefix(ref, prefix) {
		opID := strings.TrimPrefix(ref, prefix)
		if opID == "" {
			return "", fmt.Errorf("empty operation ID in ref %q", ref)
		}
		return opID, nil
	}

	return ref, nil
}

func resolveServer(doc *document, ctx map[string]any) (url string, protocol string, err error) {
	if meta := openbindings.ContextMetadata(ctx); meta != nil {
		if base, ok := meta["baseURL"].(string); ok && base != "" {
			proto := "http"
			if strings.HasPrefix(base, "https://") {
				proto = "https"
			} else if strings.HasPrefix(base, "wss://") {
				proto = "wss"
			} else if strings.HasPrefix(base, "ws://") {
				proto = "ws"
			}
			return strings.TrimRight(base, "/"), proto, nil
		}
	}

	if server := pickDocServer(doc); server != nil {
		proto := strings.ToLower(server.Protocol)
		url := proto + "://" + server.Host
		if server.PathName != "" {
			url += server.PathName
		}
		return strings.TrimRight(url, "/"), proto, nil
	}

	return "", "", fmt.Errorf("no supported server found (need http, https, ws, or wss protocol)")
}

// pickDocServer returns the doc server a connection targets: the first
// (sorted by name) server with a supported protocol, or nil when none
// exists. Security derivation MUST consult this same server so the
// requirements always describe the server actually dialed, never some other
// server that happens to declare security.
func pickDocServer(doc *document) *server {
	serverNames := make([]string, 0, len(doc.Servers))
	for name := range doc.Servers {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)

	for _, name := range serverNames {
		server := doc.Servers[name]
		switch strings.ToLower(server.Protocol) {
		case "http", "https", "ws", "wss":
			return &server
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Context requirements (CONTEXT_REQUIRED negotiation)
// ---------------------------------------------------------------------------

// requirementType maps an AsyncAPI security scheme to a standard requirement
// family, or "" when the scheme family is unknown (not checkable, not
// enforced).
func requirementType(s securityScheme) string {
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "bearer":
			return "auth.bearer"
		case "basic":
			return "auth.basic"
		}
		return ""
	case "httpBearer":
		return "auth.bearer"
	case "userPassword":
		return "auth.basic"
	case "apiKey", "httpApiKey":
		return "auth.apiKey"
	case "oauth2":
		return "auth.oauth2"
	}
	return ""
}

// requiredContext computes the context the binding requires for this
// operation, or nil when the provided context already satisfies it (or the
// doc declares nothing checkable). The document model represents security as
// requirement OBJECTS (scheme name -> scopes): the list is a disjunction (OR)
// of objects, each object a conjunction (AND) of schemes — exactly the
// alternatives/requirements shape of ContextRequiredDetails. An empty
// requirement object means anonymous access is allowed (no challenge for the
// whole operation, mirroring openapi); an object containing a scheme the SDK
// cannot express is skipped entirely rather than degraded into a weaker
// requirement. Side-effect-free; shared by runBinding and PrepareBinding.
func requiredContext(doc *document, asyncOp *asyncOperation, serverURL string, ctx map[string]any) *openbindings.ContextRequiredDetails {
	requirements := securityRequirements(doc, asyncOp)
	if len(requirements) == 0 || doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return nil
	}

	var alternatives []openbindings.ContextAlternative
	for _, reqObj := range requirements {
		if len(reqObj) == 0 {
			// Empty requirement object: anonymous access is allowed.
			return nil
		}
		schemeNames := make([]string, 0, len(reqObj))
		for name := range reqObj {
			schemeNames = append(schemeNames, name)
		}
		sort.Strings(schemeNames)

		var reqs []openbindings.ContextRequirement
		expressible := true
		for _, name := range schemeNames {
			scheme, ok := doc.Components.SecuritySchemes[name]
			if !ok {
				expressible = false
				break
			}
			typ := requirementType(scheme)
			if typ == "" {
				expressible = false
				break
			}
			req := openbindings.ContextRequirement{Type: typ}
			if scheme.Description != "" {
				req.Description = scheme.Description
			}
			reqs = append(reqs, req)
		}
		if !expressible || len(reqs) == 0 {
			continue
		}
		alternatives = append(alternatives, openbindings.ContextAlternative{Requirements: reqs})
	}
	if len(alternatives) == 0 {
		return nil
	}

	details := &openbindings.ContextRequiredDetails{
		Target:       serverURL,
		Alternatives: alternatives,
	}
	if openbindings.ContextSatisfies(ctx, details) {
		return nil
	}
	return details
}

// securityRequirements returns the security requirement objects applicable to
// an operation: operation-level security overrides; otherwise the
// requirements of the doc server the connection targets (the same server
// pickDocServer selects).
func securityRequirements(doc *document, asyncOp *asyncOperation) []map[string][]string {
	if asyncOp != nil && len(asyncOp.Security) > 0 {
		return asyncOp.Security
	}
	if server := pickDocServer(doc); server != nil && len(server.Security) > 0 {
		return server.Security
	}
	return nil
}

// ---------------------------------------------------------------------------
// Send over HTTP: unary POST
// ---------------------------------------------------------------------------

func runHTTPSend(ctx context.Context, client *http.Client, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
	// Unary: the first input is the message payload. No-input convention:
	// an operation-layer call (Binding != nil) for an operation that declares
	// no input (InputSchema == nil) closes input on entry and sends one empty
	// message — callers of no-input operations never write nor close, so
	// reading would park forever.
	var first any
	if args.Binding != nil && args.InputSchema == nil {
		_ = h.CloseInput()
	} else {
		v, rerr := h.ReadInput(ctx)
		if rerr == io.EOF {
			h.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeMissingInput,
				Message: "send operation requires an input message",
			})
			return
		}
		if rerr != nil {
			return // invocation already terminal (or cancelled)
		}
		first = v
		_ = h.CloseInput()
	}

	body := []byte("{}")
	if first != nil {
		var err error
		body, err = json.Marshal(first)
		if err != nil {
			h.FireError(openbindings.AsInvocationError(err))
			return
		}
	}

	url := serverURL + "/" + strings.TrimLeft(address, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		h.FireError(openbindings.AsInvocationError(err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	applyHTTPContext(req, doc, asyncOp, args.Context)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // cancellation is already terminal
		}
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		h.FireError(httpStatusError(resp))
		return
	}

	_ = h.SetHeader(headerMetadata(resp.Header))

	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		// Accepted with no payload: a publish acknowledgment, not an output.
		h.CloseOutput()
		return
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: err.Error()})
		return
	}
	if len(respBody) > maxResponseBytes {
		h.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes),
		})
		return
	}

	if len(respBody) == 0 {
		h.CloseOutput()
		return
	}

	status := resp.StatusCode
	raw := openbindings.RawResult{Status: &status, Body: respBody, Meta: headerMetadata(resp.Header)}
	output, derr := args.Hooks.DecodeOutput(siteFor(args, serverURL), raw,
		builtinDecodeFor(declaredContentType(doc, asyncOp)))
	if derr != nil {
		h.FireError(openbindings.AsInvocationError(derr))
		return
	}

	// §4.5.2 success stamps: decode is spec/content-type (the message's
	// declared contentType decides the lane), hook when overridden;
	// classify is not-consulted (asyncapi runs no result classifier — the
	// HTTP 4xx guard above is transport, not a format verdict).
	h.SetTrailer(decodeTrailer(args.Hooks, "spec/content-type"))
	if h.EmitOutput(output) != nil {
		return // invocation terminated while the emit was parked
	}
	h.CloseOutput()
}

// ---------------------------------------------------------------------------
// Receive over HTTP: SSE subscribe
// ---------------------------------------------------------------------------

func runSSEReceive(ctx context.Context, client *http.Client, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
	// Server -> client: the channel takes no caller input.
	_ = h.CloseInput()

	url := serverURL + "/" + strings.TrimLeft(address, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		h.FireError(openbindings.AsInvocationError(err))
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	applyHTTPContext(req, doc, asyncOp, args.Context)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.FireError(httpStatusError(resp))
		return
	}

	_ = h.SetHeader(headerMetadata(resp.Header))

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), sseMaxLineBytes)
	var dataLines []string
	var eventBytes int

	for scanner.Scan() {
		line := scanner.Text()
		// The size cap is PER EVENT, not cumulative: a long-lived
		// subscription legitimately streams more than maxResponseBytes in
		// total (the same choice connect/streaming.go documents for its
		// per-envelope cap).
		eventBytes += len(line) + 1 // +1 for newline
		if eventBytes > maxResponseBytes {
			h.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeResponseError,
				Message: fmt.Sprintf("SSE event exceeds %d byte limit", maxResponseBytes),
			})
			return
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}

		if line == "" {
			eventBytes = 0
			if len(dataLines) > 0 {
				ev, derr := decodeSSEEvent(args, siteFor(args, serverURL), doc, asyncOp, resp, dataLines)
				dataLines = dataLines[:0]
				if derr != nil {
					h.FireError(openbindings.AsInvocationError(derr))
					return
				}
				if h.EmitOutput(ev) != nil {
					return // invocation terminated while the emit was parked
				}
			}
		}
	}

	if len(dataLines) > 0 {
		ev, derr := decodeSSEEvent(args, siteFor(args, serverURL), doc, asyncOp, resp, dataLines)
		if derr != nil {
			h.FireError(openbindings.AsInvocationError(derr))
			return
		}
		if h.EmitOutput(ev) != nil {
			return
		}
	}

	if serr := scanner.Err(); serr != nil {
		if ctx.Err() == nil {
			h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: serr.Error()})
		}
		return
	}
	h.CloseOutput()
}

// ---------------------------------------------------------------------------
// WebSocket frames
// ---------------------------------------------------------------------------

// decodeWSFrame decodes one socket frame through the consultation seam:
// Status is NIL (a WS frame has no scalar completion status — never
// fabricated); the builtin follows the DECLARED message contentType. The
// former `{"error"}`/`{"data"}` convention unwrapping left the builtin —
// it is the hook channel's business now (a returned decode error is
// terminal, which is exactly the override channel for error-frame
// conventions).
func decodeWSFrame(args *openbindings.BindingInvocationArgs, site openbindings.InvokeSite, contentType string, frame []byte) (any, error) {
	raw := openbindings.RawResult{Body: frame}
	return args.Hooks.DecodeOutput(site, raw, builtinDecodeFor(contentType))
}

// ---------------------------------------------------------------------------
// Receive over WebSocket: subscribe (bidi-capable) on a pooled socket
// ---------------------------------------------------------------------------

func runWSReceive(ctx context.Context, pool *wsPool, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
	// The subscription is registered inside acquire (before the reader
	// starts on a fresh dial) so no early server push can be lost.
	sub := newWSSubscription()
	pw, unsubscribe, err := pool.acquire(ctx, serverURL, address, doc, asyncOp, args.Context,
		&wsListener{onFrame: sub.push, onClose: sub.close})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: err.Error()})
		return
	}
	defer pw.release()
	defer unsubscribe()

	// First-frame bearer convention: browsers cannot set headers on WebSocket
	// upgrades, so the token travels in the first message body — but only
	// when the resolved server's security declares a bearer-family scheme,
	// and only once per pooled connection (a reused socket has already
	// authenticated; re-sending would leak the frame into the message stream).
	if token := openbindings.ContextBearerToken(args.Context); token != "" && declaresBearerScheme(doc, asyncOp) {
		frame, merr := json.Marshal(map[string]any{"bearerToken": token})
		if merr != nil {
			// A failed marshal must not silently skip auth.
			h.FireError(openbindings.AsInvocationError(merr))
			return
		}
		if werr := pw.sendFirstFrameAuth(ctx, frame); werr != nil {
			if ctx.Err() != nil {
				return
			}
			h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: werr.Error()})
			return
		}
	}

	// Inputs -> socket. Lets callers push subscription/control frames; the
	// caller closing input does NOT end the subscription (outputs keep
	// flowing). The pump exits on input EOF, on invocation terminal, or when
	// ctx (bound to the invocation's lifetime) ends.
	go func() {
		for {
			msg, rerr := h.ReadInput(ctx)
			if rerr != nil {
				return
			}
			frame, merr := json.Marshal(msg)
			if merr != nil {
				h.FireError(openbindings.AsInvocationError(merr))
				return
			}
			if pw.send(ctx, frame) != nil {
				return
			}
		}
	}()

	// Socket -> outputs. Owns the terminal transition: clean socket close ->
	// CloseOutput; socket error -> ERR_STREAM_ERROR.
	for {
		frame, closed, closeErr, ok := sub.next(ctx)
		if !ok {
			return // invocation terminated (cancelled) while waiting
		}
		if closed {
			if closeErr != nil {
				h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: closeErr.Error()})
			} else {
				h.CloseOutput()
			}
			return
		}
		out, derr := decodeWSFrame(args, siteFor(args, serverURL), declaredContentType(doc, asyncOp), frame)
		if derr != nil {
			// A decode error mid-stream is terminal; already-emitted
			// outputs stand (drain-before-terminal).
			h.FireError(openbindings.AsInvocationError(derr))
			return
		}
		if h.EmitOutput(out) != nil {
			return // invocation terminated while the emit was parked
		}
	}
}

// declaresBearerScheme reports whether the security applicable to the
// operation (resolved against the same server the connection targets)
// declares a bearer-family scheme — http bearer, httpBearer, or oauth2, the
// schemes whose credential is a bearer token. Gates the first-frame bearer
// convention so the token is never volunteered to servers that do not
// declare bearer auth.
func declaresBearerScheme(doc *document, asyncOp *asyncOperation) bool {
	for _, s := range resolveSecuritySchemes(doc, asyncOp) {
		switch requirementType(s) {
		case "auth.bearer", "auth.oauth2":
			return true
		}
	}
	return false
}

// wsSubscription buffers broadcast frames from a pooled socket for one
// consumer, preserving arrival order without blocking the shared reader
// goroutine (the EmitOutput parking downstream is the real backpressure).
type wsSubscription struct {
	mu       sync.Mutex
	frames   [][]byte
	closed   bool
	closeErr error
	notify   chan struct{} // 1-buffered wake signal
}

func newWSSubscription() *wsSubscription {
	return &wsSubscription{notify: make(chan struct{}, 1)}
}

func (s *wsSubscription) push(frame []byte) {
	s.mu.Lock()
	s.frames = append(s.frames, frame)
	s.mu.Unlock()
	s.wake()
}

func (s *wsSubscription) close(err error) {
	s.mu.Lock()
	s.closed = true
	s.closeErr = err
	s.mu.Unlock()
	s.wake()
}

func (s *wsSubscription) wake() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// next returns the next frame, or closed=true once the socket has closed
// (buffered frames always drain first), or ok=false when ctx ends first.
func (s *wsSubscription) next(ctx context.Context) (frame []byte, closed bool, closeErr error, ok bool) {
	for {
		s.mu.Lock()
		if len(s.frames) > 0 {
			frame = s.frames[0]
			s.frames = s.frames[1:]
			s.mu.Unlock()
			return frame, false, nil, true
		}
		if s.closed {
			err := s.closeErr
			s.mu.Unlock()
			return nil, true, err, true
		}
		s.mu.Unlock()

		select {
		case <-s.notify:
		case <-ctx.Done():
			return nil, false, nil, false
		}
	}
}

// ---------------------------------------------------------------------------
// Send over WebSocket: client-streaming publish on a pooled socket
// ---------------------------------------------------------------------------

func runWSSend(ctx context.Context, pool *wsPool, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
	pw, _, err := pool.acquire(ctx, serverURL, address, doc, asyncOp, args.Context, nil)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: err.Error()})
		return
	}
	defer pw.release()

	// No-input convention: an operation-layer call (Binding != nil) for an
	// operation that declares no input (InputSchema == nil) closes input on
	// entry and publishes one empty-object frame — callers of no-input
	// operations never write nor close, so reading would park forever.
	if args.Binding != nil && args.InputSchema == nil {
		_ = h.CloseInput()
		if werr := pw.send(ctx, []byte("{}")); werr != nil {
			pool.evict(pw)
			if ctx.Err() != nil {
				return
			}
			h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: werr.Error()})
			return
		}
		h.CloseOutput()
		return
	}

	// Client-streaming publish: every input is one frame; the caller closing
	// input completes the call with zero outputs (a publish yields no
	// outputs; auth rides the upgrade request, never the message body).
	for {
		msg, rerr := h.ReadInput(ctx)
		if rerr == io.EOF {
			h.CloseOutput()
			return
		}
		if rerr != nil {
			return // invocation already terminal (or cancelled)
		}
		frame, merr := json.Marshal(msg)
		if merr != nil {
			h.FireError(openbindings.AsInvocationError(merr))
			return
		}
		if werr := pw.send(ctx, frame); werr != nil {
			// Broken socket: evict it so the next caller dials a fresh one.
			pool.evict(pw)
			if ctx.Err() != nil {
				return
			}
			h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: werr.Error()})
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// decodeSSEEvent decodes one SSE event through the consultation seam: the
// builtin follows the DECLARED message contentType (never payload
// sniffing); Status carries the initial response's status on every unit
// (real and invocation-scoped, never fabricated).
func decodeSSEEvent(args *openbindings.BindingInvocationArgs, site openbindings.InvokeSite, doc *document, asyncOp *asyncOperation, resp *http.Response, dataLines []string) (any, error) {
	status := resp.StatusCode
	raw := openbindings.RawResult{
		Status: &status,
		Body:   []byte(strings.Join(dataLines, "\n")),
		Meta:   headerMetadata(resp.Header),
	}
	return args.Hooks.DecodeOutput(site, raw, builtinDecodeFor(declaredContentType(doc, asyncOp)))
}

// headerMetadata converts HTTP response headers to invocation metadata.
// Keys are lowercased for cross-SDK portability (the TS SDK's Headers
// iteration yields lowercase keys).
func headerMetadata(hdr http.Header) openbindings.Metadata {
	md := make(openbindings.Metadata, len(hdr))
	for k, vs := range hdr {
		md[strings.ToLower(k)] = append([]string(nil), vs...)
	}
	return md
}

// httpStatusError builds the terminal error for an HTTP error response,
// attaching the (parsed) body to Details alongside the status.
func httpStatusError(resp *http.Response) *openbindings.InvocationError {
	ie := openbindings.HTTPError(resp.StatusCode, resp.Status)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(body) == 0 {
		return ie
	}
	details := map[string]any{"status": resp.StatusCode}
	if len(body) > maxResponseBytes {
		details["body"] = fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes)
	} else {
		// The raw capture, verbatim: error details carry bytes-as-text,
		// never a sniffed parse (payload-independence, §6).
		details["body"] = string(body)
	}
	ie.Details = details
	return ie
}

// ---------------------------------------------------------------------------
// Credential application
// ---------------------------------------------------------------------------

// applyHTTPContext applies opaque binding context (credentials via well-known
// fields) and execution options (headers, cookies) to an HTTP request, using
// AsyncAPI securitySchemes for spec-driven credential placement.
func applyHTTPContext(req *http.Request, doc *document, asyncOp *asyncOperation, bindCtx map[string]any) {
	if len(bindCtx) > 0 {
		applied, queryParams := applyCredentialsViaSecuritySchemes(req, doc, asyncOp, bindCtx)
		if !applied {
			applyCredentialsFallback(req, bindCtx)
		}
		if len(queryParams) > 0 {
			q := req.URL.Query()
			for k, vs := range queryParams {
				for _, v := range vs {
					q.Set(k, v)
				}
			}
			req.URL.RawQuery = q.Encode()
		}
	}

	for k, v := range openbindings.ContextHeaders(bindCtx) {
		req.Header.Set(k, v)
	}
	for k, v := range openbindings.ContextCookies(bindCtx) {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
}

// resolveSecuritySchemes returns the security schemes applicable to an
// operation, flattened for credential placement. The requirements come from
// securityRequirements (operation-level, else the server the connection
// targets).
func resolveSecuritySchemes(doc *document, asyncOp *asyncOperation) []securityScheme {
	requirements := securityRequirements(doc, asyncOp)
	if len(requirements) == 0 {
		return nil
	}

	if doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return nil
	}

	var result []securityScheme
	seen := map[string]bool{}
	for _, req := range requirements {
		schemeNames := make([]string, 0, len(req))
		for schemeName := range req {
			schemeNames = append(schemeNames, schemeName)
		}
		sort.Strings(schemeNames)
		for _, schemeName := range schemeNames {
			if seen[schemeName] {
				continue
			}
			seen[schemeName] = true
			if scheme, ok := doc.Components.SecuritySchemes[schemeName]; ok {
				result = append(result, scheme)
			}
		}
	}
	return result
}

// applyCredentialsViaSecuritySchemes reads the AsyncAPI doc's securitySchemes
// and operation/server-level security requirements to place credentials exactly
// where the spec declares (header, query, or cookie with the correct name).
func applyCredentialsViaSecuritySchemes(req *http.Request, doc *document, asyncOp *asyncOperation, bindCtx map[string]any) (applied bool, queryParams url.Values) {
	schemes := resolveSecuritySchemes(doc, asyncOp)
	if len(schemes) == 0 {
		return false, nil
	}

	queryParams = url.Values{}

	for _, s := range schemes {
		switch s.Type {
		case "apiKey", "httpApiKey":
			val := openbindings.ContextAPIKey(bindCtx)
			if val == "" {
				continue
			}
			switch s.In {
			case "header":
				name := s.Name
				if name == "" {
					name = "Authorization"
				}
				req.Header.Set(name, val)
				applied = true
			case "query":
				if s.Name != "" {
					queryParams.Set(s.Name, val)
					applied = true
				}
			case "cookie":
				if s.Name != "" {
					req.AddCookie(&http.Cookie{Name: s.Name, Value: val})
					applied = true
				}
			}

		case "http":
			switch strings.ToLower(s.Scheme) {
			case "bearer":
				if token := openbindings.ContextBearerToken(bindCtx); token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
					applied = true
				}
			case "basic":
				if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
					req.SetBasicAuth(u, p)
					applied = true
				}
			}

		case "httpBearer":
			if token := openbindings.ContextBearerToken(bindCtx); token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
				applied = true
			}

		case "oauth2":
			token := openbindings.ContextBearerToken(bindCtx)
			if token == "" {
				token = openbindings.ContextString(bindCtx, "accessToken")
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
				applied = true
			}

		case "userPassword":
			if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
				req.SetBasicAuth(u, p)
				applied = true
			}
		}
	}

	return applied, queryParams
}

// applyCredentialsFallback applies credentials using sensible defaults when
// no securitySchemes are defined in the spec.
func applyCredentialsFallback(req *http.Request, bindCtx map[string]any) {
	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		req.SetBasicAuth(u, p)
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		req.Header.Set("Authorization", "ApiKey "+key)
	}
}

// declaredContentType returns the operation's declared message contentType
// (the AsyncAPI document's answer to the decode question), falling back to
// the document default, else "" (the text lane). This is the SPEC answer;
// it never reads payload bytes.
func declaredContentType(doc *document, asyncOp *asyncOperation) string {
	if asyncOp == nil {
		return ""
	}
	for _, ref := range asyncOp.Messages {
		if m := resolveMessageRef(doc, ref); m != nil && m.ContentType != "" {
			return m.ContentType
		}
	}
	if asyncOp.Reply != nil {
		for _, ref := range asyncOp.Reply.Messages {
			if m := resolveMessageRef(doc, ref); m != nil && m.ContentType != "" {
				return m.ContentType
			}
		}
	}
	return ""
}

// builtinDecodeFor is the asyncapi builtin decoder: strict JSON when the
// DECLARED message contentType is application/json or a +json suffix
// (a declared-JSON payload that fails to parse is a lying producer — a
// loud terminal, never a silent string), text otherwise. The
// `{"error":...}`/`{"data":...}` convention unwrapping LEFT the builtin
// (round-4 unification): a consumer whose stream speaks it attaches an
// OutputDecoder — a returned error is terminal, which IS the override
// channel for error-frame conventions.
func builtinDecodeFor(contentType string) openbindings.OutputDecoder {
	isJSON := isJSONContentType(contentType)
	return func(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
		if len(raw.Body) == 0 {
			return nil, nil
		}
		if isJSON {
			var parsed any
			if err := json.Unmarshal(raw.Body, &parsed); err != nil {
				return nil, &openbindings.InvocationError{
					Code:    openbindings.ErrCodeResponseError,
					Message: fmt.Sprintf("message declares %q but the payload is not valid JSON: %v", contentType, err),
				}
			}
			return parsed, nil
		}
		return string(raw.Body), nil
	}
}

// decodeTrailer builds the §4.5.2 x-ob-decode stamp (and the fixed
// x-ob-classify not-consulted stamp — asyncapi runs no classifier) for a
// successful message decode, given the builtin decode provenance token.
func decodeTrailer(hooks *openbindings.InvokeHooks, builtinDecode string) openbindings.Metadata {
	decode := builtinDecode
	if hooks.DecodeDecidedBy() == "hook" {
		decode = "hook"
	}
	return openbindings.Metadata{
		"x-ob-decode":   {decode},
		"x-ob-classify": {"not-consulted"},
	}
}

// isJSONContentType mirrors the openapi rule: application/json or any
// +json structured-suffix type; absent/unparseable → NOT JSON. Never
// sniffed.
func isJSONContentType(contentType string) bool {
	mt := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// siteFor completes the core-stamped site with the format-known Target
// (the resolved server URL).
func siteFor(args *openbindings.BindingInvocationArgs, serverURL string) openbindings.InvokeSite {
	var site openbindings.InvokeSite
	if args.Site != nil {
		site = *args.Site
	} else {
		site.Format = args.Source.Format
		site.Ref = args.Ref
	}
	if site.Target == "" {
		site.Target = serverURL
	}
	return site
}
