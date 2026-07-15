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
// The action is read from the DESCRIBED APPLICATION's perspective — AsyncAPI
// 3.0's own rule — and an invocation is the counterparty (ASYNC-P-02,
// spec/binding-specs/asyncapi): invoking a `send` operation SUBSCRIBES to
// what the application sends; invoking a `receive` operation PUBLISHES what
// the application expects to receive. The artifact is never read as
// describing the invoker.
//
// One entrypoint (runBinding) drives every cell against the binding-facing
// BindingHandle:
//
//	receive + http/https  unary publish: one input -> request body (POST),
//	                      response -> at most one output
//	receive + ws/wss      client-streaming publish: every input -> one
//	                      socket frame; the caller closing input ends it
//	send + http/https     SSE subscription: server events -> outputs
//	send + ws/wss         streaming subscription (bidi-capable): socket
//	                      frames -> outputs, caller inputs forward as frames
//
// All pre-dispatch failures (bad ref, missing server, missing context,
// missing publish input) are raised via FireError BEFORE any network I/O,
// per the binding-author contract.

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

	// The complementary perspective (ASYNC-P-02): `receive` means the
	// described application receives, so invoking PUBLISHES; `send` means
	// it sends, so invoking SUBSCRIBES.
	switch asyncOp.Action {
	case "receive":
		switch protocol {
		case "ws", "wss":
			runWSPublish(ctx, pool, serverURL, address, doc, &asyncOp, args, h)
		case "http", "https":
			runUnaryPublish(ctx, client, serverURL, address, doc, &asyncOp, args, h)
		default:
			h.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceConfigError,
				Message: fmt.Sprintf("receive (publish) not supported for protocol %q (supported: http, https, ws, wss)", protocol),
			})
		}
	case "send":
		switch protocol {
		case "ws", "wss":
			runWSSubscribe(ctx, pool, serverURL, address, doc, &asyncOp, args, h)
		case "http", "https":
			runSSESubscribe(ctx, client, serverURL, address, doc, &asyncOp, args, h)
		default:
			h.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeSourceConfigError,
				Message: fmt.Sprintf("send (subscribe) not supported for protocol %q (supported: http, https, ws, wss)", protocol),
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
// family, or "" when the scheme family is unmapped — a scheme this SDK
// cannot itself resolve (see unmappedRequirementType, which requiredContext
// consults instead of dropping the scheme, per the R2.c ruling).
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

// unmappedRequirementType derives the R2.c surfaced-requirement type for a
// scheme family requirementType doesn't map: "auth.http.<scheme>" for an
// HTTP auth scheme other than bearer/basic (e.g. "auth.http.digest"), or
// "auth." + the artifact's own type verbatim otherwise (e.g.
// "auth.scramSha256", "auth.X509"). The alternative stays discoverable to a
// runtime with a resolver for that family, rather than silently dropped.
func unmappedRequirementType(s securityScheme) string {
	if s.Type == "http" {
		if s.Scheme == "" {
			// A missing scheme value degrades to the bare family, never a
			// trailing dot (TS parity).
			return "auth.http"
		}
		return "auth.http." + strings.ToLower(s.Scheme)
	}
	return "auth." + s.Type
}

// requiredContext computes the context the binding requires for this
// operation, or nil when the provided context already satisfies it (or the
// doc declares nothing checkable). The declaration semantics are AsyncAPI
// 3.0's, incorporated, and they are CONJUNCTIVE (ASYNC-P-07): the targeted
// server's `security` applies, and the operation's `security`, when
// declared, applies IN ADDITION. Within each declared list — a flat LIST of
// Security Scheme Objects or Reference Objects, not OpenAPI-style
// requirement-maps — satisfying any ONE entry suffices. That OR-within,
// AND-across shape maps onto the challenge contract as a cross product:
// each alternative pairs one resolvable server entry with one resolvable
// operation entry (or is a single entry when only one list is declared).
// An unresolvable $ref is skipped entirely (nothing to check); an entry
// whose scheme family requirementType doesn't map is SURFACED with a
// derived type (R2.c ruling) rather than dropped, so the alternative stays
// discoverable to a runtime with a resolver for it. Side-effect-free;
// shared by runBinding and PrepareBinding.
func requiredContext(doc *document, asyncOp *asyncOperation, serverURL string, ctx map[string]any) *openbindings.ContextRequiredDetails {
	serverReqs := resolveRequirementList(doc, serverSecurityRequirements(doc), serverURL)
	opReqs := resolveRequirementList(doc, operationSecurityRequirements(asyncOp), serverURL)

	var alternatives []openbindings.ContextAlternative
	switch {
	case len(serverReqs) > 0 && len(opReqs) > 0:
		for _, s := range serverReqs {
			for _, o := range opReqs {
				reqs := []openbindings.ContextRequirement{s}
				// The same scheme declared on both levels is one
				// requirement, not a duplicated conjunct.
				if o.Type != s.Type || o.Name != s.Name {
					reqs = append(reqs, o)
				}
				alternatives = append(alternatives, openbindings.ContextAlternative{Requirements: reqs})
			}
		}
	case len(serverReqs) > 0:
		for _, s := range serverReqs {
			alternatives = append(alternatives, openbindings.ContextAlternative{Requirements: []openbindings.ContextRequirement{s}})
		}
	case len(opReqs) > 0:
		for _, o := range opReqs {
			alternatives = append(alternatives, openbindings.ContextAlternative{Requirements: []openbindings.ContextRequirement{o}})
		}
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

// oauth2Requirement builds an auth.oauth2 requirement carrying the SELECTED
// flow's grantType (R2.b ruling) alongside its authorize/token URLs and
// scopes, under the binding-invoker contract's convention field names
// (grantType, authorizeUrl, tokenUrl, scopes) — mirrors openapi's
// oauth2Requirement exactly. Fixed priority, surfaced not changed:
// authorizationCode > implicit > password > clientCredentials, the last two
// selected only when they carry a tokenUrl (the field both formats restrict
// them to). Relative URLs are resolved against the server URL.
func oauth2Requirement(s securityScheme, serverURL string) openbindings.ContextRequirement {
	req := openbindings.ContextRequirement{Type: "auth.oauth2"}
	if s.Flows == nil {
		return req
	}
	var flow *oauthFlow
	var grantType string
	switch {
	case s.Flows.AuthorizationCode != nil:
		flow, grantType = s.Flows.AuthorizationCode, "authorization_code"
	case s.Flows.Implicit != nil:
		flow, grantType = s.Flows.Implicit, "implicit"
	case s.Flows.Password != nil && s.Flows.Password.TokenURL != "":
		flow, grantType = s.Flows.Password, "password"
	case s.Flows.ClientCredentials != nil && s.Flows.ClientCredentials.TokenURL != "":
		flow, grantType = s.Flows.ClientCredentials, "client_credentials"
	}
	if flow == nil {
		return req
	}
	extra := map[string]any{"grantType": grantType}
	if flow.AuthorizationURL != "" {
		extra["authorizeUrl"] = absolutizeURL(flow.AuthorizationURL, serverURL)
	}
	if flow.TokenURL != "" {
		extra["tokenUrl"] = absolutizeURL(flow.TokenURL, serverURL)
	}
	if len(flow.Scopes) > 0 {
		scopes := make([]string, 0, len(flow.Scopes))
		for k := range flow.Scopes {
			scopes = append(scopes, k)
		}
		sort.Strings(scopes)
		extra["scopes"] = scopes
	}
	req.Extra = extra
	return req
}

// absolutizeURL resolves a possibly-relative flow URL against the server
// base; absolute URLs pass through unchanged. Mirrors openapi's
// absolutizeURL (same behavior; kept local since the format packages don't
// share private helpers).
func absolutizeURL(ref, baseURL string) string {
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if u.IsAbs() {
		return ref
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

// serverSecurityRequirements returns the security list of the doc server the
// connection targets (the same server pickDocServer selects), so the
// requirements always describe the server actually dialed.
func serverSecurityRequirements(doc *document) []securityRequirement {
	if server := pickDocServer(doc); server != nil {
		return server.Security
	}
	return nil
}

// operationSecurityRequirements returns the operation's own security list.
// It never displaces the server's: the two lists are conjunctive
// (ASYNC-P-07) — the server's security applies, and the operation's applies
// in addition.
func operationSecurityRequirements(asyncOp *asyncOperation) []securityRequirement {
	if asyncOp == nil {
		return nil
	}
	return asyncOp.Security
}

// resolveRequirementList resolves one declared security list into concrete
// ContextRequirements, in declaration order: unresolvable $refs are skipped
// (not checkable, not enforced, never degraded); unmapped scheme families
// are surfaced with a derived type (R2.c). Each requirement carries the
// components.securitySchemes key its entry's $ref resolved through as Name
// (R2.a; empty for inline schemes).
func resolveRequirementList(doc *document, requirements []securityRequirement, serverURL string) []openbindings.ContextRequirement {
	var out []openbindings.ContextRequirement
	for _, entry := range requirements {
		scheme, ok := resolveSecurityRequirement(doc, entry)
		if !ok {
			continue
		}
		var req openbindings.ContextRequirement
		if typ := requirementType(scheme); typ != "" {
			if typ == "auth.oauth2" {
				req = oauth2Requirement(scheme, serverURL)
			} else {
				req = openbindings.ContextRequirement{Type: typ}
			}
		} else {
			req = openbindings.ContextRequirement{Type: unmappedRequirementType(scheme)}
		}
		if scheme.Description != "" {
			req.Description = scheme.Description
		}
		req.Name = securityRequirementName(entry)
		out = append(out, req)
	}
	return out
}

// resolveSecurityRequirement resolves one `security` list entry to a concrete
// securityScheme: a $ref is looked up by name in
// doc.Components.SecuritySchemes; an inline entry (no $ref) is used exactly
// as declared. ok is false when a $ref cannot be resolved — an unresolvable
// reference is not checkable, not enforced, never degraded into a weaker
// requirement.
func resolveSecurityRequirement(doc *document, req securityRequirement) (securityScheme, bool) {
	if req.Ref != "" {
		if doc.Components == nil {
			return securityScheme{}, false
		}
		scheme, ok := doc.Components.SecuritySchemes[extractRefName(req.Ref)]
		return scheme, ok
	}
	if req.Type == "" {
		// Neither a $ref nor an inline scheme (e.g. a stray empty object):
		// nothing to check.
		return securityScheme{}, false
	}
	return req.securityScheme, true
}

// securityRequirementName returns the components.securitySchemes key a
// security list entry's $ref resolves through (the R2.a ruling's Name),
// or "" for an inline scheme object — it has no addressable name.
func securityRequirementName(req securityRequirement) string {
	if req.Ref == "" {
		return ""
	}
	return extractRefName(req.Ref)
}

// ---------------------------------------------------------------------------
// Publish over HTTP (`receive` action): unary POST
// ---------------------------------------------------------------------------

func runUnaryPublish(ctx context.Context, client *http.Client, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
	// Unary: the one input IS the message payload (ASYNC-P-03). A publish
	// invocation requires an input value — this family defines no empty
	// message, so absence is a pre-dispatch refusal, never an empty-object
	// substitute. An operation-layer call for an operation declaring no
	// input (Binding != nil, InputSchema == nil) is refused up front:
	// callers of no-input operations never write, so reading would park.
	if args.Binding != nil && args.InputSchema == nil {
		h.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeMissingInput,
			Message: "publish invocation requires an input message (the input is the message; the operation declares no input)",
		})
		return
	}
	first, rerr := h.ReadInput(ctx)
	if rerr == io.EOF {
		h.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeMissingInput,
			Message: "publish invocation requires an input message",
		})
		return
	}
	if rerr != nil {
		return // invocation already terminal (or cancelled)
	}
	_ = h.CloseInput()

	body, err := json.Marshal(first)
	if err != nil {
		h.FireError(openbindings.AsInvocationError(err))
		return
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
		builtinDecodeFor(replyContentType(doc, asyncOp)))
	if derr != nil {
		h.FireError(openbindings.AsInvocationError(derr))
		return
	}

	// Success provenance stamps (the conventions record,
	// spec/formats/README.md): decode is spec/content-type (the message's
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
// Subscribe over HTTP (`send` action): SSE
// ---------------------------------------------------------------------------

func runSSESubscribe(ctx context.Context, client *http.Client, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
	// The described application sends; we subscribe. An SSE subscription
	// takes no input: input closes on entry, and a late write rejects
	// non-terminally at the handle (the refusal surface for supplied input).
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
// Subscribe over WebSocket (`send` action): bidi-capable, on a pooled socket
// ---------------------------------------------------------------------------

func runWSSubscribe(ctx context.Context, pool *wsPool, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
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

	// Inputs -> socket: the duplex lane — caller-supplied input values
	// forward as frames, and the caller closing input does NOT end the
	// subscription (outputs keep flowing). The pump exits on input EOF, on
	// invocation terminal, or when ctx (bound to the invocation's lifetime)
	// ends.
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
	// CloseOutput; socket error or backpressure overflow -> ERR_STREAM_ERROR.
	// Overflow fails only this subscription — the shared reader keeps
	// broadcasting to the pooled connection's other listeners untouched.
	for {
		res, ok := sub.next(ctx)
		if !ok {
			return // invocation terminated (cancelled) while waiting
		}
		if res.Overflowed {
			h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: res.OverflowMsg})
			return
		}
		if res.Closed {
			if res.CloseErr != nil {
				h.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: res.CloseErr.Error()})
			} else {
				h.CloseOutput()
			}
			return
		}
		out, derr := decodeWSFrame(args, siteFor(args, serverURL), messageContentType(doc, asyncOp), res.Frame)
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
	for _, named := range resolveSecuritySchemes(doc, asyncOp) {
		switch requirementType(named.Scheme) {
		case "auth.bearer", "auth.oauth2":
			return true
		}
	}
	return false
}

