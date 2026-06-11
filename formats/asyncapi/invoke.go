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

type handle = openbindings.BindingHandle[any, any]

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

// runBinding resolves the operation, checks runtime context, and dispatches
// to the protocol-specific runner. Terminates the handle exactly once. ctx is
// expected to be bound to the invocation's lifetime (DoneContext).
func runBinding(ctx context.Context, client *http.Client, pool *wsPool, args *openbindings.BindingInvocationArgs, h handle, doc *Document) {
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

func resolveServer(doc *Document, ctx map[string]any) (url string, protocol string, err error) {
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
			url := proto + "://" + server.Host
			if server.PathName != "" {
				url += server.PathName
			}
			return strings.TrimRight(url, "/"), proto, nil
		}
	}

	return "", "", fmt.Errorf("no supported server found (need http, https, ws, or wss protocol)")
}

// ---------------------------------------------------------------------------
// Context requirements (CONTEXT_REQUIRED negotiation)
// ---------------------------------------------------------------------------

// requirementType maps an AsyncAPI security scheme to a standard requirement
// family, or "" when the scheme family is unknown (not checkable, not
// enforced).
func requirementType(s SecurityScheme) string {
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
// doc declares nothing checkable). Each declared scheme is one alternative:
// satisfying any one suffices. Side-effect-free; shared by runBinding and
// PrepareBinding.
func requiredContext(doc *Document, asyncOp *Operation, serverURL string, ctx map[string]any) *openbindings.ContextRequiredDetails {
	schemes := resolveSecuritySchemes(doc, asyncOp)
	var alternatives []openbindings.ContextAlternative
	for _, s := range schemes {
		typ := requirementType(s)
		if typ == "" {
			continue
		}
		req := openbindings.ContextRequirement{Type: typ}
		if s.Description != "" {
			req.Description = s.Description
		}
		alternatives = append(alternatives, openbindings.ContextAlternative{
			Requirements: []openbindings.ContextRequirement{req},
		})
	}
	if len(alternatives) == 0 {
		return nil
	}

	details := &openbindings.ContextRequiredDetails{
		Key:          openbindings.NormalizeContextKey(serverURL),
		Alternatives: alternatives,
	}
	if openbindings.ContextSatisfies(ctx, details) {
		return nil
	}
	return details
}

// ---------------------------------------------------------------------------
// Send over HTTP: unary POST
// ---------------------------------------------------------------------------

func runHTTPSend(ctx context.Context, client *http.Client, serverURL, address string, doc *Document, asyncOp *Operation, args *openbindings.BindingInvocationArgs, h handle) {
	// Unary: the first input is the message payload. An AsyncAPI send always
	// carries a message.
	first, err := h.ReadInput(ctx)
	if err == io.EOF {
		h.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeMissingInput,
			Message: "send operation requires an input message",
		})
		return
	}
	if err != nil {
		return // invocation already terminal (or cancelled)
	}
	_ = h.CloseInput()

	body := []byte("{}")
	if first != nil {
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

	var output any
	trimmed := strings.TrimSpace(string(respBody))
	if openbindings.MaybeJSON(trimmed) {
		if json.Unmarshal(respBody, &output) != nil {
			output = string(respBody)
		}
	} else {
		output = string(respBody)
	}

	if h.EmitOutput(output) != nil {
		return // invocation terminated while the emit was parked
	}
	h.CloseOutput()
}

// ---------------------------------------------------------------------------
// Receive over HTTP: SSE subscribe
// ---------------------------------------------------------------------------

