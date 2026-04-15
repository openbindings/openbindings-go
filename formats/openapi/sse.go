package openapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// streamSSEResponse reads a `text/event-stream` HTTP response body line by
// line per the W3C Server-Sent Events specification, dispatching each parsed
// event as a StreamEvent on the returned channel. The channel closes when the
// body is exhausted, the caller cancels via ctx, or an unrecoverable error
// occurs.
//
// SSE event field handling:
//
//   - `data:` lines accumulate; multiple data lines for one event are joined
//     with a literal newline (per the spec)
//   - `event:`, `id:`, `retry:` fields are parsed but currently surfaced only
//     by encoding the event name into the StreamEvent's Data when present
//   - Comment lines (starting with `:`) are ignored
//   - Blank lines dispatch the accumulated event
//
// If a data payload parses as JSON, the parsed value is emitted as
// StreamEvent.Data. Otherwise the raw string is emitted.
func streamSSEResponse(ctx context.Context, resp *http.Response, start time.Time) <-chan openbindings.StreamEvent {
	ch := make(chan openbindings.StreamEvent, 16)
	go func() {
		defer close(ch)
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

		dispatch := func() {
			if len(dataLines) == 0 && eventName == "" && eventID == "" {
				return
			}
			rawData := strings.Join(dataLines, "\n")
			var data any = rawData
			if openbindings.MaybeJSON(strings.TrimSpace(rawData)) {
				var parsed any
				if json.Unmarshal([]byte(rawData), &parsed) == nil {
					data = parsed
				}
			}
			// Surface the SSE event name and id alongside the payload when
			// present. We wrap in an object so the consumer can access both.
			// When the event has only a data payload (no event/id/retry),
			// emit the parsed payload directly so simple SSE streams produce
			// idiomatic output.
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

			select {
			case ch <- openbindings.StreamEvent{Data: data, Status: resp.StatusCode}:
			case <-ctx.Done():
			}

			eventName = ""
			eventID = ""
			dataLines = nil
			retryMs = 0
		}

		for scanner.Scan() {
			if ctx.Err() != nil {
				return
			}
			line := scanner.Text()
			// Strip optional trailing CR (servers may send CRLF).
			line = strings.TrimSuffix(line, "\r")

			if line == "" {
				dispatch()
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
		dispatch()

		if err := scanner.Err(); err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
				Code:    openbindings.ErrCodeStreamError,
				Message: err.Error(),
			}}:
			case <-ctx.Done():
			}
		}
		_ = io.Discard
	}()
	return ch
}