// Backpressure bounds for the undelivered-frame buffer between a pooled
// socket's broadcast and one subscription's consumer: whichever
// bound trips first fails THAT subscription loudly rather than buffering
// unboundedly (bounded-queue-fail-loud, per spec/formats/asyncapi.md's WS
// slow-consumer ruling — Redis client-output-buffer-limit, NATS
// slow-consumer, and MQTT max_queued_messages are the pub/sub-ecosystem
// precedent, and NATS pairs a count bound with a byte bound the same way).
// Reference-package defaults, not spec-mandated numbers. var, not const, so
// tests can lower them instead of pushing the full volume through a test
// socket.
var (
	maxWSBufferedFrames = 1024
	maxWSBufferedBytes  = 64 * 1024 * 1024 // 64 MiB
)

// wsSubscription buffers broadcast frames from a pooled socket for one
// consumer, preserving arrival order without blocking the shared reader
// goroutine. The buffer is itself bounded (maxWSBufferedFrames /
// maxWSBufferedBytes, whichever trips first): once a consumer stops
// draining, push() stops accepting new frames for THIS subscription only —
// the shared reader keeps broadcasting to every other listener on the
// pooled connection unaffected — and next() surfaces the terminal error
// after draining whatever was already buffered (drain-before-terminal).
// runWSSubscribe's deferred unsubscribe() detaches this listener once the
// terminal fires; the pooled socket itself is never touched.
type wsSubscription struct {
	mu          sync.Mutex
	frames      [][]byte
	bufBytes    int
	closed      bool
	closeErr    error
	overflowed  bool
	overflowMsg string
	notify      chan struct{} // 1-buffered wake signal
}

