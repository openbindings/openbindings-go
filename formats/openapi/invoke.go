package openapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// configOrSourceError maps a server-resolution error to the right terminal: a
// resolvable-missing configuration value (a configRequired signal) becomes a
// config.value CONTEXT_REQUIRED challenge — retryable after resolution (R1a) —
// while any other error stays a terminal ERR_SOURCE_CONFIG_ERROR for source
// misconfiguration no runtime can fix. resolveServer already consulted the
// supplied context and found the value absent, so the challenge fires
// unconditionally; the operation-invoker's bounded resolve-and-retry loop is
// the backstop. config values are non-secret, so the challenge carries an
// empty target (a best-effort key suffices and no server URL has resolved).
func configOrSourceError(err error) *openbindings.InvocationError {
	var cr *configRequired
	if errors.As(err, &cr) {
		req := openbindings.NewConfigValueRequirement(cr.point, cr.key, cr.description, cr.choices, cr.durable)
		return openbindings.NewContextRequiredError(cr.description, &openbindings.ContextRequiredDetails{
			Alternatives: []openbindings.ContextAlternative{{Requirements: []openbindings.ContextRequirement{req}}},
		})
	}
	return &openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: err.Error()}
}

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

// runBinding invokes one OpenAPI binding, driving the invocation handle: one
// HTTP exchange per invocation (openbindings.openapi@1 §8). All pre-dispatch
// refusals — bad ref, unresolvable operation, unflattenable declarations,
// out-of-family request media, unresolvable server, missing context, missing
// path parameters, unmatched input fields, credential collisions — terminate
// the handle before any network side effect, and before consuming input where
// knowable.
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

	if doc.Paths == nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: "OpenAPI document has no paths defined"})
		return
	}
	// Pointer evaluation follows OAS reference resolution (OAPI-D-03): the
	// loader resolves path-item $refs (including 3.1 components.pathItems
	// targets) at load, before this lookup.
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

	// The flattened model's structural refusals (§9.1) are declaration-only:
	// they precede input consumption.
	params := effectiveParameters(pathItem, op)
	if name := unflattenableParam(params); name != "" {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeSourceConfigError,
			Message: fmt.Sprintf("operation declares parameter %q in two different locations: it cannot be represented by the flattened model (OAPI-P-03, unflattenable)", name),
		})
		return
	}
	baseURL, err := resolveServer(doc, pathItem, op, args.Context, args.Source.Location)
	if err != nil {
		inv.FireError(configOrSourceError(err))
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
	case len(params) == 0 && !hasRequestBody(op):
		_ = inv.CloseInput()
	default:
		v, rerr := inv.ReadInput(bctx)
		switch {
		case errors.Is(rerr, io.EOF):
			// Bare close: with a required parameter or required requestBody
			// the dispatch cannot succeed — fire ERR_MISSING_INPUT before
			// any network I/O (cross-SDK parity). Otherwise parameters and
			// body are optional; proceed with an empty input.
			if missing := requiredInputMissing(params, op); missing != "" {
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

	// ----- Routing (§9.1) and body construction (§9.2): still pre-dispatch. -----

	var plans []*bodyPlan
	if requestWillEmitBody(params, inputMap, op) {
		plans, err = planRequestBodies(op)
		if err != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeSourceConfigError, Message: err.Error()})
			return
		}
	}

	var routed *routedInput
	var bodyReader io.Reader
	var contentType string
	if len(plans) == 0 {
		routed, err = routeInput(params, inputMap, pathTemplate, &bodyPlan{})
	} else {
		var reasons []string
		for _, candidate := range configuredRequestPlans(plans, args.Context) {
			if candidateCollides(params, candidate) {
				reasons = append(reasons, fmt.Sprintf("request media candidate %s collides with an independently declared parameter", candidate.mediaType))
				continue
			}
			candidateRouted, routeErr := routeInput(params, inputMap, pathTemplate, candidate)
			if routeErr != nil {
				if errors.Is(routeErr, errMissingPathParam) {
					err = routeErr
					break
				}
				reasons = append(reasons, fmt.Sprintf("%s: %v", candidate.mediaType, routeErr))
				continue
			}
			candidateBody, candidateContentType, buildErr := buildRequestBody(doc, candidate, candidateRouted)
			if buildErr != nil {
				reasons = append(reasons, fmt.Sprintf("%s: %v", candidate.mediaType, buildErr))
				continue
			}
			routed, bodyReader, contentType = candidateRouted, candidateBody, candidateContentType
			break
		}
		if routed == nil && err == nil {
			if len(reasons) == 0 {
				reasons = append(reasons, "configured requestMedia selects no declared supported candidate")
			}
			err = fmt.Errorf("no request media candidate can carry this invocation: %s", strings.Join(reasons, "; "))
		}
	}
	if err != nil {
		code := openbindings.ErrCodeValidationFailed
		if errors.Is(err, errMissingPathParam) {
			code = openbindings.ErrCodeMissingInput
		}
		inv.FireError(&openbindings.InvocationError{Code: code, Message: err.Error()})
		return
	}

	// ----- Channel assembly (§9.6, OAPI-P-10). -----

	placements := credentialPlacements(doc, op, args.Context)
	if err := checkCredentialCollisions(placements, params, routed.populated); err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()})
		return
	}

	queryUnits := routed.queryUnits
	cookieUnits := routed.cookieUnits
	for _, pl := range placements {
		switch pl.channel {
		case "query":
			queryUnits = append(queryUnits, queryEscape(pl.name, false)+"="+queryEscape(pl.value, false))
		case "cookie":
			cookieUnits = append(cookieUnits, pl.name+"="+pl.value)
		}
	}
	// Context-supplied transport-hint cookies (consumer context, not
	// security-scheme credentials) ride after credentials, sorted for
	// determinism.
	if hintCookies := openbindings.ContextCookies(args.Context); len(hintCookies) > 0 {
		names := make([]string, 0, len(hintCookies))
		for k := range hintCookies {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			cookieUnits = append(cookieUnits, k+"="+hintCookies[k])
		}
	}

	reqURL := baseURL + routed.resolvedPath
	if len(queryUnits) > 0 {
		reqURL += "?" + strings.Join(queryUnits, "&")
	}

	req, err := http.NewRequestWithContext(bctx, strings.ToUpper(method), reqURL, bodyReader)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: err.Error()})
		return
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// The Accept header advertises only artifact-declared concrete success
	// media. An empty membership set omits the header.
	if accept := acceptHeader(op); accept != "" {
		req.Header.Set("Accept", accept)
	}

	for _, h := range routed.headers {
		req.Header.Set(h[0], h[1])
	}
	for _, pl := range placements {
		if pl.channel == "header" {
			req.Header.Set(pl.name, pl.value)
		}
	}
	// Context-supplied transport-hint headers (consumer context) apply last.
	for k, v := range openbindings.ContextHeaders(args.Context) {
		req.Header.Set(k, v)
	}
	// One Cookie header (OAPI-P-10): declared cookie parameters in
	// declaration order, credentials appended after.
	if len(cookieUnits) > 0 {
		req.Header.Set("Cookie", strings.Join(cookieUnits, "; "))
	}

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

	site := siteFor(args, baseURL)
	responseDecl := governingResponse(op, resp.StatusCode)
	actualContentType := resp.Header.Get("Content-Type")

	// Interaction-shape dispatch (§8, OAPI-P-06): the shape is bounded by
	// declaration and selected by framing. An operation is streaming-capable
	// iff a declared success response declares text/event-stream; for a
	// streaming-capable operation the response's Content-Type header — never
	// payload bytes — selects between server-streaming and unary. A
	// text/event-stream response on an operation that is NOT
	// streaming-capable contradicts the declaration: a protocol error, never
	// a silent reclassification.
	if isSSEContentType(actualContentType) {
		// Classification is independent of declaration lookup. A non-success
		// final status remains the native HTTP failure even when its body uses
		// event-stream framing.
		status := resp.StatusCode
		ok, cerr := args.Hooks.Classify(site, openbindings.RawResult{
			Status: &status,
			Meta:   headerMetadata(resp.Header),
		}, BuiltinClassify)
		if cerr != nil {
			_ = resp.Body.Close()
			inv.FireError(openbindings.AsInvocationError(cerr))
			return
		}
		if !ok {
			_ = resp.Body.Close()
			inv.FireError(openbindings.HTTPError(resp.StatusCode, resp.Status))
			return
		}
		matched, mediaErr := governingResponseMedia(responseDecl, actualContentType)
		if mediaErr != nil || matched.base != "text/event-stream" {
			_ = resp.Body.Close()
			message := "response arrived as text/event-stream, but the governing response does not declare that media type"
			if mediaErr != nil {
				message += ": " + mediaErr.Error()
			}
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeProtocol,
				Message: message,
			})
			return
		}
		inv.SetTrailer(openbindings.Metadata{"x-ob-governing-media": {matched.canonical}})
		streamSSE(bctx, resp, args, site, inv)
		return
	}

	defer func() { _ = resp.Body.Close() }()

	// The unary body is one delivery unit: consumer-bounded via
	// BindingInvocationArgs.MaxDeliveryUnitBytes (default 10 MiB). The +1
	// sentinel distinguishes an at-limit response from an over-limit one.
	maxUnit := args.DeliveryUnitLimit()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxUnit+1))
	if err != nil {
		if bctx.Err() != nil {
			return
		}
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: err.Error()})
		return
	}
	if int64(len(respBody)) > maxUnit {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("response exceeds %d byte limit", maxUnit),
		})
		return
	}

	// Classify, then decode — both through the consultation seam
	// (per-invocation hook → invoker-level hook → the format builtins
	// below). The binding specification's defaults (OAPI-P-07/P-08),
	// content-independent throughout: classify = success iff status ∈ 2xx
	// (declared `responses` never change classification — they enrich
	// failure details); decode = the response's Content-Type HEADER decides
	// the lane (wire framing, not payload sniffing): JSON for
	// application/json and +json suffixes, text otherwise, absent /
	// unparseable header → text.
	status := resp.StatusCode
	raw := openbindings.RawResult{
		Status: &status,
		Body:   respBody,
		Meta:   headerMetadata(resp.Header),
	}

	ok, cerr := args.Hooks.Classify(site, raw, BuiltinClassify)
	if cerr != nil {
		inv.FireError(openbindings.AsInvocationError(cerr))
		return
	}
	if !ok {
		// The format's NATIVE failure: hooks change the verdict, never
		// the error vocabulary. The raw body rides Details for callers.
		ierr := openbindings.HTTPError(resp.StatusCode, resp.Status)
		if d, okd := ierr.Details.(map[string]any); okd && len(respBody) > 0 {
			d["body"] = string(respBody)
		}
		inv.FireError(ierr)
		return
	}
	// An empty successful response carries absence, not an invented JSON
	// null. It completes without consulting response media or decode.
	if len(respBody) == 0 {
		inv.SetTrailer(decodeClassifyTrailer(args.Hooks, "not-consulted/empty"))
		inv.CloseOutput()
		return
	}

	matched, mediaErr := governingResponseMedia(responseDecl, actualContentType)
	if mediaErr != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeProtocol, Message: mediaErr.Error()})
		return
	}

	output, derr := args.Hooks.DecodeOutput(site, raw, decodeByContentType(actualContentType))
	if derr != nil {
		inv.FireError(openbindings.AsInvocationError(derr))
		return
	}

	// Success provenance stamps (conventions record): decode provenance is
	// header/content-type when the builtin (the Content-Type lane) decided,
	// hook when overridden; classify is always assumption/2xx unless a hook
	// widened it.
	trailer := decodeClassifyTrailer(args.Hooks, "header/content-type")
	trailer["x-ob-governing-media"] = []string{matched.canonical}
	inv.SetTrailer(trailer)
	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// decodeClassifyTrailer builds the x-ob-decode/x-ob-classify success
