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

	"github.com/openbindings/openbindings-go/invoke"
)

// Connect streaming wire format constants.
//
// Framing is the Connect protocol's JSON codec, incorporated (CONN-P-05):
// streaming RPCs use an envelope-framed format. Each envelope is a 5-byte
// header followed by a payload:
//
//	+-------+----------+-----------+
//	| flags | length   | payload   |
//	| 1B    | 4B BE    | length B  |
//	+-------+----------+-----------+
//
// The flags byte is a bitfield. Two bits are defined:
//   - Bit 0 (0x01): COMPRESSED — payload is compressed. This processor
//     advertises only the encodings it implements (CONN-P-05), which is
//     identity alone, so a compressed envelope is a negotiation violation.
//   - Bit 1 (0x02): END_STREAM — this envelope terminates the stream.
//
// A server-streaming response is a sequence of zero or more data envelopes
// (flags = 0) followed by exactly one end-stream envelope (flags =
// END_STREAM) — the protocol closes EVERY stream with one. The end-stream
// payload is a JSON object with optional `error` and `metadata` fields; an
// `error` member classifies the invocation as a failure (CONN-P-06).
const (
	connectFlagCompressed = 0x01
	connectFlagEndStream  = 0x02

	// streamingContentType is the Content-Type for Connect streaming with
	// the JSON codec.
	streamingContentType = "application/connect+json"
)

// writeConnectEnvelope writes a single Connect envelope (5-byte header +
// payload) to w. flags should be 0 for a normal data frame or
// connectFlagEndStream for an end-stream frame. This processor sends
// identity-encoded frames only (it advertises no other encoding).
func writeConnectEnvelope(w io.Writer, flags byte, payload []byte) error {
	if flags&connectFlagCompressed != 0 {
		return fmt.Errorf("connect: compression is not implemented; this processor sends identity-encoded envelopes only")
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

// readConnectEnvelope reads one Connect envelope from r and returns its
// flags and payload. Returns io.EOF if the reader is exhausted before any
// header bytes are read. Refuses payloads larger than maxPayload bytes —
// a per-envelope cap that is implementation policy (§2), not a spec rule.
// A COMPRESSED envelope is refused: this processor advertises identity
// only (CONN-P-05), so receiving one is the server violating the
// protocol's compression negotiation.
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
		return 0, nil, fmt.Errorf("connect: received a compressed envelope despite identity-only encoding negotiation (this processor advertises only the encodings it implements, openbindings.connect@1 §8 / CONN-P-05)")
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
	Error    map[string]any      `json:"error,omitempty"`
	Metadata map[string][]string `json:"metadata,omitempty"`
}

// runStreaming sends a server-streaming dispatch — a POST carrying one
// enveloped request message (CONN-P-05; the GET lane is excluded from
// revision 1, §2) — and drives the handle: leading headers from the HTTP
// response, one EmitOutput per data envelope as frames arrive, trailing
// metadata from the end-stream envelope, then CloseOutput (clean end) or
// FireError.
//
// Classification is protocol-native (CONN-P-06): a streaming invocation
// succeeds IFF the stream rode HTTP 200 AND its END_STREAM envelope
// carries no error member. Output values already emitted STAND; a late
// failure classifies the invocation as a failure without retracting them.
//
// Streaming dispatch is schema-mode only: descriptorless mode is
// unary-only by definition (CONN-P-04), so mi is always non-nil here. ctx
// is already bound to the invocation's lifetime (DoneContext), so caller
// Cancel() tears down the in-flight request.
func (e *Invoker) runStreaming(ctx context.Context, inv invoke.BindingHandle[any, any], reqURL string, msgBytes []byte, headers map[string]string, mi *methodInfo, maxUnit int64) {
	// Build the framed request body: a single envelope with flags=0 carrying
	// the request message, immediately followed by EOF (no end-stream envelope
	// is required from the client side for server-streaming RPCs).
	var body bytes.Buffer
	if err := writeConnectEnvelope(&body, 0, msgBytes); err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeExecutionFailed})
		return
	}
	e.runStreamingBody(ctx, inv, reqURL, &body, headers, mi, maxUnit)
}

// runClientStreaming preserves the schema-declared client-streaming or
// bidirectional shape. Request messages are encoded as they arrive and the
// HTTP exchange begins immediately; response envelopes are consumed
// concurrently, so bidirectional outputs may arrive while input remains open.
func (e *Invoker) runClientStreaming(ctx context.Context, inv invoke.BindingHandle[any, any], reqURL string, headers map[string]string, mi *methodInfo, maxUnit int64) {
	reader, writer := io.Pipe()
	go func() {
		defer func() { _ = writer.Close() }()
		for {
			value, err := inv.ReadInput(ctx)
			if err == io.EOF {
				if werr := writeConnectEnvelope(writer, connectFlagEndStream, []byte("{}")); werr != nil && ctx.Err() == nil {
					inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeStreamError})
				}
				return
			}
			if err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			payload, ierr := buildSchemaModeBody(mi, value)
			if ierr != nil {
				inv.FireError(ierr)
				_ = writer.CloseWithError(ierr)
				return
			}
			if err := writeConnectEnvelope(writer, 0, payload); err != nil {
				if ctx.Err() == nil {
					inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeStreamError})
				}
				return
			}
		}
	}()

	e.runStreamingBody(ctx, inv, reqURL, reader, headers, mi, maxUnit)
	_ = reader.Close()
}