func newWSSubscription() *wsSubscription {
	return &wsSubscription{notify: make(chan struct{}, 1)}
}

// push appends a broadcast frame unless the subscription has already
// overflowed or closed. Tripping either bound marks the subscription
// overflowed and drops this (and every subsequent) frame — the tripping
// frame itself is never buffered, mirroring the TS reference invoker.
func (s *wsSubscription) push(frame []byte) {
	s.mu.Lock()
	if s.overflowed || s.closed {
		s.mu.Unlock()
		return
	}
	switch {
	case len(s.frames) >= maxWSBufferedFrames:
		s.overflowed = true
		s.overflowMsg = fmt.Sprintf("backpressure overflow: more than %d undelivered frames", maxWSBufferedFrames)
	case s.bufBytes+len(frame) > maxWSBufferedBytes:
		s.overflowed = true
		s.overflowMsg = fmt.Sprintf("backpressure overflow: more than %d undelivered bytes", maxWSBufferedBytes)
	default:
		s.frames = append(s.frames, frame)
		s.bufBytes += len(frame)
	}
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

// wsSubResult is one dequeued step of a wsSubscription: exactly one of
// (Frame set), Overflowed, or Closed is true.
type wsSubResult struct {
	Frame       []byte
	Overflowed  bool
	OverflowMsg string
	Closed      bool
	CloseErr    error
}

// next returns the next frame (buffered frames always drain first before
// Overflowed or Closed surfaces), or ok=false when ctx ends first.
func (s *wsSubscription) next(ctx context.Context) (res wsSubResult, ok bool) {
	for {
		s.mu.Lock()
		if len(s.frames) > 0 {
			frame := s.frames[0]
			s.frames = s.frames[1:]
			s.bufBytes -= len(frame)
			s.mu.Unlock()
			return wsSubResult{Frame: frame}, true
		}
		if s.overflowed {
			msg := s.overflowMsg
			s.mu.Unlock()
			return wsSubResult{Overflowed: true, OverflowMsg: msg}, true
		}
		if s.closed {
			err := s.closeErr
			s.mu.Unlock()
			return wsSubResult{Closed: true, CloseErr: err}, true
		}
		s.mu.Unlock()

		select {
		case <-s.notify:
		case <-ctx.Done():
			return wsSubResult{}, false
		}
	}
}

// ---------------------------------------------------------------------------
// Publish over WebSocket (`receive` action): client-streaming, pooled socket
// ---------------------------------------------------------------------------

func runWSPublish(ctx context.Context, pool *wsPool, serverURL, address string, doc *document, asyncOp *asyncOperation, args *openbindings.BindingInvocationArgs, h handle) {
	// A publish invocation requires input — the input IS the message
	// (ASYNC-P-03); this family defines no empty message. An operation-layer
	// call for an operation declaring no input is refused before dispatch
	// (callers of no-input operations never write, so reading would park).
	if args.Binding != nil && args.InputSchema == nil {
		h.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeMissingInput,
			Message: "publish invocation requires an input message (the input is the message; the operation declares no input)",
		})
		return
	}

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
	// input after at least one message completes the call with zero outputs
	// (a publish yields no outputs; frames the server sends during the
	// exchange are discarded — a defined disposal; auth rides the upgrade
	// request, never the message body). Closing with zero messages sent is
	// the streaming face of the same refusal: nothing was published.
	sent := 0
	for {
		msg, rerr := h.ReadInput(ctx)
		if rerr == io.EOF {
			if sent == 0 {
				h.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeMissingInput,
					Message: "publish invocation requires an input message (input closed with no messages sent)",
				})
				return
			}
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
		sent++
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
	return args.Hooks.DecodeOutput(site, raw, builtinDecodeFor(messageContentType(doc, asyncOp)))
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
		// never a sniffed parse (payload-independence, per the conventions
		// record's recommended built-in defaults).
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