// provenance stamps (the conventions record, spec/binding-specs/README.md) for
// an HTTP-lane invoker, given the axis's builtin provenance token. A hook
// decision on either axis stamps "hook".
func decodeClassifyTrailer(hooks *openbindings.InvokeHooks, builtinDecode string) openbindings.Metadata {
	decode, classify := builtinDecode, "assumption/2xx"
	if hooks.DecodeDecidedBy() == "hook" {
		decode = "hook"
	}
	if hooks.ClassifyDecidedBy() == "hook" {
		classify = "hook"
	}
	return openbindings.Metadata{
		"x-ob-decode":   {decode},
		"x-ob-classify": {classify},
	}
}

// BuiltinClassify is the openapi builtin result classifier (OAPI-P-08):
// success iff the final HTTP status is 2xx (declared responses refine
// failure DETAILS only, never classification).
func BuiltinClassify(_ openbindings.InvokeSite, raw openbindings.RawResult) (bool, error) {
	return raw.Status != nil && *raw.Status >= 200 && *raw.Status < 300, nil
}

// decodeByContentType returns the builtin decoder implementing the header
// rule (OAPI-P-07): strict JSON for application/json and +json suffixes (a
// declared-JSON body that fails to parse is a lying server — a loud
// ErrCodeResponseError, never a silent string); the text lane otherwise —
// bytes become a string per the header's charset parameter, defaulting to
// UTF-8, with invalid sequences a loud decode error. An empty body (204
// included) yields null.
func decodeByContentType(contentType string) openbindings.OutputDecoder {
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
					Message: fmt.Sprintf("response declares %q but the body is not valid JSON: %v", contentType, err),
				}
			}
			return parsed, nil
		}
		return decodeTextLane(contentType, raw.Body)
	}
}

