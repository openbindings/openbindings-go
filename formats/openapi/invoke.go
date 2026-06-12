package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

const maxResponseBytes = 10 * 1024 * 1024 // 10 MB

// classifyHTTPError maps a transport-level error from http.Client.Do to a
// standard SDK error code. Cancellation and deadlines from the caller's context
// are surfaced as cancelled/timeout, network errors as connect_failed, and
// anything else as the generic execution_failed.
func classifyHTTPError(ctx context.Context, err error) string {
	if err == nil {
		return openbindings.ErrCodeExecutionFailed
	}
	// Prefer the context's reason when the caller cancelled or set a deadline.
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.Canceled) {
			return openbindings.ErrCodeCancelled
		}
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return openbindings.ErrCodeTimeout
		}
	}
	if errors.Is(err, context.Canceled) {
		return openbindings.ErrCodeCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return openbindings.ErrCodeTimeout
	}
	// net.Error covers DNS failures, refused connections, TLS handshake errors,
	// and other transport-layer problems. A timeout flagged at this level is
	// typically a per-dial deadline rather than a caller-set context deadline.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return openbindings.ErrCodeTimeout
		}
		return openbindings.ErrCodeConnectFailed
	}
	return openbindings.ErrCodeExecutionFailed
}

// runBinding invokes one OpenAPI binding, driving the invocation handle.
// All pre-dispatch failures (bad ref, missing server URL, unresolvable
// operation, missing context) terminate the handle before any network side
// effect; the response is then emitted as one output (or a stream of outputs
// for a `text/event-stream` response).
func runBinding(ctx context.Context, client *http.Client, args *openbindings.BindingInvocationArgs, inv openbindings.BindingHandle[any, any], doc *openapi3.T) {
	// Bound all HTTP I/O to the invocation's lifetime: caller Cancel(), an
	// abandoned output stream, or upstream ctx cancellation tears down the
	// in-flight request or SSE stream.
	bctx, stop := openbindings.DoneContext(ctx, inv.Done())
	defer stop()

	// ----- Pre-side-effect resolution. -----

	pathTemplate, method, err := parseRef(args.Ref)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeInvalidRef, Message: err.Error()})
		return
	}

	baseURL, err := resolveBaseURLWithLocation(doc, args.Context, args.Source.Location)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: err.Error()})
		return
	}

	if doc.Paths == nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "OpenAPI document has no paths defined"})
		return
	}
	pathItem := doc.Paths.Find(pathTemplate)
	if pathItem == nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeRefNotFound, Message: fmt.Sprintf("path %q not in OpenAPI doc", pathTemplate)})
		return
	}
	op := pathItem.GetOperation(strings.ToUpper(method))
	if op == nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeRefNotFound, Message: fmt.Sprintf("method %q not in path %q", method, pathTemplate)})
		return
	}

	// CONTEXT_REQUIRED is raised before any input is consumed and before any
	// network I/O, so a no-input-consumed retry (after the operation layer
	// resolves context) is safe.
	if details := requiredContext(doc, op, args.Context, baseURL); details != nil {
		inv.FireError(openbindings.NewContextRequiredError(
			"OpenAPI operation requires authentication context", details))
		return
	}

	// ----- Input flows through the handle, not the args. An operation with
	// no parameters and no request body takes no input. -----
	allParams := mergeParameters(pathItem.Parameters, op.Parameters)
	inputMap := map[string]any{}
	switch {
	case args.Binding != nil && args.InputSchema == nil:
		// Operation-layer no-input convention (checked BEFORE the
		// source-derived detection below): the call came through the
		// operation layer for an operation that declares no input. Callers
		// of no-input operations never write nor close, so reading would
		// park forever. Dispatch with empty input even when the OpenAPI doc
		// declares params (e.g. cookie-only params the OBI did not express).
		_ = inv.CloseInput()
	case len(allParams) == 0 && !hasRequestBody(op):
		_ = inv.CloseInput()
	default:
		v, rerr := inv.ReadInput(bctx)
		switch {
		case errors.Is(rerr, io.EOF):
			// Bare close: with a required parameter or required requestBody
			// the dispatch cannot succeed — fire ERR_MISSING_INPUT before
			// any network I/O (cross-SDK parity). Otherwise parameters and
			// body are optional; proceed with an empty input.
			if missing := requiredInputMissing(allParams, op); missing != "" {
				inv.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeMissingInput,
					Message: missing,
				})
				return
			}
		case rerr != nil:
			inv.FireError(openbindings.AsInvocationError(rerr))
			return
		default:
			if m, ok := openbindings.ToStringAnyMap(v); ok {
				inputMap = m
			}
		}
		_ = inv.CloseInput()
	}

	resolvedPath, queryParams, headerParams, bodyFields := classifyInput(allParams, inputMap, pathTemplate)

	reqURL := baseURL + resolvedPath
	if len(queryParams) > 0 {
		q := url.Values{}
		for k, v := range queryParams {
			q.Set(k, fmt.Sprintf("%v", v))
		}
		reqURL += "?" + q.Encode()
	}

	var bodyReader io.Reader
	var contentType string
	if hasRequestBody(op) {
		if isMultipartFormData(op) {
			buf, ct, err := buildMultipartBody(op, bodyFields)
			if err != nil {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()})
				return
			}
			bodyReader = buf
			contentType = ct
		} else {
			bodyBytes, err := json.Marshal(bodyFields)
			if err != nil {
				inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()})
				return
			}
			bodyReader = bytes.NewReader(bodyBytes)
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(bctx, strings.ToUpper(method), reqURL, bodyReader)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: err.Error()})
		return
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Accept both JSON and Server-Sent Events. Servers that support SSE will
	// return text/event-stream when streaming; otherwise we get JSON as before.
	req.Header.Set("Accept", "application/json, text/event-stream")

	for k, v := range headerParams {
		req.Header.Set(k, fmt.Sprintf("%v", v))
	}

	applyHTTPContext(req, doc, op, args.Context)

	resp, err := client.Do(req)
	if err != nil {
		if bctx.Err() != nil {
			return // cancelled; the handle is already terminal
		}
		inv.FireError(&openbindings.InvocationError{Code: classifyHTTPError(bctx, err), Message: err.Error()})
		return
	}

	// Leading metadata precedes the first emit.
	_ = inv.SetHeader(headerMetadata(resp.Header))

	// SSE dispatch: a 2xx response with text/event-stream content type is a
	// streaming response. Hand the (still-open) response to the SSE streamer
	// which takes ownership of closing the body and the terminal transition.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && isSSEContentType(resp.Header.Get("Content-Type")) {
		streamSSE(bctx, resp, inv)
		return
	}

	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if bctx.Err() != nil {
			return
		}
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: err.Error()})
		return
	}
	if len(respBody) > maxResponseBytes {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes),
		})
		return
	}

	var output any
	if len(respBody) > 0 {
		trimmed := strings.TrimSpace(string(respBody))
		if openbindings.MaybeJSON(trimmed) {
			var parsed any
			if json.Unmarshal(respBody, &parsed) == nil {
				output = parsed
			} else {
				output = string(respBody)
			}
		} else {
			output = string(respBody)
		}
	}

	if resp.StatusCode >= 400 {
		ierr := openbindings.HTTPError(resp.StatusCode, resp.Status)
		// Surface the response body in Details so callers can inspect the
		// error payload, without conflating it with a successful output.
		if d, ok := ierr.Details.(map[string]any); ok && output != nil {
			d["body"] = output
		}
		inv.FireError(ierr)
		return
	}

	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// headerMetadata clones HTTP response headers into invocation Metadata.