// namedSecurityScheme pairs a resolved AsyncAPI security scheme with the
// components.securitySchemes key its requirement entry's $ref resolved
// through (empty for an inline scheme) — the same name requiredContext
// stamps onto the ContextRequirement's Name (R2.a ruling), needed here so
// credential application can look up a NAMED apiKey scheme's key via
// ContextAPIKeyFor without re-deriving it.
type namedSecurityScheme struct {
	Name   string
	Scheme securityScheme
}

// resolveSecuritySchemes returns the security schemes applicable to an
// operation, flattened for credential placement: the targeted server's list
// then the operation's list, in declaration order — both apply, the
// conjunctive reading (ASYNC-P-07) — with a scheme declared on both levels
// placed once. An entry that fails to resolve (a dangling $ref) is dropped.
func resolveSecuritySchemes(doc *document, asyncOp *asyncOperation) []namedSecurityScheme {
	var result []namedSecurityScheme
	seen := map[string]bool{}
	for _, requirements := range [][]securityRequirement{serverSecurityRequirements(doc), operationSecurityRequirements(asyncOp)} {
		for _, req := range requirements {
			if scheme, ok := resolveSecurityRequirement(doc, req); ok {
				key := scheme.Type + "\x00" + scheme.Scheme + "\x00" + securityRequirementName(req)
				if seen[key] {
					continue
				}
				seen[key] = true
				result = append(result, namedSecurityScheme{Name: securityRequirementName(req), Scheme: scheme})
			}
		}
	}
	return result
}

