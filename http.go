package openbindings

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// HTTPError builds the terminal *InvocationError for an HTTP error response,
// carrying the status in Details. status is typically resp.Status from
// net/http (e.g. "401 Unauthorized"), not a bare reason phrase. Format
// invokers fire it via BindingHandle.FireError.
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

// IsHTTPURL reports whether s starts with http:// or https://.
func IsHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