func headerMetadata(h http.Header) openbindings.Metadata {
	md := make(openbindings.Metadata, len(h))
	for k, vs := range h {
		md[k] = append([]string(nil), vs...)
	}
	return md
}

// requiredContext derives the operation's authentication requirements from
// the document's securitySchemes and the operation-level (falling back to
// document-level) security requirements. It returns a ContextRequiredDetails
// when the call needs context the caller has not supplied, or nil when the
// operation needs no authentication, allows anonymous access, or the present
// context already satisfies a requirement.
//
// OpenAPI `security` is a disjunction (OR) of requirement objects, each a
// conjunction (AND) of scheme names — exactly the alternatives/requirements
// shape of ContextRequiredDetails.
func requiredContext(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, baseURL string) *openbindings.ContextRequiredDetails {
	var requirements *openapi3.SecurityRequirements
	if op != nil && op.Security != nil {
		requirements = op.Security
	} else {
		requirements = &doc.Security
	}
	if requirements == nil || len(*requirements) == 0 {
		return nil // no authentication required
	}
	if doc.Components == nil || doc.Components.SecuritySchemes == nil {
		return nil
	}

	var alternatives []openbindings.ContextAlternative
	for _, secReq := range *requirements {
		if len(secReq) == 0 {
			// An empty requirement object means anonymous access is allowed;
			// no challenge is warranted for the whole operation.
			return nil
		}
		var reqs []openbindings.ContextRequirement
		expressible := true
		for schemeName := range secReq {
			ref, ok := doc.Components.SecuritySchemes[schemeName]
			if !ok || ref.Value == nil {
				expressible = false
				break
			}
			req, ok := schemeToRequirement(ref.Value, baseURL)
			if !ok {
				expressible = false
				break
			}
			reqs = append(reqs, req)
		}
		if expressible && len(reqs) > 0 {
			alternatives = append(alternatives, openbindings.ContextAlternative{Requirements: reqs})
		}
	}
	if len(alternatives) == 0 {
		return nil
	}

	details := &openbindings.ContextRequiredDetails{
		Key:          openbindings.NormalizeEndpoint(baseURL),
		Alternatives: alternatives,
	}
	// Already satisfiable from the supplied context: no challenge needed.
	if openbindings.ContextSatisfies(bindCtx, details) {
		return nil
	}
	return details
}