// applyCredentialsViaSecuritySchemes reads the AsyncAPI doc's securitySchemes
// and operation/server-level security requirements to place credentials exactly
// where the spec declares (header, query, or cookie with the correct name).
// An apiKey/httpApiKey scheme looks up its credential by the
// securitySchemes key first (R2.d ruling: context.apiKeys[name]), falling
// back to the single context.apiKey.
func applyCredentialsViaSecuritySchemes(req *http.Request, doc *document, asyncOp *asyncOperation, bindCtx map[string]any) (applied bool, queryParams url.Values) {
	schemes := resolveSecuritySchemes(doc, asyncOp)
	if len(schemes) == 0 {
		return false, nil
	}

	queryParams = url.Values{}

	for _, named := range schemes {
		s := named.Scheme
		switch s.Type {
		case "apiKey", "httpApiKey":
			val := openbindings.ContextAPIKeyFor(bindCtx, named.Name)
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

// messageContentType returns the effective content type of the operation's
// own messages — the declarations governing a subscription's outputs and a
// publish's input encoding (direction-correct decode, ASYNC-P-05) —
// falling back to the document default, else "" (the text lane). This is
// the SPEC answer; it never reads payload bytes.
func messageContentType(doc *document, asyncOp *asyncOperation) string {
	if asyncOp == nil {
		return ""
	}
	for _, ref := range asyncOp.Messages {
		if m := resolveMessageRef(doc, ref); m != nil && m.ContentType != "" {
			return m.ContentType
		}
	}
	return doc.DefaultContentType
}

// replyContentType returns the effective content type of the operation's
// REPLY-side messages — the declarations governing a publish invocation's
// output: the response decodes by the reply side (direction-correct
// decode, ASYNC-P-05) — falling back to the document default, else ""
// (the text lane).
func replyContentType(doc *document, asyncOp *asyncOperation) string {
	if asyncOp == nil {
		return ""
	}
	if asyncOp.Reply != nil {
		for _, ref := range asyncOp.Reply.Messages {
			if m := resolveMessageRef(doc, ref); m != nil && m.ContentType != "" {
				return m.ContentType
			}
		}
	}
	return doc.DefaultContentType
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

// decodeTrailer builds the x-ob-decode provenance stamp (the conventions
// record, spec/formats/README.md) — and the fixed x-ob-classify
// not-consulted stamp, asyncapi runs no classifier — for a
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
