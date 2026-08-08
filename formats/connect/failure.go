package connect

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	openbindings "github.com/openbindings/openbindings-go"
)

// FailureEvidence is the source-native evidence from an unsuccessful Connect
// invocation. Exactly one or both transport and END_STREAM evidence lanes may
// be present, depending on where the protocol classified the failure.
type FailureEvidence struct {
	HTTPResponse *HTTPFailureEvidence
	Error        map[string]any
	EndStream    *EndStreamFailureEvidence
}

// HTTPFailureEvidence preserves a non-200 Connect response. Body is exact
// unless Truncated is true, in which case it is the explicitly marked prefix
// retained under the implementation's diagnostics bound.
type HTTPFailureEvidence struct {
	Status     int
	StatusText string
	URL        string
	Headers    map[string][]string
	Body       []byte
	Truncated  bool
}

// EndStreamFailureEvidence preserves the exact END_STREAM JSON payload and
// its native Connect error object.
type EndStreamFailureEvidence struct {
	Error   map[string]any
	Payload []byte
}

// FailureEvidenceFrom extracts and validates Connect-native failure evidence
// from an in-process or invoker-frame-round-tripped InvocationError.
func FailureEvidenceFrom(err error) (FailureEvidence, bool) {
	var invocationError *openbindings.InvocationError
	if !errors.As(err, &invocationError) || invocationError == nil || invocationError.Details == nil {
		return FailureEvidence{}, false
	}
	raw, marshalErr := json.Marshal(invocationError.Details)
	if marshalErr != nil {
		return FailureEvidence{}, false
	}
	var wire struct {
		HTTPResponse *struct {
			Status     int                 `json:"status"`
			StatusText string              `json:"statusText"`
			URL        string              `json:"url"`
			Headers    map[string][]string `json:"headers"`
			Body       *capturedBytesWire  `json:"body"`
		} `json:"httpResponse"`
		Connect *struct {
			Error     map[string]any `json:"error"`
			EndStream *struct {
				Error   map[string]any     `json:"error"`
				Payload *capturedBytesWire `json:"payload"`
			} `json:"endStream"`
		} `json:"connect"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return FailureEvidence{}, false
	}
	evidence := FailureEvidence{}
	if wire.HTTPResponse != nil && wire.HTTPResponse.Body != nil {
		body, ok := decodeCapturedBytes(wire.HTTPResponse.Body)
		if !ok {
			return FailureEvidence{}, false
		}
		headers := wire.HTTPResponse.Headers
		if headers == nil {
			headers = map[string][]string{}
		}
		evidence.HTTPResponse = &HTTPFailureEvidence{
			Status: wire.HTTPResponse.Status, StatusText: wire.HTTPResponse.StatusText,
			URL: wire.HTTPResponse.URL, Headers: headers, Body: body,
			Truncated: wire.HTTPResponse.Body.Truncated,
		}
	}
	if wire.Connect != nil {
		evidence.Error = wire.Connect.Error
		if wire.Connect.EndStream != nil && wire.Connect.EndStream.Payload != nil {
			payload, ok := decodeCapturedBytes(wire.Connect.EndStream.Payload)
			if !ok {
				return FailureEvidence{}, false
			}
			evidence.EndStream = &EndStreamFailureEvidence{
				Error: wire.Connect.EndStream.Error, Payload: payload,
			}
		}
	}
	if evidence.HTTPResponse == nil && evidence.EndStream == nil {
		return FailureEvidence{}, false
	}
	return evidence, true
}

type capturedBytesWire struct {
	Base64     string `json:"base64"`
	ByteLength int    `json:"byteLength"`
	Truncated  bool   `json:"truncated"`
}

func decodeCapturedBytes(wire *capturedBytesWire) ([]byte, bool) {
	if wire.ByteLength < 0 {
		return nil, false
	}
	value, err := base64.StdEncoding.DecodeString(wire.Base64)
	return value, err == nil && len(value) == wire.ByteLength
}