// schemeToRequirement maps an OpenAPI security scheme to the standard context
// requirement family, carrying the family-specific fields a resolver needs to
// act without out-of-band knowledge (notably oauth2 flow endpoints). The bool
// is false for schemes the SDK cannot express (so the alternative is skipped).
func schemeToRequirement(s *openapi3.SecurityScheme, baseURL string) (openbindings.ContextRequirement, bool) {
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "basic":
			return openbindings.ContextRequirement{Type: "auth.basic"}, true
		case "bearer":
			return openbindings.ContextRequirement{Type: "auth.bearer"}, true
		default:
			// digest, negotiate, etc.: not expressible as a context
			// requirement, so the whole alternative is skipped (TS parity).
			return openbindings.ContextRequirement{}, false
		}
	case "apiKey":
		return openbindings.ContextRequirement{Type: "auth.apiKey"}, true
	case "oauth2":
		return oauth2Requirement(s, baseURL), true
	case "openIdConnect":
		// OpenID Connect resolves to an OAuth2 access token; the discovery URL
		// lets a resolver fetch the authorize/token endpoints.
		req := openbindings.ContextRequirement{Type: "auth.oauth2"}
		if s.OpenIdConnectUrl != "" {
			req.Extra = map[string]any{"openIdConnectUrl": absolutizeURL(s.OpenIdConnectUrl, baseURL)}
		}
		return req, true
	default:
		return openbindings.ContextRequirement{}, false
	}
}

