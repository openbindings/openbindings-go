package connect

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// maxResponseBytes caps the HTTP ERROR body captured into failure details on
// the streaming dispatch path (streaming.go). Deliberately fixed: a
// diagnostics capture on the error path, not a delivery unit —
// BindingInvocationArgs.MaxDeliveryUnitBytes does not apply here. The
// delivery-unit bounds (unary body, per-envelope payload) are
// consumer-configured via that field; resource policy stays with the
// consumer and the implementation, never openbindings.connect@1 (§2).
const maxResponseBytes int64 = 10 * 1024 * 1024

// methodInfo holds a resolved method descriptor for schema-mode input and
// output correspondence. A nil *methodInfo marks descriptorless mode
// (openbindings.connect@1 CONN-P-01).
type methodInfo struct {
	method protoreflect.MethodDescriptor
}

// parseRef splits a binding ref per CONN-D-03 (§7), which takes exactly
// openbindings.grpc@1 §7's grammar (GRPC-D-03), incorporated:
// <fully-qualified-service>/<method> — the service's package-qualified
// name, or its bare name when its file declares no package, one '/', and
// the unqualified RPC name. Matching downstream is byte-exact in schema
// mode; in descriptorless mode the segments ride verbatim into the
// request URL.
func parseRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty Connect ref")
	}
	idx := strings.LastIndex(ref, "/")
	if idx < 0 || idx == 0 || idx == len(ref)-1 {
		return "", "", fmt.Errorf("Connect ref %q must be <fully-qualified-service>/<method> (openbindings.connect@1 CONN-D-03)", ref)
	}
	return ref[:idx], ref[idx+1:], nil
}

// validateBaseURL checks a base URL against §4's grammar (CONN-D-02): an
// absolute http/https URI naming the service's base URL — scheme, host,
// optional port (ordinary HTTP defaults), and an optional path prefix
// WITHOUT a trailing '/'. Query, fragment, and userinfo components are not
// part of a base URL. Transport security follows the scheme (schemes are
// TLS-unambiguous here, so this family has no transport configuration
// point).
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf(
			"connect base URL %q does not parse (openbindings.connect@1 CONN-D-02): %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf(
			"connect base URL %q must be an absolute http/https URI (openbindings.connect@1 CONN-D-02)", raw)
	}
	if u.Host == "" {
		return fmt.Errorf(
			"connect base URL %q names no host (openbindings.connect@1 CONN-D-02)", raw)
	}
	if u.User != nil {
		return fmt.Errorf(
			"connect base URL %q carries a userinfo component, which is not part of a base URL (openbindings.connect@1 CONN-D-02)", raw)
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf(
			"connect base URL %q carries a query component, which is not part of a base URL (openbindings.connect@1 CONN-D-02)", raw)
	}
	if u.Fragment != "" {
		return fmt.Errorf(
			"connect base URL %q carries a fragment component, which is not part of a base URL (openbindings.connect@1 CONN-D-02)", raw)
	}
	if strings.HasSuffix(u.Path, "/") {
		return fmt.Errorf(
			"connect base URL %q carries a trailing '/': a base URL's optional path prefix has no trailing slash (openbindings.connect@1 CONN-D-02)", raw)
	}
	return nil
}

// connectURL builds the request URL per §4: the Connect protocol's
// routing, incorporated — the base URL STRING-CONCATENATED with
// /<fully-qualified-service>/<method> from the binding's ref.
// Concatenation, not RFC 3986 resolution, so a path prefix is preserved;
// CONN-D-02 guarantees the base carries no trailing '/'.
func connectURL(baseURL, svcName, methodName string) string {
	return baseURL + "/" + svcName + "/" + methodName
}

// resolveMethod resolves <fully-qualified-service>/<method> against a
// discovered embedded schema (schema mode). Matching is byte-exact, no
// case folding (CONN-D-03, incorporating GRPC-D-03's grammar); a ref
// matching no service or method makes the binding unresolvable.
func resolveMethod(disc *discovery, svcName, methodName string) (protoreflect.MethodDescriptor, *openbindings.InvocationError) {
	var svcDesc protoreflect.ServiceDescriptor
	for _, svc := range disc.services {
		if string(svc.FullName()) == svcName {
			svcDesc = svc
			break
		}
	}
	if svcDesc == nil {
		return nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("service %q not found in embedded schema", svcName),
		}
	}
	m := svcDesc.Methods().ByName(protoreflect.Name(methodName))
	if m == nil {
		return nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeRefNotFound,
			Message: fmt.Sprintf("method %q not found in service %q", methodName, svcName),
		}
	}
	return m, nil
}

