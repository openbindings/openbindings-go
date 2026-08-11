package openapi

import (
	"bytes"
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
	return isSSEContentTypeFor(contentType, BindingSpec)
}

func isSSEContentTypeFor(contentType, bindingSpec string) bool {
	if hasMediaFidelity(bindingSpec) {
		parsed, err := parseRevision3MediaType(contentType)
		return err == nil && parsed.base == "text/event-stream"
	}
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
