package openapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// sseMaxLineBytes bounds individual SSE line length to prevent runaway memory
// use from a misbehaving server. The W3C SSE spec does not impose a line
// limit, but a single 16 MB line is generous in practice.
const sseMaxLineBytes = 16 * 1024 * 1024

// isSSEContentType reports whether the given Content-Type header value
// indicates a Server-Sent Events stream. Per the W3C SSE specification, the
// MIME type is `text/event-stream`. Charset and other parameters may follow.
func isSSEContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mt := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	return mt == "text/event-stream"
}

// streamSSE reads a `text/event-stream` HTTP response body line by line per
// the W3C Server-Sent Events specification, emitting each parsed event as one
// output on the invocation handle. It takes ownership of closing the body and
// the terminal transition: CloseOutput when the body is exhausted cleanly,
// FireError(ERR_STREAM_ERROR) on a scan failure, and a clean return (the
// handle is already terminal) when the caller cancels.
//
// SSE event field handling:
//
//   - `data:` lines accumulate; multiple data lines for one event are joined
//     with a literal newline (per the spec)
//   - `event:`, `id:`, `retry:` fields are parsed and, when present, wrap the
//     payload in an object so the consumer can access both
//   - Comment lines (starting with `:`) are ignored
//   - Blank lines dispatch the accumulated event
//
// If a data payload parses as JSON, the parsed value is emitted; otherwise the
// raw string is emitted.
func streamSSE(ctx context.Context, resp *http.Response, inv openbindings.BindingHandle[any, any]) {
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, sseMaxLineBytes)

	var (
		eventName string
		eventID   string
		dataLines []string
		retryMs   int
	)

	// dispatch emits the accumulated event. It returns false when the emit
	// fails (the invocation terminated while parked), signalling the caller
	// to stop reading the body.
	dispatch := func() bool {
		if len(dataLines) == 0 && eventName == "" && eventID == "" {
			return true
		}
		rawData := strings.Join(dataLines, "\n")
		var data any = rawData
		if openbindings.MaybeJSON(strings.TrimSpace(rawData)) {
			var parsed any
			if json.Unmarshal([]byte(rawData), &parsed) == nil {
				data = parsed
			}
		}
		if eventName != "" || eventID != "" || retryMs != 0 {
			wrapped := map[string]any{"data": data}
			if eventName != "" {
				wrapped["event"] = eventName
			}
			if eventID != "" {
				wrapped["id"] = eventID
			}
			if retryMs != 0 {
				wrapped["retry"] = retryMs
			}
			data = wrapped
		}

		eventName = ""
		eventID = ""
		dataLines = nil
		retryMs = 0

		return inv.EmitOutput(data) == nil
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return // cancelled; the handle is already terminal
		}
		line := scanner.Text()
		// Strip optional trailing CR (servers may send CRLF).
		line = strings.TrimSuffix(line, "\r")

		if line == "" {
			if !dispatch() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			// Comment line; ignored per spec.
			continue
		}

		var field, value string
		if i := strings.IndexByte(line, ':'); i >= 0 {
			field = line[:i]
			value = line[i+1:]
			// Per spec, a single leading space in the value is stripped.
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		} else {
			// A line with no colon is treated as a field with empty value.
			field = line
			value = ""
		}

		switch field {
		case "event":
			eventName = value
		case "id":
			eventID = value
		case "data":
			dataLines = append(dataLines, value)
		case "retry":
			if ms, err := strconv.Atoi(value); err == nil && ms >= 0 {
				retryMs = ms
			}
		}
		// Unknown fields are ignored per spec.
	}

	// Flush any pending event when the body ends without a trailing blank line.
	if !dispatch() {
		return
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return
		}
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeStreamError,
			Message: err.Error(),
		})
		return
	}
	inv.CloseOutput()
}