// buildSchemaModeBody marshals one caller-facing input value into the
// request message body per §9.2 (CONN-P-02), which incorporates
// openbindings.grpc@1 §9.1 (GRPC-P-03): the accepted shape is the request
// type's CANONICAL JSON form — an object for ordinary messages, the
// mapping's defined form where it differs (a string for a
// google.protobuf.Duration-typed request, the wrapped value for wrapper
// types, and so on). Unmarshalling follows the mapping's own rules,
// including its default posture on unknown fields: they are refused
// loudly, never silently discarded — and every refusal fires before
// dispatch. A nil input is the absent input value and marshals as the
// empty request message.
func buildSchemaModeBody(mi *methodInfo, input any) ([]byte, *openbindings.InvocationError) {
	msg := dynamicpb.NewMessage(mi.method.Input())
	if input != nil {
		jsonBytes, err := json.Marshal(input)
		if err != nil {
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeValidationFailed,
				Message: fmt.Sprintf("marshal input: %v", err),
			}
		}
		if err := protojson.Unmarshal(jsonBytes, msg); err != nil {
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeValidationFailed,
				Message: fmt.Sprintf("input is not the canonical JSON form of %s: %v", mi.method.Input().FullName(), err),
			}
		}
	}
	body, err := protojson.Marshal(msg)
	if err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
	}
	return body, nil
}

// buildDescriptorlessBody serializes the input for descriptorless mode per
// §9.3 (CONN-P-03): the input value — ANY JSON value — is serialized
// verbatim as the unary request body, and an ABSENT input value sends {},
// the empty message's canonical form. An explicit JSON null is a value,
// not absence, and rides as `null`. No field semantics exist in this mode,
// and no unknown-field posture is implied.
func buildDescriptorlessBody(input any, gotInput bool) ([]byte, *openbindings.InvocationError) {
	if !gotInput {
		return []byte("{}"), nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
	}
	return body, nil
}

// decodeSchemaModeOutput renders one response payload per §9.2
// (CONN-P-02, incorporating GRPC-P-05): the output value is the response
// message rendered by the canonical JSON mapping. A payload that fails to
// unmarshal against the response descriptor is a failure outcome — loud,
// never a silently passed-through value.
//
// Unknown members in a RESPONSE are tolerated and dropped
// (DiscardUnknown), matching the incorporated schema layer's wire
// behavior: openbindings.grpc@1's binary decode preserves-and-ignores
// unknown response fields, and §9.2's loud unknown-field posture is the
// INPUT rule. The pin stays authoritative for interpretation (§6), so a
// drifted-but-compatible server keeps answering.
func decodeSchemaModeOutput(mi *methodInfo, payload []byte) (any, *openbindings.InvocationError) {
	msg := dynamicpb.NewMessage(mi.method.Output())
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(payload, msg); err != nil {
		return nil, &openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("response is not the canonical JSON form of %s: %v (openbindings.connect@1 CONN-P-02)", mi.method.Output().FullName(), err),
		}
	}
	rendered, err := protojson.Marshal(msg)
	if err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: fmt.Sprintf("marshal response: %v", err)}
	}
	var out any
	if err := json.Unmarshal(rendered, &out); err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: fmt.Sprintf("parse response JSON: %v", err)}
	}
	return out, nil
}

// isJSONContentType reports whether a unary response Content-Type is the
// Connect JSON codec's application/json (media-type parameters tolerated).
func isJSONContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return ct == "application/json" || strings.HasPrefix(ct, "application/json;")
}

// connectCodeToErrCode maps a Connect protocol error code to the standard
// invocation error codes. Failure outcomes have no representation in the
// specification (§9.5); this vocabulary is this consumer surface's own.
func connectCodeToErrCode(code string) string {
	switch code {
	case "unauthenticated":
		return openbindings.ErrCodeAuthRequired
	case "permission_denied":
		return openbindings.ErrCodePermissionDenied
	case "unavailable", "resource_exhausted":
		// The server answered but refused the request as retryable — not a
		// transport failure that never reached a server (ErrCodeConnectFailed).
		// Same family mapping as gRPC; the binding-invoker contract pins it.
		return openbindings.ErrCodeUnavailable
	case "deadline_exceeded":
		return openbindings.ErrCodeTimeout
	case "canceled":
		return openbindings.ErrCodeCancelled
	default:
		return openbindings.ErrCodeExecutionFailed
	}
}

