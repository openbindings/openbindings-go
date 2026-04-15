package openbindings

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPErrorOutput builds an ExecuteOutput from an HTTP error response.
// status is typically resp.Status from net/http (e.g. "401 Unauthorized"), not a bare reason phrase.
func HTTPErrorOutput(start time.Time, statusCode int, status string) *ExecuteOutput {
	reason := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(status), strconv.Itoa(statusCode)))
	if reason == "" {
		reason = http.StatusText(statusCode)
	}
	return &ExecuteOutput{
		Status:     statusCode,
		DurationMs: time.Since(start).Milliseconds(),
		Error: &ExecuteError{
			Code:    httpErrorCode(statusCode),
			Message: fmt.Sprintf("HTTP %d %s", statusCode, reason),
		},
	}
}

// httpErrorCode maps an HTTP status code to a standard error code constant.
func httpErrorCode(statusCode int) string {
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