// decodeTextLane decodes response bytes as text per the Content-Type
// header's charset parameter, defaulting to UTF-8 (OAPI-P-07). Invalid
// sequences, and charsets this implementation cannot decode, are loud
// decode errors — a consumer needing another charset overrides at the
// decode configuration point.
func decodeTextLane(contentType string, body []byte) (any, error) {
	charset := "utf-8"
	if contentType != "" {
		if _, params, err := mime.ParseMediaType(contentType); err == nil {
			if cs := params["charset"]; cs != "" {
				charset = cs
			}
		}
	}
	switch strings.ToLower(charset) {
	case "utf-8", "utf8":
		if !utf8.Valid(body) {
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeResponseError,
				Message: "response body is not valid UTF-8 (the declared/default charset)",
			}
		}
		return string(body), nil
	case "us-ascii", "ascii":
		for i := 0; i < len(body); i++ {
			if body[i] >= 0x80 {
				return nil, &openbindings.InvocationError{
					Code:    openbindings.ErrCodeResponseError,
					Message: fmt.Sprintf("response body byte %d is not valid US-ASCII (the declared charset)", i),
				}
			}
		}
		return string(body), nil
	case "iso-8859-1", "iso8859-1", "latin-1", "latin1":
		runes := make([]rune, len(body))
		for i, b := range body {
			runes[i] = rune(b)
		}
		return string(runes), nil
	default:
		return nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("response declares charset %q, which this implementation cannot decode; override at the decode configuration point", charset),
		}
	}
}

