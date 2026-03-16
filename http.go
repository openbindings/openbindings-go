package openbindings

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ApplyHTTPContext applies BindingContext credentials and headers to an HTTP request.
func ApplyHTTPContext(req *http.Request, bindCtx *BindingContext) {
	if bindCtx == nil {
		return
	}

	if bindCtx.Credentials != nil {
		creds := bindCtx.Credentials
		if creds.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+creds.BearerToken)
		} else if creds.APIKey != "" {
			req.Header.Set("Authorization", "ApiKey "+creds.APIKey)
		} else if creds.Basic != nil {
			req.SetBasicAuth(creds.Basic.Username, creds.Basic.Password)
		}
	}

	for k, v := range bindCtx.Headers {
		req.Header.Set(k, v)
	}
}

// HTTPErrorOutput builds an ExecuteOutput from an HTTP error response.
func HTTPErrorOutput(start time.Time, statusCode int, status string) *ExecuteOutput {
	return &ExecuteOutput{
		Status:     statusCode,
		DurationMs: time.Since(start).Milliseconds(),
		Error: &ExecuteError{
			Code:    fmt.Sprintf("http_%d", statusCode),
			Message: fmt.Sprintf("HTTP %d %s", statusCode, status),
		},
	}
}

// IsHTTPURL reports whether s starts with http:// or https://.
func IsHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