// oauth2Requirement builds an auth.oauth2 requirement carrying the flow's
// authorize/token URLs and scopes under the role's convention field names
// (authorizeUrl, tokenUrl, scopes). It prefers the authorization-code flow —
// the only interactive, PKCE-capable flow — then implicit, then any flow with
// an authorization endpoint. Relative URLs are resolved against the base URL.
func oauth2Requirement(s *openapi3.SecurityScheme, baseURL string) openbindings.ContextRequirement {
	req := openbindings.ContextRequirement{Type: "auth.oauth2"}
	if s.Flows == nil {
		return req
	}
	flow := s.Flows.AuthorizationCode
	if flow == nil {
		flow = s.Flows.Implicit
	}
	if flow == nil {
		// Password and clientCredentials flows carry only a tokenUrl in
		// OpenAPI 3.x (authorizationUrl is undefined for them), so select on
		// TokenURL — the endpoint they actually have. Selecting on
		// AuthorizationURL here would never match, leaving these schemes with a
		// bare auth.oauth2 requirement (no tokenUrl/scopes).
		for _, f := range []*openapi3.OAuthFlow{s.Flows.Password, s.Flows.ClientCredentials} {
			if f != nil && f.TokenURL != "" {
				flow = f
				break
			}
		}
	}
	if flow == nil {
		return req
	}
	extra := map[string]any{}
	if flow.AuthorizationURL != "" {
		extra["authorizeUrl"] = absolutizeURL(flow.AuthorizationURL, baseURL)
	}
	if flow.TokenURL != "" {
		extra["tokenUrl"] = absolutizeURL(flow.TokenURL, baseURL)
	}
	if len(flow.Scopes) > 0 {
		scopes := make([]string, 0, len(flow.Scopes))
		for k := range flow.Scopes {
			scopes = append(scopes, k)
		}
		sort.Strings(scopes)
		extra["scopes"] = scopes
	}
	if len(extra) > 0 {
		req.Extra = extra
	}
	return req
}

// absolutizeURL resolves a possibly-relative URL against the server base;
// absolute URLs pass through unchanged.
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

func parseRef(ref string) (path string, method string, err error) {
	ref = strings.TrimPrefix(ref, "#/")

	parts := strings.Split(ref, "/")
	if len(parts) < 3 || parts[0] != "paths" {
		return "", "", fmt.Errorf("ref %q must be in format #/paths/<escaped-path>/<method>", ref)
	}

	method = parts[len(parts)-1]
	pathSegments := parts[1 : len(parts)-1]
	escapedPath := strings.Join(pathSegments, "/")

	path = strings.ReplaceAll(escapedPath, "~1", "/")
	path = strings.ReplaceAll(path, "~0", "~")

	validMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true,
		"delete": true, "head": true, "options": true, "trace": true,
	}
	if !validMethods[strings.ToLower(method)] {
		return "", "", fmt.Errorf("invalid HTTP method %q in ref", method)
	}

	return path, strings.ToLower(method), nil
}

func resolveBaseURL(doc *openapi3.T, ctx map[string]any) (string, error) {
	if meta := openbindings.ContextMetadata(ctx); meta != nil {
		if base, ok := meta["baseURL"].(string); ok && base != "" {
			return strings.TrimRight(base, "/"), nil
		}
	}

	if len(doc.Servers) > 0 {
		serverURL := doc.Servers[0].URL
		if serverURL != "" {
			return strings.TrimRight(serverURL, "/"), nil
		}
	}

	return "", fmt.Errorf("no server URL: set servers in the OpenAPI doc or provide baseURL in context metadata")
}

// resolveBaseURLWithLocation resolves the base URL, falling back to the source
// location's origin when the spec has a relative server URL (e.g. "/api/v3").
func resolveBaseURLWithLocation(doc *openapi3.T, ctx map[string]any, sourceLocation string) (string, error) {
	base, err := resolveBaseURL(doc, ctx)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(base, "http://") || strings.HasPrefix(base, "https://") {
		return base, nil
	}
	if openbindings.IsHTTPURL(sourceLocation) {
		parsed, err := url.Parse(sourceLocation)
		if err == nil {
			origin := parsed.Scheme + "://" + parsed.Host
			return strings.TrimRight(origin+base, "/"), nil
		}
	}
	return base, nil
}

