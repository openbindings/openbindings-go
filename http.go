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
		Effects: httpErrorEffects(statusCode),
		Details: map[string]any{"status": statusCode},
	}
}

// httpErrorEffects reports what an HTTP error status proves about side
// effects. A 429 or 503 is the server refusing the request before it executed
// (rate-limited / unavailable), so the call provably did not take hold —
// EffectsNone licenses a backoff-retry. Every other status is left unset and
// so is treated as EffectsPossible by consumers: a 502 Bad Gateway may already
// have reached the backend, a 408/504 timeout was dispatched, and any other
// 5xx may have executed — none is safe to blind-retry for a non-idempotent
// operation. (ERR_UNAVAILABLE deliberately carries no effects default of its
// own, because its effects are per-status: none for 429/503, possible for 502;
// classify() supplies EffectsPossible for the transport-transient ERR_TIMEOUT
// that 408/504 map to.)
func httpErrorEffects(statusCode int) Effects {
	switch statusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable: // 429, 503
		return EffectsNone
	default:
		return ""
	}
}

// HTTPErrorCode maps an HTTP status code to a standard error code constant,
// implementing the binding-invoker contract's HTTP status→code table. The
// category follows from the returned code via the canonical code→category map.
// Shared utility for format invokers that handle HTTP responses.
//
//   - 401 Unauthorized          → ERR_AUTH_REQUIRED     (auth)
//   - 403 Forbidden             → ERR_PERMISSION_DENIED (auth)
//   - 408, 504                  → ERR_TIMEOUT           (transient)
//   - 429, 502, 503             → ERR_UNAVAILABLE       (transient)
//   - every other 4xx and 5xx   → ERR_EXECUTION_FAILED  (service)
//
// The numeric status is preserved on the error's Details, so callers can still
// branch on 404, 422, and the like via HTTPStatus.
func HTTPErrorCode(statusCode int) string {
	switch statusCode {
	case http.StatusUnauthorized: // 401
		return ErrCodeAuthRequired
	case http.StatusForbidden: // 403
		return ErrCodePermissionDenied
	case http.StatusRequestTimeout, http.StatusGatewayTimeout: // 408, 504
		return ErrCodeTimeout
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable: // 429, 502, 503
		return ErrCodeUnavailable
	default:
		// Every other 4xx and every 5xx: the request reached the server and
		// was refused on its merits — a service error, not a blind-retry.
		return ErrCodeExecutionFailed
	}
}

// HTTPStatus extracts the HTTP status code from an invocation error whose
// binding spoke HTTP. The status→code table folds several statuses onto a
// shared code (429/502/503 → ERR_UNAVAILABLE, 408/504 → ERR_TIMEOUT, every
// other 4xx/5xx → ERR_EXECUTION_FAILED, and 401/403 to the auth codes), so
// real REST error handling still branches on the numeric status — 404
// not-found vs 422 caller bug vs a specific retry backoff — and this accessor
// is the supported way to reach it (the Details shape is not a contract).
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