// runStreamingBody performs one Connect streaming exchange over an already
// prepared request body. body may be a live pipe for client/bidirectional
// streaming or a finite buffer for server streaming.
func (e *Invoker) runStreamingBody(ctx context.Context, inv invoke.BindingHandle[any, any], reqURL string, body io.Reader, headers map[string]string, mi *methodInfo, maxUnit int64) {

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, body)
	if err != nil {
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeExecutionFailed})
		return
	}
	// Connect protocol headers. Connect-Protocol-Version: 1 rides EVERY
	// request (CONN-P-05), and the processor advertises only the encodings
	// it implements — identity alone — via Connect-Accept-Encoding.
	req.Header.Set("Content-Type", streamingContentType)
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Accept-Encoding", "identity")
	req.Header.Set("Accept", streamingContentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // cancelled; the handle is already terminal
		}
		inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeConnectFailed})
		return
	}
	defer resp.Body.Close()

	// CONN-P-06: a streaming invocation succeeds only if the stream rode
	// HTTP 200, as the protocol requires. Connect streaming reports its own
	// errors in the END_STREAM envelope, but a proxy or middleware can
	// still answer at the HTTP layer.
	if resp.StatusCode != http.StatusOK {
		// Deliberately fixed at maxResponseBytes: an implementation resource
		// guard on the error path, not a delivery unit —
		// BindingInvocationArgs.MaxDeliveryUnitBytes does not apply here.
		// Read up to the cap PLUS ONE byte: LimitReader(n) yields at most n
		// bytes, so bounding at maxResponseBytes truncates an error body of
		// exactly the cap by one byte, which can break the Connect error
		// JSON that applyConnectError parses. +1 lets a cap-sized error
		// payload be read whole.
		// Preserve the historical cap-plus-one boundary (the final byte can
		// complete a cap-sized JSON value), and read one further sentinel so
		// the implementation can detect truncation. The body does not cross
		// the abstract invocation boundary.
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+2))
		truncated := int64(len(bodyBytes)) > maxResponseBytes+1
		if truncated {
			bodyBytes = bodyBytes[:maxResponseBytes+1]
		}

		inv.FireError(connectHTTPError(resp, bodyBytes, truncated))
		return
	}

	// Streaming responses ride the JSON codec's enveloped content type
	// (CONN-P-05); anything else is a loud protocol error.
	if gotCT := resp.Header.Get("Content-Type"); !isStreamingJSONContentType(gotCT) {

		inv.FireError(&invoke.InvocationError{
			Code: invoke.ErrCodeResponseError,
		})
		return
	}

	// Leading metadata precedes the first emit.

	// readConnectEnvelope enforces a per-envelope cap rather than bounding
	// the total stream size: a long-running subscription may legitimately
	// produce more than one delivery unit's bytes in total. One envelope is
	// one delivery unit, consumer-bounded via
	// BindingInvocationArgs.MaxDeliveryUnitBytes. (Implementation policy, §2.)
	for {
		flags, payload, err := readConnectEnvelope(resp.Body, maxUnit)
		if err == io.EOF {
			// The protocol closes EVERY stream with an END_STREAM envelope
			// (CONN-P-05); success requires an error-free END_STREAM
			// (CONN-P-06), so a stream that ends without one cannot be
			// classified a success. Loud failure; emitted values stand.
			inv.FireError(&invoke.InvocationError{
				Code: invoke.ErrCodeStreamError,
			})
			return
		}
		if err != nil {
			if ctx.Err() != nil {
				return // cancelled; the handle is already terminal
			}
			inv.FireError(&invoke.InvocationError{Code: invoke.ErrCodeStreamError})
			return
		}
		if flags&connectFlagEndStream != 0 {
			// The peer has ended the RPC. Stop accepting client-streaming input;
			// outputs already emitted remain authoritative.
			_ = inv.CloseInput()
			// End-stream envelope: trailing metadata, then clean close or
			// terminal error. An empty payload carries nothing and is read
			// as the empty object.
			var endStream connectEndStream
			if len(payload) > 0 {
				if uerr := json.Unmarshal(payload, &endStream); uerr != nil {
					// Loud, never a silent drop: an unreadable END_STREAM
					// cannot prove the stream error-free (CONN-P-06).
					inv.FireError(&invoke.InvocationError{
						Code: invoke.ErrCodeStreamError,
					})
					return
				}
			}
			// Streaming trailing metadata rides the END_STREAM envelope but
			// has no representation on the abstract invocation.
			if endStream.Error != nil {
				inv.FireError(&invoke.InvocationError{
					Code: invoke.ErrCodeExecutionFailed,
				})
				return
			}
			inv.CloseOutput()
			return
		}
		// Data envelope: the output value is the response message rendered
		// by the canonical JSON mapping (CONN-P-02); a frame that fails to
		// unmarshal against its descriptor is a loud failure outcome.
		out, derr := decodeSchemaModeOutput(mi, payload)
		if derr != nil {
			inv.FireError(derr)
			return
		}
		if err := inv.EmitOutput(out); err != nil {
			return // terminated while emitting; stop reading
		}
	}
}

// isStreamingJSONContentType reports whether a streaming response
// Content-Type is the JSON codec's application/connect+json (media-type
// parameters tolerated).
func isStreamingJSONContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	return ct == streamingContentType || strings.HasPrefix(ct, streamingContentType+";")
}