// applyConnectError refines an HTTP-status-derived terminal error with the
// Connect error payload (a JSON object with `code` and `message` fields)
// when the body parses as one.
func applyConnectError(ierr *openbindings.InvocationError, body []byte) {
	if len(body) == 0 {
		return
	}
	var connectErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &connectErr) != nil {
		return
	}
	if connectErr.Code != "" {
		ierr.Code = connectCodeToErrCode(connectErr.Code)
	}
	if connectErr.Message != "" {
		ierr.Message = connectErr.Message
	}
}

// headerMetadata clones HTTP headers into invocation Metadata.
func headerMetadata(h http.Header) openbindings.Metadata {
	md := make(openbindings.Metadata, len(h))
	for k, vs := range h {
		md[k] = append([]string(nil), vs...)
	}
	return md
}

// unaryTrailerPrefix marks trailing metadata in a Connect unary response:
// the protocol carries unary trailing metadata as "Trailer-"-prefixed
// response headers — unary Connect uses no HTTP trailers (§9.4).
const unaryTrailerPrefix = "Trailer-"

// splitUnaryMetadata separates a unary response's leading headers from its
// trailing metadata: "Trailer-"-prefixed headers (the Connect unary
// trailer convention, prefix stripped) plus any HTTP/1.1 trailers a
// protocol-violating server may have sent (resp.Trailer is populated once
// the body has been fully read). Metadata has no representation in the
// output VALUE (§9.4, a declared exclusion); this handle-level surfacing
// is out of band, the implementation's own concern.
func splitUnaryMetadata(resp *http.Response) (header, trailer openbindings.Metadata) {
	header = make(openbindings.Metadata, len(resp.Header))
	trailer = openbindings.Metadata{}
	for k, vs := range resp.Header {
		if strings.HasPrefix(k, unaryTrailerPrefix) && len(k) > len(unaryTrailerPrefix) {
			trailer[k[len(unaryTrailerPrefix):]] = append([]string(nil), vs...)
			continue
		}
		header[k] = append([]string(nil), vs...)
	}
	for k, vs := range resp.Trailer {
		trailer[k] = append(trailer[k], vs...)
	}
	return header, trailer
}

// runUnary sends one unary dispatch — a POST with a plain JSON body
// (CONN-P-05; the protocol's GET lane for side-effect-free methods is
// EXCLUDED from revision 1, §2: every dispatch under this identifier is a
// POST) — and drives the handle: leading headers, one output, trailing
// metadata, then CloseOutput — or FireError with the mapped terminal
// error.
//
// Classification is protocol-native and not a configuration point
// (CONN-P-06): a unary invocation succeeds IFF the final response status,
// after any redirects, is 200 — the protocol makes every unary error
// non-200, so this is Connect's own rule, not a 2xx heuristic.
func (e *Invoker) runUnary(ctx context.Context, inv openbindings.BindingHandle[any, any], reqURL string, body []byte, headers map[string]string, mi *methodInfo, maxUnit int64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: err.Error()})
		return
	}

	// Connect protocol headers. Connect-Protocol-Version: 1 rides EVERY
	// request — the protocol makes sending it a SHOULD, and the
	// specification fixes it (CONN-P-05).
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

	// Credentials and caller-supplied entries ride ordinary HTTP header
	// fields (§9.6, CONN-P-07).
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // cancelled; the handle is already terminal
		}
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: err.Error()})
		return
	}
	defer resp.Body.Close()

	// The response-size cap is implementation policy (§2), not a spec rule:
	// the unary body is one delivery unit, consumer-bounded via
	// BindingInvocationArgs.MaxDeliveryUnitBytes (default 10 MiB).
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxUnit+1))
	if err != nil {
		if ctx.Err() != nil {
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

	// Leading metadata precedes the first emit; trailing metadata precedes
	// CloseOutput/FireError.
	header, trailer := splitUnaryMetadata(resp)
	_ = inv.SetHeader(header)
	if len(trailer) > 0 {
		inv.SetTrailer(trailer)
	}

	if resp.StatusCode != http.StatusOK {
		// CONN-P-06: success iff the final status is 200; anything else —
		// a Connect error, a proxy status, a 2xx that is not 200 — is a
		// failure outcome.
		ierr := openbindings.HTTPError(resp.StatusCode, resp.Status)
		applyConnectError(ierr, respBody)
		inv.FireError(ierr)
		return
	}

	// A 200 whose content type is not the JSON codec's application/json is
	// a loud protocol error, never a passed-through value (§9.3; unary
	// framing is the codec's plain JSON body, CONN-P-05).
	if ct := resp.Header.Get("Content-Type"); !isJSONContentType(ct) {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("connect unary 200 response carries Content-Type %q, not the JSON codec's application/json (openbindings.connect@1 §9.3 / CONN-P-05)", ct),
		})
		return
	}

	var output any
	if mi != nil {
		// Schema mode (CONN-P-02): the output value is the response message
		// rendered by the canonical JSON mapping; a body that fails to
		// unmarshal against the descriptor is a loud failure outcome.
		out, derr := decodeSchemaModeOutput(mi, respBody)
		if derr != nil {
			inv.FireError(derr)
			return
		}
		output = out
	} else {
		// Descriptorless mode (CONN-P-03): the output value is the response
		// body parsed as JSON, verbatim; an empty response body yields
		// null; a body that fails to parse as JSON is a loud protocol-error
		// failure outcome, never a string.
		if len(respBody) > 0 {
			if err := json.Unmarshal(respBody, &output); err != nil {
				inv.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeResponseError,
					Message: fmt.Sprintf("connect unary 200 response body does not parse as JSON: %v (openbindings.connect@1 §9.3)", err),
				})
				return
			}
		}
	}

	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// buildHTTPHeaders constructs request headers from the binding context per