func classifyInput(params openapi3.Parameters, input map[string]any, pathTemplate string) (resolvedPath string, query, headers, body map[string]any) {
	query = map[string]any{}
	headers = map[string]any{}
	body = map[string]any{}
	cookies := map[string]any{}

	paramClassification := map[string]string{}
	for _, paramRef := range params {
		if paramRef == nil || paramRef.Value == nil {
			continue
		}
		paramClassification[paramRef.Value.Name] = paramRef.Value.In
	}

	resolvedPath = pathTemplate
	for name, value := range input {
		classification, isParam := paramClassification[name]
		if !isParam {
			body[name] = value
			continue
		}
		switch classification {
		case "path":
			resolvedPath = strings.ReplaceAll(resolvedPath, "{"+name+"}", fmt.Sprintf("%v", value))
		case "query":
			query[name] = value
		case "header":
			headers[name] = value
		case "cookie":
			cookies[name] = value
		default:
			body[name] = value
		}
	}

	// Declared cookie params travel in a Cookie header (sorted for a
	// deterministic value), never in the body. Context-supplied cookies are
	// appended later by applyHTTPContext via req.AddCookie.
	if len(cookies) > 0 {
		names := make([]string, 0, len(cookies))
		for name := range cookies {
			names = append(names, name)
		}
		sort.Strings(names)
		pairs := make([]string, 0, len(names))
		for _, name := range names {
			pairs = append(pairs, fmt.Sprintf("%s=%v", name, cookies[name]))
		}
		headers["Cookie"] = strings.Join(pairs, "; ")
	}

	return resolvedPath, query, headers, body
}

func hasRequestBody(op *openapi3.Operation) bool {
	return op.RequestBody != nil && op.RequestBody.Value != nil
}

// requiredInputMissing reports why a bare input close cannot satisfy the
// operation: a non-empty string names the first required parameter or the
// required request body. Empty string means an empty request is dispatchable.
func requiredInputMissing(params openapi3.Parameters, op *openapi3.Operation) string {
	for _, paramRef := range params {
		if paramRef != nil && paramRef.Value != nil && paramRef.Value.Required {
			return fmt.Sprintf("operation requires parameter %q", paramRef.Value.Name)
		}
	}
	if hasRequestBody(op) && op.RequestBody.Value.Required {
		return "operation requires a request body"
	}
	return ""
}

// isMultipartFormData returns true when the operation's request body specifies
// multipart/form-data and does NOT also offer application/json (which is preferred).
func isMultipartFormData(op *openapi3.Operation) bool {
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return false
	}
	content := op.RequestBody.Value.Content
	if content == nil {
		return false
	}
	// Prefer JSON when both are available.
	if content.Get("application/json") != nil {
		return false
	}
	return content.Get("multipart/form-data") != nil
}

// buildMultipartBody encodes bodyFields as a multipart/form-data payload.
// Properties whose schema declares type "string" with format "binary" are
// expected to carry []byte values and are written as file parts. All other
// properties are serialized as string form fields.
func buildMultipartBody(op *openapi3.Operation, bodyFields map[string]any) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	binaryFields := resolveBinaryFields(op)

	for name, value := range bodyFields {
		if binaryFields[name] {
			data, ok := value.([]byte)
			if !ok {
				return nil, "", fmt.Errorf("field %q: expected []byte for binary field, got %T", name, value)
			}
			part, err := writer.CreateFormFile(name, name)
			if err != nil {
				return nil, "", fmt.Errorf("create form file %q: %w", name, err)
			}
			if _, err := part.Write(data); err != nil {
				return nil, "", fmt.Errorf("write form file %q: %w", name, err)
			}
		} else {
			var fieldStr string
			switch v := value.(type) {
			case string:
				fieldStr = v
			default:
				b, err := json.Marshal(v)
				if err != nil {
					return nil, "", fmt.Errorf("marshal field %q: %w", name, err)
				}
				fieldStr = string(b)
			}
			if err := writer.WriteField(name, fieldStr); err != nil {
				return nil, "", fmt.Errorf("write field %q: %w", name, err)
			}
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}

	return &buf, writer.FormDataContentType(), nil
}

