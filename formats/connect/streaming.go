package connect

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	openbindings "github.com/openbindings/openbindings-go"
)

// Connect streaming wire format constants.
//
// Per the Connect protocol specification (https://connectrpc.com/docs/protocol),
// streaming RPCs use an envelope-framed format. Each envelope is a 5-byte
// header followed by a payload:
//
//	+-------+----------+-----------+
//	| flags | length   | payload   |
//	| 1B    | 4B BE    | length B  |
//	+-------+----------+-----------+
//
// The flags byte is a bitfield. We support two bits:
//   - Bit 0 (0x01): COMPRESSED — payload is compressed (we do not support compression in v0.1)
//   - Bit 1 (0x02): END_STREAM — this envelope terminates the stream
//
// A server-streaming response is a sequence of zero or more data envelopes
// (flags = 0) followed by exactly one end-stream envelope (flags = END_STREAM).
// The end-stream payload is a JSON object with optional `error` and `metadata`
// fields. An error in the end-stream payload indicates the stream terminated
// abnormally.
const (
	connectFlagCompressed = 0x01
	connectFlagEndStream  = 0x02

	// streamingContentType is the Content-Type for Connect streaming with JSON payloads.
	streamingContentType = "application/connect+json"
)

// writeConnectEnvelope writes a single Connect envelope (5-byte header +
// payload) to w. flags should be 0 for a normal data frame or
// connectFlagEndStream for an end-stream frame. Compression is not supported.
func writeConnectEnvelope(w io.Writer, flags byte, payload []byte) error {
	if flags&connectFlagCompressed != 0 {
		return fmt.Errorf("connect: compression is not supported")
	}
	header := make([]byte, 5)
	header[0] = flags
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// readConnectEnvelope reads one Connect envelope from r and returns its flags
// and payload. Returns io.EOF if the reader is exhausted before any header
// bytes are read. Returns io.ErrUnexpectedEOF if a partial header or partial
// payload is encountered. Refuses payloads larger than maxPayload bytes.
func readConnectEnvelope(r io.Reader, maxPayload int64) (flags byte, payload []byte, err error) {
	header := make([]byte, 5)
	n, err := io.ReadFull(r, header)
	if err != nil {
		if err == io.EOF && n == 0 {
			return 0, nil, io.EOF
		}
		if err == io.ErrUnexpectedEOF {
			return 0, nil, fmt.Errorf("connect: short envelope header (got %d bytes, want 5)", n)
		}
		return 0, nil, err
	}
	flags = header[0]
	length := binary.BigEndian.Uint32(header[1:])
	if int64(length) > maxPayload {
		return 0, nil, fmt.Errorf("connect: envelope payload size %d exceeds limit %d", length, maxPayload)
	}
	if flags&connectFlagCompressed != 0 {
		return 0, nil, fmt.Errorf("connect: compressed envelopes are not supported")
	}
	if length == 0 {
		return flags, nil, nil
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, nil, fmt.Errorf("connect: short envelope payload (want %d bytes)", length)
		}
		return 0, nil, err
	}
	return flags, payload, nil
}

// connectEndStream is the JSON payload of an end-stream envelope.
type connectEndStream struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	Metadata map[string][]string `json:"metadata,omitempty"`
}

