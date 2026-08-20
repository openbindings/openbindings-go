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

	"github.com/openbindings/openbindings-go/invoke"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// maxResponseBytes caps the HTTP error body drained on the streaming dispatch
// path (streaming.go). Deliberately fixed: this is an implementation resource
// guard on the error path, not a delivery unit —
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
	if ref == "" {
		return "", "", fmt.Errorf("empty Connect ref")
	}
	idx := strings.Index(ref, "/")
	if idx < 0 || idx != strings.LastIndex(ref, "/") || idx == 0 || idx == len(ref)-1 {
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
func resolveMethod(disc *discovery, svcName, methodName string) (protoreflect.MethodDescriptor, *invoke.InvocationError) {
	var svcDesc protoreflect.ServiceDescriptor
	for _, svc := range disc.services {
		if string(svc.FullName()) == svcName {
			svcDesc = svc
			break
		}
	}
	if svcDesc == nil {
		return nil, &invoke.InvocationError{
			Code: invoke.ErrCodeRefNotFound,
		}
	}
	m := svcDesc.Methods().ByName(protoreflect.Name(methodName))
	if m == nil {
		return nil, &invoke.InvocationError{
			Code: invoke.ErrCodeRefNotFound,
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
func buildSchemaModeBody(mi *methodInfo, input any) ([]byte, *invoke.InvocationError) {
	msg := dynamicpb.NewMessage(mi.method.Input())
	if input != nil {
		jsonBytes, err := json.Marshal(input)
		if err != nil {
			return nil, &invoke.InvocationError{
				Code: invoke.ErrCodeValidationFailed,
			}
		}
		if err := protojson.Unmarshal(jsonBytes, msg); err != nil {
			return nil, &invoke.InvocationError{
				Code: invoke.ErrCodeValidationFailed,
			}
		}
	}
	body, err := protojson.Marshal(msg)
	if err != nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeValidationFailed}
	}
	return body, nil
}

// buildDescriptorlessBody serializes the input for descriptorless mode per
// §9.3 (CONN-P-03): the input value — ANY JSON value — is serialized
// verbatim as the unary request body, and an ABSENT input value sends {},
// the empty message's canonical form. An explicit JSON null is a value,
// not absence, and rides as `null`. No field semantics exist in this mode,
// and no unknown-field posture is implied.
func buildDescriptorlessBody(input any, gotInput bool) ([]byte, *invoke.InvocationError) {
	if !gotInput {
		return []byte("{}"), nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeValidationFailed}
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
func decodeSchemaModeOutput(mi *methodInfo, payload []byte) (any, *invoke.InvocationError) {
	msg := dynamicpb.NewMessage(mi.method.Output())
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payload, msg); err != nil {
		return nil, &invoke.InvocationError{
			Code: invoke.ErrCodeResponseError,
		}
	}
	rendered, err := protojson.Marshal(msg)
	if err != nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeResponseError}
	}
	var out any
	if err := json.Unmarshal(rendered, &out); err != nil {
		return nil, &invoke.InvocationError{Code: invoke.ErrCodeResponseError}
	}
	return out, nil
}

// isJSONContentType reports whether a unary response Content-Type is the
// Connect JSON codec's application/json (media-type parameters tolerated).
func isJSONContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return ct == "application/json" || strings.HasPrefix(ct, "application/json;")
}

func connectHTTPError(resp *http.Response, _ []byte, _ bool) *invoke.InvocationError {
	return invoke.HTTPError(resp.StatusCode, resp.Status)
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
func (e *Invoker) runUnary(ctx context.Context, inv invoke.BindingHandle[any, any], reqURL string, body []byte, headers map[string]string, mi *methodInfo, maxUnit int64) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeExecutionFailed})
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
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeConnectFailed})
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
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeResponseError})
		return
	}
	if int64(len(respBody)) > maxUnit {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeResponseError,
		})
		return
	}

	if resp.StatusCode != http.StatusOK {
		// CONN-P-06: success iff the final status is 200; anything else —
		// a Connect error, a proxy status, a 2xx that is not 200 — is a
		// failure outcome.
		inv.FireError(connectHTTPError(resp, respBody, false))
		return
	}

	// A 200 whose content type is not the JSON codec's application/json is
	// a loud protocol error, never a passed-through value (§9.3; unary
	// framing is the codec's plain JSON body, CONN-P-05).
	if ct := resp.Header.Get("Content-Type"); !isJSONContentType(ct) {
		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeResponseError,
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
		// body parsed as JSON, verbatim; an empty response cannot represent a
		// JSON value and is a protocol error rather than an invented null; a
		// body that fails to parse as JSON is a loud protocol-error
		// failure outcome, never a string.
		if len(respBody) == 0 {
			inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeResponseError})
			return
		}
		if err := json.Unmarshal(respBody, &output); err != nil {
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeResponseError,
			})
			return
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

	token := invoke.ContextBearerToken(bindCtx)
	user, password, hasBasic := invoke.ContextBasicAuth(bindCtx)
	if token != "" && hasBasic {
		return nil, fmt.Errorf("bearer and basic credentials both target Authorization")
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if hasBasic {
		encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
		headers["Authorization"] = "Basic " + encoded
	}

	if apiKeyPresent(bindCtx) {
		return nil, &unplacedCredentialError{message: "an API key is present in context but openbindings.connect@1 defines no standard header for one (unlike the gRPC family's fixed authorization metadata key, §9.6): name the header the key rides via context.headers — an inexpressible credential is surfaced, never placed on Authorization and never silently skipped (openbindings.connect@1 §9.6 / CONN-P-07)"}
	}

	reserved := map[string]bool{"host": true, "content-length": true, "content-type": true}
	seen := map[string]string{}
	for k, v := range invoke.ContextHeaders(bindCtx) {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "connect-") || reserved[lower] {
			return nil, fmt.Errorf("metadata field %q is protocol-reserved or processor-owned", k)
		}
		if prior, ok := seen[lower]; ok {
			return nil, fmt.Errorf("metadata fields %q and %q have the same case-insensitive destination", prior, k)
		}
		if lower == "authorization" && headers["Authorization"] != "" {
			return nil, fmt.Errorf("configured Authorization header collides with structured authorization credential")
		}
		seen[lower] = k
		headers[k] = v
	}
	if cookies := invoke.ContextCookies(bindCtx); len(cookies) > 0 {
		if _, ok := seen["cookie"]; ok {
			return nil, fmt.Errorf("configured Cookie header collides with structured cookie credentials")
		}
		pairs := make([]string, 0, len(cookies))
		for k, v := range cookies {
			pairs = append(pairs, k+"="+v)
		}
		sort.Strings(pairs)
		headers["Cookie"] = strings.Join(pairs, "; ")
	}

	return headers, nil
}

type unplacedCredentialError struct{ message string }

func (e *unplacedCredentialError) Error() string { return e.message }

// apiKeyPresent reports whether the binding context carries a well-known API
// key credential — the flat `apiKey` field or any non-empty scheme-scoped
// `apiKeys[name]` entry. Connect derives no API-key security requirement
// (CONN-P-07), so such a field appears only when a consumer set it directly;
// under §9.6 it is inexpressible without a consumer-named header and must be
// surfaced rather than placed on an invented `Authorization: ApiKey`.
func apiKeyPresent(bindCtx map[string]any) bool {
	if invoke.ContextAPIKey(bindCtx) != "" {
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
