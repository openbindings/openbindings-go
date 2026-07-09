package openbindings

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// HTTPError builds the terminal *InvocationError for an HTTP error response,
// carrying the status in Details ("status": int; HTTP-speaking invokers also
// add "body": string with the error response payload). Read them through
// HTTPStatus and HTTPResponseBody rather than asserting the Details shape.
// status is typically resp.Status from net/http (e.g. "401 Unauthorized"),
// not a bare reason phrase. Format invokers fire it via
// BindingHandle.FireError.
func HTTPError(statusCode int, status string) *InvocationError {
	reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(status), strconv.Itoa(statusCode)))
	if reason == "" {
		reason = http.StatusText(statusCode)
	}
	return &InvocationError{
		Code:    HTTPErrorCode(statusCode),
		Message: fmt.Sprintf("HTTP %d %s", statusCode, reason),
		Details: map[string]any{"status": statusCode},
	}
}

// HTTPErrorCode maps an HTTP status code to a standard error code constant.
// Shared utility for format invokers that handle HTTP responses.
func HTTPErrorCode(statusCode int) string {
	switch statusCode {
	case 401:
		return ErrCodeAuthRequired
	case 403:
		return ErrCodePermissionDenied
	default:
		return ErrCodeExecutionFailed
	}
}

// HTTPStatus extracts the HTTP status code from an invocation error whose
// binding spoke HTTP. ErrCodeExecutionFailed is the catch-all above the
// auth codes (401/403 get their own), so real REST error handling branches
// on the status — 404 not-found vs 422 caller bug vs 429/503 retry — and
// this accessor is the supported way to reach it (the Details shape is not
// a contract).
func HTTPStatus(err error) (int, bool) {
	m := detailsMap(err)
	if m == nil {
		return 0, false
	}
	switch v := m["status"].(type) {
	case int:
		return v, true
	case float64: // Details that crossed a JSON wire
		return int(v), true
	}
	return 0, false
}

// HTTPResponseBody extracts the error response body an HTTP-speaking
// invoker preserved alongside the status, when there was one.
func HTTPResponseBody(err error) (string, bool) {
	m := detailsMap(err)
	if m == nil {
		return "", false
	}
	body, ok := m["body"].(string)
	return body, ok
}

func detailsMap(err error) map[string]any {
	var ie *InvocationError
	if !errors.As(err, &ie) || ie == nil {
		return nil
	}
	m, _ := ie.Details.(map[string]any)
	return m
}

// IsHTTPURL reports whether s starts with http:// or https://.
func IsHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
