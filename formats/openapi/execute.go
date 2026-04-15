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
	"strings"
	"time"

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

// executeBindingWithDoc executes one OpenAPI binding. It returns either a
// completed *ExecuteOutput (the unary case) OR a stream channel (for
// `text/event-stream` responses). Exactly one of the two return values is
// non-nil. The caller dispatches based on which is set.
//
// The dual return shape avoids forcing every unary code path through a
// channel allocation while still allowing SSE responses to surface as a
// natural multi-event stream.
func executeBindingWithDoc(ctx context.Context, client *http.Client, input *openbindings.BindingExecutionInput, doc *openapi3.T) (*openbindings.ExecuteOutput, <-chan openbindings.StreamEvent) {
	start := time.Now()

	pathTemplate, method, err := parseRef(input.Ref)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidRef, err.Error()), nil
	}

	baseURL, err := resolveBaseURLWithLocation(doc, input.Options, input.Source.Location)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeSourceConfigError, err.Error()), nil
	}

	if doc.Paths == nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeSourceConfigError, "OpenAPI document has no paths defined"), nil
	}
	pathItem := doc.Paths.Find(pathTemplate)
	if pathItem == nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound, fmt.Sprintf("path %q not in OpenAPI doc", pathTemplate)), nil
	}
	op := pathItem.GetOperation(strings.ToUpper(method))
	if op == nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeRefNotFound, fmt.Sprintf("method %q not in path %q", method, pathTemplate)), nil
	}

	return doHTTPRequest(ctx, client, input, doc, op, pathItem, pathTemplate, method, baseURL, start)
}

func doHTTPRequest(ctx context.Context, client *http.Client, input *openbindings.BindingExecutionInput, doc *openapi3.T, op *openapi3.Operation, pathItem *openapi3.PathItem, pathTemplate, method, baseURL string, start time.Time) (*openbindings.ExecuteOutput, <-chan openbindings.StreamEvent) {
	allParams := mergeParameters(pathItem.Parameters, op.Parameters)
	inputMap, _ := openbindings.ToStringAnyMap(input.Input)
	if inputMap == nil {
		inputMap = map[string]any{}
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
				return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error()), nil
			}
			bodyReader = buf
			contentType = ct
		} else {
			bodyBytes, err := json.Marshal(bodyFields)
			if err != nil {
				return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error()), nil
			}
			bodyReader = bytes.NewReader(bodyBytes)
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), reqURL, bodyReader)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error()), nil
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

	applyHTTPContext(req, doc, op, input.Context, input.Options)

	resp, err := client.Do(req)
	if err != nil {
		return openbindings.FailedOutput(start, classifyHTTPError(ctx, err), err.Error()), nil
	}

	// SSE dispatch: a 2xx response with text/event-stream content type is a
	// streaming response. Hand the (still-open) response to the SSE streamer
	// which takes ownership of closing the body.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && isSSEContentType(resp.Header.Get("Content-Type")) {
		return nil, streamSSEResponse(ctx, resp, start)
	}

	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError, err.Error()), nil
	}
	if len(respBody) > maxResponseBytes {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError,
			fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes)), nil
	}

	duration := time.Since(start).Milliseconds()

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
		errOutput := openbindings.HTTPErrorOutput(start, resp.StatusCode, resp.Status)
		errOutput.Output = output
		return errOutput, nil
	}

	return &openbindings.ExecuteOutput{
		Output:     output,
		Status:     resp.StatusCode,
		DurationMs: duration,
	}, nil
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

func resolveBaseURL(doc *openapi3.T, opts *openbindings.ExecutionOptions) (string, error) {
	if opts != nil && opts.Metadata != nil {
		if base, ok := opts.Metadata["baseURL"].(string); ok && base != "" {
			return strings.TrimRight(base, "/"), nil
		}
	}

	if len(doc.Servers) > 0 {
		serverURL := doc.Servers[0].URL
		if serverURL != "" {
			return strings.TrimRight(serverURL, "/"), nil
		}
	}

	return "", fmt.Errorf("no server URL: set servers in the OpenAPI doc or provide baseURL in execution options metadata")
}

// resolveBaseURLWithLocation resolves the base URL, falling back to the source
// location's origin when the spec has a relative server URL (e.g. "/api/v3").
func resolveBaseURLWithLocation(doc *openapi3.T, opts *openbindings.ExecutionOptions, sourceLocation string) (string, error) {
	base, err := resolveBaseURL(doc, opts)
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
		default:
			body[name] = value
		}
	}

	return resolvedPath, query, headers, body
}

func hasRequestBody(op *openapi3.Operation) bool {
	return op.RequestBody != nil && op.RequestBody.Value != nil
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
// fields) and execution options (headers, cookies) to an HTTP request, using
// OpenAPI securitySchemes for spec-driven credential placement.
func applyHTTPContext(req *http.Request, doc *openapi3.T, op *openapi3.Operation, bindCtx map[string]any, opts *openbindings.ExecutionOptions) {
	if len(bindCtx) > 0 {
		if !applyCredentialsViaSecuritySchemes(req, doc, op, bindCtx) {
			applyCredentialsFallback(req, bindCtx)
		}
	}

	if opts != nil {
		for k, v := range opts.Headers {
			req.Header.Set(k, v)
		}
		for k, v := range opts.Cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}
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
