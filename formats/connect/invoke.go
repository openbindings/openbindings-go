package connect

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

const maxResponseBytes int64 = 10 * 1024 * 1024

// methodInfo holds a resolved method descriptor for input/output marshaling.
type methodInfo struct {
	method protoreflect.MethodDescriptor
}

// parseRef extracts the service and method name from a Connect ref.
// Same convention as gRPC: "package.Service/Method".
func parseRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("empty Connect ref")
	}
	idx := strings.LastIndex(ref, "/")
	if idx < 0 || idx == 0 || idx == len(ref)-1 {
		return "", "", fmt.Errorf("Connect ref %q must be in the form package.Service/Method", ref)
	}
	return ref[:idx], ref[idx+1:], nil
}

// resolveMethod parses proto content and finds the method descriptor.
func resolveMethod(ctx context.Context, content any, svcName, methodName string) (*methodInfo, error) {
	disc, err := discoverFromProto(ctx, "", content)
	if err != nil {
		return nil, err
	}
	for _, svc := range disc.services {
		if string(svc.FullName()) == svcName {
			m := svc.Methods().ByName(protoreflect.Name(methodName))
			if m == nil {
				return nil, fmt.Errorf("method %q not found in service %q", methodName, svcName)
			}
			return &methodInfo{method: m}, nil
		}
	}
	return nil, fmt.Errorf("service %q not found in proto definition", svcName)
}

// connectURL builds the Connect request URL: {baseURL}/{service}/{method}.
func connectURL(baseURL, svcName, methodName string) string {
	return strings.TrimRight(baseURL, "/") + "/" + svcName + "/" + methodName
}

// marshalRequestMessage marshals the request message to JSON bytes. With a
// method descriptor it round-trips through protojson so field names match the
// proto3 JSON canonical names (camelCase) the synthesizer writes into OBI
// schemas; without one it marshals the input directly. A nil input dispatches
// an empty message.
func marshalRequestMessage(input any, mi *methodInfo) ([]byte, *openbindings.InvocationError) {
	if input == nil {
		return []byte("{}"), nil
	}
	if mi != nil && mi.method != nil {
		msg := dynamicpb.NewMessage(mi.method.Input())
		inputMap, ok := input.(map[string]any)
		if !ok {
			return nil, &openbindings.InvocationError{
				Code:    openbindings.ErrCodeValidationFailed,
				Message: fmt.Sprintf("input must be a JSON object, got %T", input),
			}
		}
		jsonBytes, err := json.Marshal(inputMap)
		if err != nil {
			return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
		}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(jsonBytes, msg); err != nil {
			return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
		}
		body, err := protojson.Marshal(msg)
		if err != nil {
			return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
		}
		return body, nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, &openbindings.InvocationError{Code: openbindings.ErrCodeValidationFailed, Message: err.Error()}
	}
	return body, nil
}

// connectCodeToErrCode maps a Connect protocol error code to the standard
// invocation error codes.
func connectCodeToErrCode(code string) string {
	switch code {
	case "unauthenticated":
		return openbindings.ErrCodeAuthRequired
	case "permission_denied":
		return openbindings.ErrCodePermissionDenied
	case "unavailable":
		return openbindings.ErrCodeConnectFailed
	case "deadline_exceeded":
		return openbindings.ErrCodeTimeout
	default:
		return openbindings.ErrCodeExecutionFailed
	}
}

// applyConnectError refines an HTTP-status-derived terminal error with the
// Connect error payload (a JSON object with `code` and `message` fields) when
// the body parses as one.
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
// the protocol carries unary trailers as "Trailer-"-prefixed HTTP headers.
const unaryTrailerPrefix = "Trailer-"

// splitUnaryMetadata separates a unary response's leading headers from its
// trailing metadata: "Trailer-"-prefixed headers (the Connect unary trailer
// convention, prefix stripped) plus any HTTP/1.1 trailers (resp.Trailer is
// populated once the body has been fully read).
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

// runUnary sends a Connect protocol unary request (HTTP POST with JSON) and
// drives the handle: leading headers, one output, trailing metadata, then
// CloseOutput — or FireError with the mapped terminal error.
func (e *Invoker) runUnary(ctx context.Context, inv openbindings.BindingHandle[any, any], baseURL, svcName, methodName string, input any, headers map[string]string, mi *methodInfo) {
	body, ierr := marshalRequestMessage(input, mi)
	if ierr != nil {
		inv.FireError(ierr)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL(baseURL, svcName, methodName), bytes.NewReader(body))
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: err.Error()})
		return
	}

	// Connect protocol headers.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

	// Apply credentials and custom headers.
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

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: err.Error()})
		return
	}
	if int64(len(respBody)) > maxResponseBytes {
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes),
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

	if resp.StatusCode >= 400 {
		ierr := openbindings.HTTPError(resp.StatusCode, resp.Status)
		applyConnectError(ierr, respBody)
		inv.FireError(ierr)
		return
	}

	var output any
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &output); err != nil {
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeResponseError, Message: err.Error()})
			return
		}
	}

	if err := inv.EmitOutput(output); err != nil {
		return
	}
	inv.CloseOutput()
}

// buildHTTPHeaders constructs HTTP headers from binding context.
func buildHTTPHeaders(bindCtx map[string]any) map[string]string {
	headers := map[string]string{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		headers["Authorization"] = "ApiKey " + key
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		encoded := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
		headers["Authorization"] = "Basic " + encoded
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

	return headers
}
