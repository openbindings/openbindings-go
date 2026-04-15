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
	"time"

	"github.com/golang/protobuf/jsonpb" //nolint:staticcheck // required by jhump/protoreflect/dynamic
	"github.com/jhump/protoreflect/dynamic" //nolint:staticcheck // no v2 equivalent yet
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

// executeConnectStreaming sends a server-streaming Connect RPC and returns a
// channel that yields one StreamEvent per data envelope received from the
// server. The channel is closed when the end-stream envelope is processed,
// when the underlying connection terminates, or when ctx is cancelled.
//
// The Connect streaming wire format is described at:
// https://connectrpc.com/docs/protocol#streaming-rpcs
//
// This function only supports server-streaming. Client-streaming and
// bidirectional-streaming RPCs are excluded by the OBI execution model in
// v0.1 (one input, stream of outputs).
func executeConnectStreaming(ctx context.Context, client *http.Client, baseURL, svcName, methodName string, input any, headers map[string]string, mi *methodInfo, start time.Time) (<-chan openbindings.StreamEvent, error) {
	connectURL := strings.TrimRight(baseURL, "/") + "/" + svcName + "/" + methodName

	// Marshal the single request message.
	var msgBytes []byte
	if input != nil {
		if mi != nil && mi.method != nil {
			msg := dynamic.NewMessage(mi.method.GetInputType())
			inputMap, ok := input.(map[string]any)
			if !ok {
				return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, fmt.Sprintf("input must be a JSON object, got %T", input))), nil
			}
			jsonBytes, err := json.Marshal(inputMap)
			if err != nil {
				return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())), nil
			}
			if err := msg.UnmarshalJSONPB(&jsonpb.Unmarshaler{AllowUnknownFields: true}, jsonBytes); err != nil {
				return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())), nil
			}
			msgBytes, err = msg.MarshalJSONPB(&jsonpb.Marshaler{OrigName: true})
			if err != nil {
				return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())), nil
			}
		} else {
			var err error
			msgBytes, err = json.Marshal(input)
			if err != nil {
				return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeInvalidInput, err.Error())), nil
			}
		}
	} else {
		msgBytes = []byte("{}")
	}

	// Build the framed request body: a single envelope with flags=0 carrying
	// the request message, immediately followed by EOF (no end-stream envelope
	// is required from the client side for server-streaming RPCs).
	var body bytes.Buffer
	if err := writeConnectEnvelope(&body, 0, msgBytes); err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error())), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, connectURL, &body)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeExecutionFailed, err.Error())), nil
	}
	req.Header.Set("Content-Type", streamingContentType)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Accept", streamingContentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeConnectFailed, err.Error())), nil
	}

	// Check for transport-level auth failures BEFORE streaming begins.
	// Connect streaming returns errors in the end-stream envelope rather
	// than via HTTP status, but a 401/403 from a proxy or middleware can
	// still appear at the HTTP layer.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return openbindings.SingleEventChannel(openbindings.HTTPErrorOutput(start, resp.StatusCode, resp.Status)), nil
	}
	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		out := openbindings.HTTPErrorOutput(start, resp.StatusCode, resp.Status)
		// Try to parse a Connect error envelope from the body.
		if len(bodyBytes) > 0 {
			var connectErr struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal(bodyBytes, &connectErr) == nil && connectErr.Message != "" {
				out.Error.Message = connectErr.Message
			}
		}
		return openbindings.SingleEventChannel(out), nil
	}

	// Streaming responses MUST use the streaming content type.
	gotCT := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(gotCT, "application/connect+") {
		resp.Body.Close()
		return openbindings.SingleEventChannel(openbindings.FailedOutput(start, openbindings.ErrCodeResponseError,
			fmt.Sprintf("expected Content-Type starting with application/connect+, got %q", gotCT))), nil
	}

	ch := make(chan openbindings.StreamEvent, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		// LimitReader at the HTTP body level is too coarse for streaming
		// (we want to bound each individual envelope, not the total stream
		// size, since a long-running subscription may legitimately produce
		// more than maxResponseBytes total). readConnectEnvelope already
		// enforces a per-envelope cap.
		reader := resp.Body

		for {
			if ctx.Err() != nil {
				return
			}
			flags, payload, err := readConnectEnvelope(reader, maxResponseBytes)
			if err == io.EOF {
				// Stream ended without an end-stream envelope. This is a
				// protocol violation, but we treat it as the stream simply
				// finishing rather than an error event, to remain lenient.
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
					Code:    openbindings.ErrCodeStreamError,
					Message: err.Error(),
				}}
				return
			}
			if flags&connectFlagEndStream != 0 {
				// End-stream envelope: parse for error/metadata.
				if len(payload) > 0 {
					var endStream struct {
						Error *struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"error,omitempty"`
					}
					if jerr := json.Unmarshal(payload, &endStream); jerr == nil && endStream.Error != nil {
						ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
							Code:    openbindings.ErrCodeExecutionFailed,
							Message: endStream.Error.Message,
						}}
					}
				}
				return
			}
			// Data envelope: decode payload as JSON and emit.
			var data any
			if len(payload) > 0 {
				if err := json.Unmarshal(payload, &data); err != nil {
					ch <- openbindings.StreamEvent{Error: &openbindings.ExecuteError{
						Code:    openbindings.ErrCodeResponseError,
						Message: fmt.Sprintf("decode envelope payload: %v", err),
					}}
					return
				}
			}
			ch <- openbindings.StreamEvent{Data: data, Status: resp.StatusCode}
		}
	}()
	return ch, nil
}