// §9.6 (CONN-P-07): credentials ride as ordinary HTTP header fields.
//
// Bearer and basic credentials share the single `Authorization` header and
// are therefore mutually exclusive — at most one is placed, bearer taking
// precedence over basic. An API KEY rides the header the credential NAMES:
// HTTP defines no single standard header for one (unlike the gRPC family's
// fixed `authorization` metadata key), so the naming is the consumer's,
// carried through context.headers. This is the deliberate connect@1
// divergence from grpc@1 §9.5 — connect does NOT place an API key on
// `Authorization: ApiKey`, and an API key co-exists with a bearer token
// (each rides its own header) rather than being excluded by it.
//
// A well-known apiKey/apiKeys field with no consumer-named header is a
// credential that cannot be expressed as a request header under this family;
// §9.6 requires it to be SURFACED to the consumer, never silently skipped
// and never placed on an invented header. It is surfaced here as a
// pre-dispatch refusal (returned error) — mirroring grpc's unplaceable-key
// surfacing (applyGRPCContext) so the failure class is consistent across the
// family. The honest signal is: name the header via context.headers.
func buildHTTPHeaders(bindCtx map[string]any) (map[string]string, error) {
	headers := map[string]string{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		encoded := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
		headers["Authorization"] = "Basic " + encoded
	}

	if apiKeyPresent(bindCtx) {
		return nil, fmt.Errorf(
			"an API key is present in context but openbindings.connect@1 defines no standard header for one (unlike the gRPC family's fixed authorization metadata key, §9.6): name the header the key rides via context.headers — an inexpressible credential is surfaced, never placed on Authorization and never silently skipped (openbindings.connect@1 §9.6 / CONN-P-07)")
	}

	for k, v := range openbindings.ContextHeaders(bindCtx) {
		headers[k] = v
	}
	if cookies := openbindings.ContextCookies(bindCtx); len(cookies) > 0 {
		pairs := make([]string, 0, len(cookies))
		for k, v := range cookies {
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		headers["Cookie"] = strings.Join(pairs, "; ")
	}

	return headers, nil
}

// apiKeyPresent reports whether the binding context carries a well-known API
// key credential — the flat `apiKey` field or any non-empty scheme-scoped
// `apiKeys[name]` entry. Connect derives no API-key security requirement
// (CONN-P-07), so such a field appears only when a consumer set it directly;
// under §9.6 it is inexpressible without a consumer-named header and must be
// surfaced rather than placed on an invented `Authorization: ApiKey`.
func apiKeyPresent(bindCtx map[string]any) bool {
	if openbindings.ContextAPIKey(bindCtx) != "" {
		return true
	}
	if m, ok := bindCtx["apiKeys"].(map[string]any); ok {
		for _, v := range m {
			if s, ok := v.(string); ok && s != "" {
				return true
			}
		}
	}
	return false
}