// isJSONContentType reports whether a Content-Type header declares a JSON
// body: application/json or any +json structured-suffix type. Absent or
// unparseable headers are NOT JSON (the text lane) — never sniffed.
func isJSONContentType(contentType string) bool {
	return isJSONMediaType(normalizeMediaType(contentType))
}

// siteFor completes the core-stamped site with the format-known Target
// (the resolved base URL) before consultation; a nil args.Site (direct
// format-package call) gets a minimal unstamped-equivalent site whose
// Builtin* dispatch stays loud.
func siteFor(args *openbindings.BindingInvocationArgs, baseURL string) openbindings.InvokeSite {
	var site openbindings.InvokeSite
	if args.Site != nil {
		site = *args.Site
	} else {
		site.BindingSpec = args.Source.BindingSpec
		site.Ref = args.Ref
	}
	if site.Target == "" {
		site.Target = baseURL
	}
	return site
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
			// R2.a ruling: every requirement carries the securitySchemes key
			// that named it — distinguishes two ANDed schemes of the same
			// type within this alternative and keys the scheme-scoped
			// 'apiKeys' credential lookup.
			req.Name = schemeName
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
		Target:       baseURL,
		Alternatives: alternatives,
	}
	// Already satisfiable from the supplied context: no challenge needed.
	if openbindings.ContextSatisfies(bindCtx, details) {
		return nil
	}
	return details
}