// runStreaming sends a server-streaming Connect RPC and drives the handle:
// leading headers from the HTTP response, one EmitOutput per data envelope,
// trailing metadata from the end-stream envelope, then CloseOutput (clean
// end) or FireError (end-stream error, stream failure).
//
// The Connect streaming wire format is described at:
// https://connectrpc.com/docs/protocol#streaming-rpcs
//
// Only server-streaming is supported. Client-streaming and bidirectional
// methods are out of the module's scope. ctx is already bound to the
// invocation's lifetime (DoneContext), so caller Cancel() tears down the
// in-flight request.
func (e *Invoker) runStreaming(ctx context.Context, inv openbindings.BindingHandle[any, any], baseURL, svcName, methodName string, input any, headers map[string]string, mi *methodInfo) {
	msgBytes, ierr := marshalRequestMessage(input, mi)
	if ierr != nil {
		inv.FireError(ierr)
		return
	}

	// Build the framed request body: a single envelope with flags=0 carrying
	// the request message, immediately followed by EOF (no end-stream envelope
	// is required from the client side for server-streaming RPCs).
	var body bytes.Buffer
	if err := writeConnectEnvelope(&body, 0, msgBytes); err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: err.Error()})
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL(baseURL, svcName, methodName), &body)
	if err != nil {
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeExecutionFailed, Message: err.Error()})
		return
	}
	req.Header.Set("Content-Type", streamingContentType)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Accept", streamingContentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // cancelled; the handle is already terminal
		}
		inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeConnectFailed, Message: err.Error()})
		return
	}
	defer resp.Body.Close()

	// Transport-level failures surface via HTTP status BEFORE streaming
	// begins. Connect streaming returns errors in the end-stream envelope
	// rather than via HTTP status, but a 401/403 from a proxy or middleware
	// can still appear at the HTTP layer.
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = inv.SetHeader(headerMetadata(resp.Header))
		ierr := openbindings.HTTPError(resp.StatusCode, resp.Status)
		applyConnectError(ierr, bodyBytes)
		inv.FireError(ierr)
		return
	}

	// Streaming responses MUST use the streaming content type.
	if gotCT := resp.Header.Get("Content-Type"); !strings.HasPrefix(gotCT, "application/connect+") {
		_ = inv.SetHeader(headerMetadata(resp.Header))
		inv.FireError(&openbindings.InvocationError{
			Code:    openbindings.ErrCodeResponseError,
			Message: fmt.Sprintf("expected Content-Type starting with application/connect+, got %q", gotCT),
		})
		return
	}

	// Leading metadata precedes the first emit.
	_ = inv.SetHeader(headerMetadata(resp.Header))

	// readConnectEnvelope enforces a per-envelope cap rather than bounding the
	// total stream size: a long-running subscription may legitimately produce
	// more than maxResponseBytes total.
	for {
		flags, payload, err := readConnectEnvelope(resp.Body, maxResponseBytes)
		if err == io.EOF {
			// Stream ended without an end-stream envelope. This is a
			// protocol violation, but we treat it as the stream simply
			// finishing rather than an error, to remain lenient.
			inv.CloseOutput()
			return
		}
		if err != nil {
			if ctx.Err() != nil {
				return // cancelled; the handle is already terminal
			}
			inv.FireError(&openbindings.InvocationError{Code: openbindings.ErrCodeStreamError, Message: err.Error()})
			return
		}
		if flags&connectFlagEndStream != 0 {
			// End-stream envelope: trailing metadata, then clean close or
			// terminal error.
			var endStream connectEndStream
			if len(payload) > 0 {
				_ = json.Unmarshal(payload, &endStream)
			}
			if len(endStream.Metadata) > 0 {
				inv.SetTrailer(openbindings.Metadata(endStream.Metadata))
			}
			if endStream.Error != nil {
				msg := endStream.Error.Message
				if msg == "" {
					msg = endStream.Error.Code
				}
				inv.FireError(&openbindings.InvocationError{
					Code:    connectCodeToErrCode(endStream.Error.Code),
					Message: msg,
				})
				return
			}
			inv.CloseOutput()
			return
		}
		// Data envelope: decode payload as JSON and emit.
		var data any
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &data); err != nil {
				inv.FireError(&openbindings.InvocationError{
					Code:    openbindings.ErrCodeResponseError,
					Message: fmt.Sprintf("decode envelope payload: %v", err),
				})
				return
			}
		}
		if err := inv.EmitOutput(data); err != nil {
			return // terminated while emitting; stop reading
		}
	}
}
