package openapi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// sseMaxLineBytes bounds individual SSE line length to prevent runaway memory
// use from a misbehaving server. The WHATWG SSE spec does not impose a line
// limit, but a single 16 MB line is generous in practice.
//
// Deliberately fixed: this is a line-scanner internal guard, not the
// delivery-unit bound — BindingInvocationArgs.MaxDeliveryUnitBytes does not
// apply here.
const sseMaxLineBytes = 16 * 1024 * 1024

// isSSEContentType reports whether the given Content-Type header value
// indicates a Server-Sent Events stream (`text/event-stream`). Charset and
// other parameters may follow.
func isSSEContentType(contentType string) bool {
	return normalizeMediaType(contentType) == "text/event-stream"
}

// scanSSELines is a bufio.SplitFunc for the WHATWG event-stream line
// grammar: lines end with CRLF, a lone LF, or a lone CR. A CR at the end of
// the buffered data waits for more input (the LF of a CRLF pair may not have
// arrived yet) unless the stream is at EOF.
func scanSSELines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			return i + 1, data[:i], nil
		}
		// CR: swallow a following LF when it is available.
		if i+1 < len(data) {
			if data[i+1] == '\n' {
				return i + 2, data[:i], nil
			}
			return i + 1, data[:i], nil
		}
		if atEOF {
			return i + 1, data[:i], nil
		}
		return 0, nil, nil // need more data to decide CR vs CRLF
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// streamSSE reads a `text/event-stream` HTTP response body per the WHATWG
// server-sent events processing model (openbindings.openapi@1 §8,
// OAPI-P-06), emitting one output per received event on the invocation
// handle. It takes ownership of closing the body and the terminal
// transition: CloseOutput when the body is exhausted cleanly,
// FireError(ERR_STREAM_ERROR) on a scan failure (abnormal termination is a
// failure outcome; output values already emitted stand), and a clean return
// (the handle is already terminal) when the caller cancels.
//
// Event extraction, per the WHATWG model:
//
//   - `data:` lines accumulate; an event's data lines joined with U+000A
//     form the event's text
//   - comment-only and empty-`data` events emit no value (an event whose
//     joined data text is empty is discarded, `event:`/`id:`-only events
//     included)
//   - `event`, `id`, and `retry` are FRAMING: they never enter the output
//     value. This implementation surfaces them out of band on the per-unit
//     Meta (x-sse-event / x-sse-id / x-sse-retry); the last event ID
//     persists across events until changed, WHATWG lastEventId semantics
//   - an incomplete final event (end of stream before its dispatching blank
//     line) is discarded, never flushed
//
// Decode runs per delivery unit (event) through the consultation seam. The
// builtin lane is the per-event text default (OAPI-P-07): the event's
// U+000A-joined data text itself — SSE events carry no per-event framing to
// decide otherwise, so JSON-emitting endpoints decode via consumer
// configuration at the decode point. Status carries the initial response's
// status on every unit (it is real and invocation-scoped, never fabricated);
// classification ran once, at dispatch.
func streamSSE(ctx context.Context, resp *http.Response, args *openbindings.BindingInvocationArgs, site openbindings.InvokeSite, inv openbindings.BindingHandle[any, any]) {
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, sseMaxLineBytes)
	scanner.Split(scanSSELines)

	var (
		eventName   string
		lastEventID string
		dataLines   []string
		retryMs     int
		eventBytes  int64
		firstLine   = true
	)
	maxUnit := args.DeliveryUnitLimit()

	// dispatch emits the accumulated event. It returns false when the emit
	// fails (the invocation terminated while parked), signalling the caller
	// to stop reading the body.
	status := resp.StatusCode
	invocationMeta := headerMetadata(resp.Header)
	dispatch := func() bool {
		rawData := strings.Join(dataLines, "\n")
		name := eventName
		eventName = ""
		dataLines = nil
		// Comment-only and empty-data events emit no value: an event whose
		// joined data text is empty is discarded (WHATWG step 2 plus the
		// trailing-newline strip; the last event ID, already set, persists).
		if rawData == "" {
			return true
		}

		// Per-unit Meta: invocation-scoped headers merged with this
		// event's framing fields.
		meta := make(openbindings.Metadata, len(invocationMeta)+3)
		for k, v := range invocationMeta {
			meta[k] = v
		}
		if name != "" {
			meta["x-sse-event"] = []string{name}
		}
		if lastEventID != "" {
			meta["x-sse-id"] = []string{lastEventID}
		}
		if retryMs != 0 {
			meta["x-sse-retry"] = []string{strconv.Itoa(retryMs)}
			retryMs = 0
		}

		raw := openbindings.RawResult{
			Status: &status,
			Body:   []byte(rawData),
			Meta:   meta,
		}
		data, derr := args.Hooks.DecodeOutput(site, raw, decodeByContentType(resp.Header.Get("Content-Type")))
		if derr != nil {
			// A decode error mid-stream is terminal; already-emitted
			// outputs stand (drain-before-terminal).
			inv.FireError(openbindings.AsInvocationError(derr))
			return false
		}

		return inv.EmitOutput(data) == nil
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return // cancelled; the handle is already terminal
		}
		line := scanner.Text()
		if firstLine {
			// One leading U+FEFF BOM is ignored per the WHATWG stream grammar.
			line = strings.TrimPrefix(line, "\uFEFF")
			firstLine = false
		}

		// The size cap is PER EVENT, not cumulative: a long-lived stream
		// legitimately delivers more than one delivery unit in total (the
		// same choice asyncapi's streamSSE documents for its per-event cap).
		// One event is one delivery unit, consumer-bounded via
		// BindingInvocationArgs.MaxDeliveryUnitBytes (default 10 MiB).
		eventBytes += int64(len(line)) + 1 // +1 for newline
		if eventBytes > maxUnit {
			inv.FireError(&openbindings.InvocationError{
				Code:    openbindings.ErrCodeResponseError,
				Message: fmt.Sprintf("SSE event exceeds %d byte limit", maxUnit),
			})
			return
		}

		if line == "" {
			eventBytes = 0
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
			// A value containing U+0000 NULL is ignored; otherwise it sets
			// the last event ID (an empty value resets it), per WHATWG.
			if !strings.ContainsRune(value, '\x00') {
				lastEventID = value
			}
		case "data":
			dataLines = append(dataLines, value)
		case "retry":
			// ASCII digits only, per WHATWG; anything else is ignored.
			if value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
				if ms, err := strconv.Atoi(value); err == nil {
					retryMs = ms
				}
			}
		}
		// Unknown fields are ignored per spec.
	}

	// End of stream: an incomplete final event (no dispatching blank line)
	// is discarded per the WHATWG processing model — never flushed.

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