// schemeToRequirement maps an OpenAPI security scheme to a context
// requirement, carrying the family-specific fields a resolver needs to act
// without out-of-band knowledge (notably oauth2 flow endpoints). Per the
// R2.c ruling, a scheme family the SDK cannot itself resolve is no longer
// dropped: it is surfaced with a type derived from the artifact ("auth.http."
// + the HTTP scheme for an unmapped "http" scheme, e.g. "auth.http.digest";
// "auth." + the artifact's own type otherwise, e.g. "auth.mutualTLS") so the
// alternative stays discoverable to a runtime with a resolver for it, rather
// than silently vanishing into an unauthenticated dispatch. Every switch arm
// now returns true; the bool is kept for signature stability (and as a hook
// for a genuinely inexpressible future case) rather than removed.
func schemeToRequirement(s *openapi3.SecurityScheme, baseURL string) (openbindings.ContextRequirement, bool) {
	switch s.Type {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "basic":
			return openbindings.ContextRequirement{Type: "auth.basic"}, true
		case "bearer":
			return openbindings.ContextRequirement{Type: "auth.bearer"}, true
		default:
			// digest, negotiate, etc.: not a family this SDK resolves
			// itself, but SURFACED (R2.c ruling), not dropped. A missing
			// scheme value degrades to the bare family, never a trailing
			// dot (TS parity).
			return openbindings.ContextRequirement{
				Type:        httpRequirementType(s.Scheme),
				Description: s.Description,
			}, true
		}
	case "apiKey":
		return openbindings.ContextRequirement{Type: "auth.apiKey"}, true
	case "oauth2":
		return oauth2Requirement(s, baseURL), true
	case "openIdConnect":
		// OpenID Connect resolves to an OAuth2 access token; the discovery URL
		// lets a resolver fetch the authorize/token endpoints. No flow is
		// selected here (openIdConnect has no `flows` object), so this
		// requirement carries no grantType (R2.b ruling).
		req := openbindings.ContextRequirement{Type: "auth.oauth2"}
		if s.OpenIdConnectUrl != "" {
			req.Extra = map[string]any{"openIdConnectUrl": absolutizeURL(s.OpenIdConnectUrl, baseURL)}
		}
		return req, true
	default:
		// Any other artifact type this SDK doesn't itself resolve (e.g.
		// "mutualTLS"): surfaced verbatim (R2.c ruling) rather than dropped.
		return openbindings.ContextRequirement{
			Type:        "auth." + s.Type,
			Description: s.Description,
		}, true
	}
}

// oauth2Requirement builds an auth.oauth2 requirement carrying the SELECTED
// flow's grantType (R2.b ruling) alongside its authorize/token URLs and
// scopes, under the binding-invoker contract's convention field names
// (grantType, authorizeUrl, tokenUrl, scopes). Fixed priority — surfaced,
// never changed by this ruling: authorizationCode (the only interactive,
// PKCE-capable flow) > implicit > password > clientCredentials, the last two
// selected only when they carry a tokenUrl (OpenAPI 3.x defines
// authorizationUrl only for authorizationCode/implicit). Relative URLs are
// resolved against the base URL.
// httpRequirementType derives the surfaced type for an http scheme this SDK
// does not itself resolve: "auth.http.<scheme>" (lowercased), degrading to
// the bare "auth.http" when the artifact omits the scheme value (TS parity —
// never a trailing dot).
func httpRequirementType(scheme string) string {
	if scheme == "" {
		return "auth.http"
	}
	return "auth.http." + strings.ToLower(scheme)
}