func runSSEReceive(ctx context.Context, client *http.Client, serverURL, address string, doc *Document, asyncOp *Operation, args *openbindings.BindingInvocationArgs, h handle) {
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
	var dataLines []string
	var totalBytes int

	for scanner.Scan() {
		line := scanner.Text()
		totalBytes += len(line) + 1 // +1 for newline
		if totalBytes > maxResponseBytes {
			h.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeResponseError,
				Message: fmt.Sprintf("SSE stream exceeds %d byte limit", maxResponseBytes),
			})
			return
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}

		if line == "" && len(dataLines) > 0 {
			ev := parseSSEPayload(dataLines)
			dataLines = dataLines[:0]
			if h.EmitOutput(ev) != nil {
				return // invocation terminated while the emit was parked
			}
		}
	}

	if len(dataLines) > 0 {
		if h.EmitOutput(parseSSEPayload(dataLines)) != nil {
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

// parseWSFrame interprets one socket frame. `{"error": ...}` frames are
// terminal stream errors; `{"data": ...}` frames unwrap to the payload;
// everything else passes through (raw string when not JSON).
func parseWSFrame(raw []byte) (out any, terminal *openbindings.InvocationError) {
	var parsed any
	if json.Unmarshal(raw, &parsed) != nil {
		return string(raw), nil
	}
	if obj, ok := parsed.(map[string]any); ok {
		if errVal, hasErr := obj["error"]; hasErr && errVal != nil {
			msg := "server reported an error"
			if em, ok := errVal.(map[string]any); ok {
				if m, ok := em["message"].(string); ok && m != "" {
					msg = m
				}
			}
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeStreamError,
				Message: msg,
				Details: map[string]any{"error": errVal},
			}
		}
		if dataVal, hasData := obj["data"]; hasData {
			return dataVal, nil
		}
	}
	return parsed, nil
}

// ---------------------------------------------------------------------------
// Receive over WebSocket: subscribe (bidi-capable) on a pooled socket
// ---------------------------------------------------------------------------

func runWSReceive(ctx context.Context, pool *wsPool, serverURL, address string, doc *Document, asyncOp *Operation, args *openbindings.BindingInvocationArgs, h handle) {
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
	// upgrades, so the token travels in the first message body.
	if token := openbindings.ContextBearerToken(args.Context); token != "" {
		frame, merr := json.Marshal(map[string]any{"bearerToken": token})
		if merr == nil {
			if werr := pw.send(ctx, frame); werr != nil {
				if ctx.Err() != nil {
					return
				}
				h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: werr.Error()})
				return
			}
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
		out, terminal := parseWSFrame(frame)
		if terminal != nil {
			h.FireError(terminal)
			return
		}
		if h.EmitOutput(out) != nil {
			return // invocation terminated while the emit was parked
		}
	}
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

func runWSSend(ctx context.Context, pool *wsPool, serverURL, address string, doc *Document, asyncOp *Operation, args *openbindings.BindingInvocationArgs, h handle) {
	pw, _, err := pool.acquire(ctx, serverURL, address, doc, asyncOp, args.Context, nil)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: err.Error()})
		return
	}
	defer pw.release()

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

func parseSSEPayload(dataLines []string) any {
	raw := strings.Join(dataLines, "\n")
	var parsed any
	if json.Unmarshal([]byte(raw), &parsed) == nil {
		return parsed
	}
	return raw
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
		var parsed any
		if openbindings.MaybeJSON(strings.TrimSpace(string(body))) && json.Unmarshal(body, &parsed) == nil {
			details["body"] = parsed
		} else {
			details["body"] = string(body)
		}
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
func applyHTTPContext(req *http.Request, doc *Document, asyncOp *Operation, bindCtx map[string]any) {
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

// resolveSecuritySchemes returns the security schemes applicable to an operation.
// Operation-level security overrides server-level; falls back to the first server's
// security if not set on the operation.
func resolveSecuritySchemes(doc *Document, asyncOp *Operation) []SecurityScheme {
	var requirements []map[string][]string

	if asyncOp != nil && len(asyncOp.Security) > 0 {
		requirements = asyncOp.Security
	}

	if len(requirements) == 0 && doc.Servers != nil {
		// Use the first server's security (sorted by name for determinism).
		serverNames := make([]string, 0, len(doc.Servers))
		for name := range doc.Servers {
			serverNames = append(serverNames, name)
		}
		sort.Strings(serverNames)
		for _, name := range serverNames {
			srv := doc.Servers[name]
			if len(srv.Security) > 0 {
				requirements = srv.Security
				break
			}
		}
	}

	if len(requirements) == 0 {
		return nil
	}

	if doc.Components == nil || len(doc.Components.SecuritySchemes) == 0 {
		return nil
	}

	var result []SecurityScheme
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
func applyCredentialsViaSecuritySchemes(req *http.Request, doc *Document, asyncOp *Operation, bindCtx map[string]any) (applied bool, queryParams url.Values) {
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
