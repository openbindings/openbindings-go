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
	"time"

	"github.com/golang/protobuf/jsonpb" //nolint:staticcheck // required by jhump/protoreflect/dynamic
	"github.com/jhump/protoreflect/desc" //nolint:staticcheck // no v2 equivalent yet
	"github.com/jhump/protoreflect/dynamic" //nolint:staticcheck // no v2 equivalent yet
	openbindings "github.com/openbindings/openbindings-go"
)

const maxResponseBytes int64 = 10 * 1024 * 1024

// methodInfo holds a resolved method descriptor for input/output marshaling.
type methodInfo struct {
	method *desc.MethodDescriptor
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
func resolveMethod(content any, svcName, methodName string) (*methodInfo, error) {
	disc, err := discoverFromProto("", content)
	if err != nil {
		return nil, err
	}
	for _, svc := range disc.services {
		if svc.GetFullyQualifiedName() == svcName {
			m := svc.FindMethodByName(methodName)
			if m == nil {
				return nil, fmt.Errorf("method %q not found in service %q", methodName, svcName)
			}
			return &methodInfo{method: m}, nil
		}
	}
	return nil, fmt.Errorf("service %q not found in proto definition", svcName)
}

// executeConnect sends a Connect protocol request via HTTP POST.
// The URL format is: {baseURL}/{service}/{method}
func executeConnect(ctx context.Context, client *http.Client, baseURL, svcName, methodName string, input any, headers map[string]string, mi *methodInfo, start time.Time) *openbindings.ExecuteOutput {
	// Build the Connect URL: POST /{service}/{method}
	connectURL := strings.TrimRight(baseURL, "/") + "/" + svcName + "/" + methodName

	// Marshal input to JSON.
	var body []byte
	if input != nil {
		if mi != nil && mi.method != nil {
			// Use protobuf-aware marshaling for field name accuracy.
			msg := dynamic.NewMessage(mi.method.GetInputType())
			inputMap, ok := input.(map[string]any)
			if !ok {
				return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, fmt.Sprintf("input must be a JSON object, got %T", input))
			}
			jsonBytes, err := json.Marshal(inputMap)
			if err != nil {
				return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
			}
			if err := msg.UnmarshalJSONPB(&jsonpb.Unmarshaler{AllowUnknownFields: true}, jsonBytes); err != nil {
				return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
			}
			body, err = msg.MarshalJSONPB(&jsonpb.Marshaler{OrigName: true})
			if err != nil {
				return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
			}
		} else {
			// No proto descriptor; marshal directly as JSON.
			var err error
			body, err = json.Marshal(input)
			if err != nil {
				return openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())
			}
		}
	} else {
		body = []byte("{}")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL, bytes.NewReader(body))
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error())
	}

	// Connect protocol headers.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")

	// Apply credentials and custom headers.
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeConnectFailed, err.Error())
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError, err.Error())
	}
	if int64(len(respBody)) > maxResponseBytes {
		return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError,
			fmt.Sprintf("response exceeds %d byte limit", maxResponseBytes))
	}

	duration := time.Since(start).Milliseconds()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return openbindings.HTTPErrorOutput(start, resp.StatusCode, resp.Status)
	}

	if resp.StatusCode >= 400 {
		errOutput := openbindings.HTTPErrorOutput(start, resp.StatusCode, resp.Status)
		// Try to parse Connect error response.
		var connectErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(respBody, &connectErr) == nil && connectErr.Message != "" {
			errOutput.Error.Message = connectErr.Message
		}
		return errOutput
	}

	// Parse response JSON.
	var output any
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &output); err != nil {
			return openbindings.FailedOutput(start, openbindings.ErrCodeResponseError, err.Error())
		}
	}

	return &openbindings.ExecuteOutput{Output: output, Status: resp.StatusCode, DurationMs: duration}
}

// buildHTTPHeaders constructs HTTP headers from binding context and execution options.
func buildHTTPHeaders(bindCtx map[string]any, opts *openbindings.ExecutionOptions) map[string]string {
	headers := map[string]string{}

	if token := openbindings.ContextBearerToken(bindCtx); token != "" {
		headers["Authorization"] = "Bearer " + token
	} else if key := openbindings.ContextAPIKey(bindCtx); key != "" {
		headers["Authorization"] = "ApiKey " + key
	} else if u, p, ok := openbindings.ContextBasicAuth(bindCtx); ok {
		encoded := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
		headers["Authorization"] = "Basic " + encoded
	}

	if opts != nil {
		for k, v := range opts.Headers {
			headers[k] = v
		}
		if len(opts.Cookies) > 0 {
			pairs := make([]string, 0, len(opts.Cookies))
			for k, v := range opts.Cookies {
				pairs = append(pairs, k+"="+v)
			}
			sort.Strings(pairs)
			headers["Cookie"] = strings.Join(pairs, "; ")
		}
	}

	return headers
}