func oauth2Requirement(s *openapi3.SecurityScheme, baseURL string) openbindings.ContextRequirement {
	req := openbindings.ContextRequirement{Type: "auth.oauth2"}
	if s.Flows == nil {
		return req
	}
	var flow *openapi3.OAuthFlow
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
	req.Extra = extra
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

// validRefMethods are the OAS's HTTP method keys, lowercase exactly as the
// artifact spells them (OAPI-D-03). Acceptance never case-folds.
var validRefMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// parseRef parses a binding ref per OAPI-D-03: a JSON Pointer of the exact
// form `#/paths/<escaped-path>/<method>` addressing an operation object. The
// path segment carries RFC 6901 escaping ("/" → "~1", "~" → "~0"), and the
// method is lowercase exactly as the artifact spells it — an uppercase
// method is non-conformant and refused, never case-folded.
func parseRef(ref string) (path string, method string, err error) {
	const prefix = "#/paths/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", fmt.Errorf("ref %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method> (OAPI-D-03)", ref)
	}
	parts := strings.Split(ref[len(prefix):], "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("ref %q must be a JSON Pointer of the form #/paths/<escaped-path>/<method>: the path segment carries RFC 6901 escaping (\"/\" → \"~1\") (OAPI-D-03)", ref)
	}
	escapedPath, method := parts[0], parts[1]
	if !validRefMethods[method] {
		if validRefMethods[strings.ToLower(method)] {
			return "", "", fmt.Errorf("ref %q: method %q must be lowercase exactly as the artifact spells it (OAPI-D-03)", ref, method)
		}
		return "", "", fmt.Errorf("invalid HTTP method %q in ref", method)
	}

	// RFC 6901 unescaping, in order: ~1 first, then ~0.
	path = strings.ReplaceAll(escapedPath, "~1", "/")
	path = strings.ReplaceAll(path, "~0", "~")
	return path, method, nil
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

func requestWillEmitBody(params openapi3.Parameters, input map[string]any, op *openapi3.Operation) bool {
	if !hasRequestBody(op) {
		return false
	}
	if op.RequestBody.Value.Required {
		return true
	}
	parameterNames := map[string]bool{}
	for _, parameter := range params {
		if parameter != nil && parameter.Value != nil {
			parameterNames[parameter.Value.Name] = true
		}
	}
	for name := range input {
		if !parameterNames[name] {
			return true
		}
	}
	return false
}

// encodePathValue percent-encodes one path parameter value with exactly
// JavaScript's encodeURIComponent byte set (every byte except ALPHA / DIGIT /
// "-" "_" "." "!" "~" "*" "'" "(" ")" is %XX-escaped, UTF-8 bytewise), so the
// Go and TS invokers substitute byte-identical URL path segments.
// url.PathEscape is NOT equivalent: it passes sub-delims like "$&+,:;=@"
// through unescaped.
func encodePathValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xF])
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Credentials and channel assembly (§9.6: OAPI-P-09 wire application,
// OAPI-P-10 channel assembly)
// ---------------------------------------------------------------------------

// credentialPlacement is one credential's wire application: which channel
// it rides (header, query, or cookie) under which name.
type credentialPlacement struct {
	channel string
	name    string
	value   string
}

// credentialPlacements derives the credential wire applications for an
// operation from the artifact's security declarations (read at invocation
// time, never extracted into the OBI) and the supplied context: an apiKey
// scheme's credential rides its declared in/name; http basic and bearer,
// oauth2, and openIdConnect ride the Authorization header. When the document
// declares no security schemes, well-known context credentials fall back to
// the Authorization header. Placements are computed BEFORE dispatch so the
// OAPI-P-10 collision refusal can run pre-dispatch; duplicate channel+name
// placements collapse to the first.
func credentialPlacements(doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any) []credentialPlacement {
	if len(bindCtx) == 0 {
		return nil
	}

	var placements []credentialPlacement
	seen := map[string]bool{}
	add := func(channel, name, value string) {
		key := channel + "\x00" + name
		if channel == "header" {
			key = channel + "\x00" + http.CanonicalHeaderKey(name)
		}
		if seen[key] {
			return
		}
		seen[key] = true
		placements = append(placements, credentialPlacement{channel: channel, name: name, value: value})
	}

	schemes := resolveSecuritySchemes(doc, op)
	for _, named := range schemes {
		if named.Scheme.Value == nil {
			continue
		}
		s := named.Scheme.Value
		switch s.Type {
		case "apiKey":
			val := openbindings.ContextAPIKeyFor(bindCtx, named.Name)
			if val == "" {
				continue
			}
			switch s.In {
			case "header", "query", "cookie":
				add(s.In, s.Name, val)
			}
		case "http":
			switch strings.ToLower(s.Scheme) {
			case "bearer":
				if token := openbindings.ContextBearerToken(bindCtx); token != "" {
					add("header", "Authorization", "Bearer "+token)
				}
			case "basic":
				if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
					add("header", "Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u+":"+p)))
				}
			}
		case "oauth2", "openIdConnect":
			token := openbindings.ContextString(bindCtx, "accessToken")
			if token == "" {
				token = openbindings.ContextBearerToken(bindCtx)
			}
			if token != "" {
				add("header", "Authorization", "Bearer "+token)
			}
		}
	}

	if len(placements) == 0 {
		// No scheme applied a credential (typically no securitySchemes
		// declared): well-known context credentials fall back to the
		// Authorization header.
		if token := openbindings.ContextBearerToken(bindCtx); token != "" {
			add("header", "Authorization", "Bearer "+token)
		} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
			add("header", "Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(u+":"+p)))
		} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
			add("header", "Authorization", "ApiKey "+key)
		}
	}
	return placements
}