// resolveBinaryFields inspects the multipart/form-data schema and returns a set
// of property names whose schema is type "string" + format "binary".
func resolveBinaryFields(op *openapi3.Operation) map[string]bool {
	result := map[string]bool{}
	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return result
	}
	mt := op.RequestBody.Value.Content.Get("multipart/form-data")
	if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
		return result
	}
	for name, propRef := range mt.Schema.Value.Properties {
		if propRef == nil || propRef.Value == nil {
			continue
		}
		if propRef.Value.Type.Is("string") && propRef.Value.Format == "binary" {
			result[name] = true
		}
	}
	return result
}

// applyHTTPContext applies opaque binding context (credentials via well-known
// fields, plus transport hints headers/cookies via well-known fields) to an
// HTTP request, using OpenAPI securitySchemes for spec-driven credential
// placement.
func applyHTTPContext(req *http.Request, doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any) {
	if len(bindCtx) > 0 {
		if !applyCredentialsViaSecuritySchemes(req, doc, op, bindCtx) {
			applyCredentialsFallback(req, bindCtx)
		}
	}

	for k, v := range openbindings.ContextHeaders(bindCtx) {
		req.Header.Set(k, v)
	}
	for k, v := range openbindings.ContextCookies(bindCtx) {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
}

// applyCredentialsViaSecuritySchemes reads the OpenAPI doc's securitySchemes
// and operation-level security requirements to place credentials exactly
// where the spec declares (header, query, or cookie with the correct name).
// Credentials are read from well-known context fields.
func applyCredentialsViaSecuritySchemes(req *http.Request, doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any) bool {
	schemes := resolveSecuritySchemes(doc, op)
	if len(schemes) == 0 {
		return false
	}

	applied := false

	for _, scheme := range schemes {
		if scheme.Value == nil {
			continue
		}
		s := scheme.Value

		switch s.Type {
		case "apiKey":
			val := openbindings.ContextAPIKey(bindCtx)
			if val == "" {
				continue
			}
			switch s.In {
			case "header":
				req.Header.Set(s.Name, val)
				applied = true
			case "query":
				q := req.URL.Query()
				q.Set(s.Name, val)
				req.URL.RawQuery = q.Encode()
				applied = true
			case "cookie":
				req.AddCookie(&http.Cookie{Name: s.Name, Value: val})
				applied = true
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

		case "oauth2", "openIdConnect":
			token := openbindings.ContextString(bindCtx, "accessToken")
			if token == "" {
				token = openbindings.ContextBearerToken(bindCtx)
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
				applied = true
			}
		}
	}

	return applied
}

// resolveSecuritySchemes returns the security scheme refs applicable to an operation.
// Operation-level security overrides top-level; falls back to top-level if not set.
func resolveSecuritySchemes(doc *openapi3.T, op *openapi3.Operation) []*openapi3.SecuritySchemeRef {
	var requirements *openapi3.SecurityRequirements
	if op != nil {
		requirements = op.Security
	}
	if requirements == nil {
		requirements = &doc.Security
	}
	if requirements == nil || len(*requirements) == 0 {
		return nil
	}

	if doc.Components == nil || doc.Components.SecuritySchemes == nil {
		return nil
	}

	var result []*openapi3.SecuritySchemeRef
	seen := map[string]bool{}
	for _, req := range *requirements {
		for schemeName := range req {
			if seen[schemeName] {
				continue
			}
			seen[schemeName] = true
			if ref, ok := doc.Components.SecuritySchemes[schemeName]; ok {
				result = append(result, ref)
			}
		}
	}
	return result
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
