package invoke

// HTTPError builds the terminal *InvocationError for an HTTP error response.
// The native status remains below the abstract invocation boundary.
func HTTPError(statusCode int, _ string) *InvocationError {
	return &InvocationError{
		Code: HTTPErrorCode(statusCode),
	}
}

// HTTPErrorCode returns the SDK's open, non-portable code for a concrete HTTP
// failure. The invocation interface deliberately does not map status numbers
// into a closed cross-protocol taxonomy; callers observe unsuccessful
// completion structurally.
func HTTPErrorCode(statusCode int) string {
	return ErrCodeExecutionFailed
}