// checkCredentialCollisions enforces the OAPI-P-10 refusal: a name collision
// between a credential and a caller-populated declared parameter on the same
// channel is refused before dispatch — loud, never a silent overwrite in
// either direction. Header names compare case-insensitively.
func checkCredentialCollisions(placements []credentialPlacement, params openapi3.Parameters, populated map[string]map[string]bool) error {
	declared := map[string]map[string]bool{"header": {}, "query": {}, "cookie": {}, "path": {}}
	for _, ref := range params {
		if ref == nil || ref.Value == nil {
			continue
		}
		name := ref.Value.Name
		if ref.Value.In == openapi3.ParameterInHeader {
			name = http.CanonicalHeaderKey(name)
		}
		declared[ref.Value.In][name] = true
	}
	ownedHeaders := map[string]bool{"Host": true, "Content-Length": true, "Content-Type": true, "Accept": true}
	seen := map[string]bool{}
	for _, pl := range placements {
		name := pl.name
		if pl.channel == "header" {
			name = http.CanonicalHeaderKey(name)
			if ownedHeaders[name] {
				return fmt.Errorf("credential %q collides with processor-owned request field %s (OAPI-P-10)", pl.name, name)
			}
		}
		if declared[pl.channel][name] || populated[pl.channel][name] {
			return fmt.Errorf("credential %q collides with an effective %s parameter of the same name (OAPI-P-10: refused before dispatch, never a silent overwrite in either direction)", pl.name, pl.channel)
		}
		key := pl.channel + "\x00" + name
		if seen[key] {
			return fmt.Errorf("two credentials collide at %s %q (OAPI-P-10)", pl.channel, pl.name)
		}
		seen[key] = true
	}
	return nil
}

// namedSecurityScheme pairs a resolved OpenAPI security scheme with the
// securitySchemes map key it was resolved from — the same key requiredContext
// stamps onto the ContextRequirement's Name (R2.a ruling), needed here so
// credential application can look up a NAMED apiKey scheme's key via
// ContextAPIKeyFor without re-deriving the name.
type namedSecurityScheme struct {
	Name   string
	Scheme *openapi3.SecuritySchemeRef
}

// resolveSecuritySchemes returns the security schemes applicable to an
// operation, each paired with its securitySchemes key. Operation-level
// security overrides top-level; falls back to top-level if not set. Scheme
// names within each requirement iterate sorted so placement order is
// deterministic.
func resolveSecuritySchemes(doc *openapi3.T, op *openapi3.Operation) []namedSecurityScheme {
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

	var result []namedSecurityScheme
	seen := map[string]bool{}
	for _, req := range *requirements {
		names := make([]string, 0, len(req))
		for schemeName := range req {
			names = append(names, schemeName)
		}
		sort.Strings(names)
		for _, schemeName := range names {
			if seen[schemeName] {
				continue
			}
			seen[schemeName] = true
			if ref, ok := doc.Components.SecuritySchemes[schemeName]; ok {
				result = append(result, namedSecurityScheme{Name: schemeName, Scheme: ref})
			}
		}
	}
	return result
}
