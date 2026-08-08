package openbindings

import (
	"errors"
	"strings"
)

// HTTPError builds the terminal *InvocationError for an HTTP error response.
// The status is retained only as explicit Diagnostics. Read it through
// HTTPStatus rather than asserting the diagnostic shape.
// The native status text is deliberately not copied into the ordinary
// message; format invokers fire this via BindingHandle.FireError and retain
// the native response only on Diagnostics.
func HTTPError(statusCode int, _ string) *InvocationError {
	return &InvocationError{
		Code:        HTTPErrorCode(statusCode),
		Message:     "Invocation completed unsuccessfully",
		Diagnostics: map[string]any{"status": statusCode},
	}
}

// HTTPErrorCode returns the SDK's open, non-portable code for a concrete HTTP
// failure. The invocation interface deliberately does not map status numbers
// into a closed cross-protocol taxonomy; callers observe unsuccessful
// completion structurally and may inspect HTTPStatus only on an expert
// diagnostic path.
func HTTPErrorCode(statusCode int) string {
	return ErrCodeExecutionFailed
}

// HTTPStatus extracts the HTTP status code from an invocation error's explicit
// diagnostic escape hatch. Ordinary protocol-blind operation behavior must not
// depend on this value.
func HTTPStatus(err error) (int, bool) {
	m := diagnosticsMap(err)
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

// HTTPResponseBody extracts a diagnostically retained HTTP error body.
func HTTPResponseBody(err error) (string, bool) {
	m := diagnosticsMap(err)
	if m == nil {
		return "", false
	}
	body, ok := m["body"].(string)
	return body, ok
}

func diagnosticsMap(err error) map[string]any {
	var ie *InvocationError
	if !errors.As(err, &ie) || ie == nil {
		return nil
	}
	m, _ := ie.Diagnostics.(map[string]any)
	return m
}

// IsHTTPURL reports whether s starts with http:// or https://.
func IsHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
