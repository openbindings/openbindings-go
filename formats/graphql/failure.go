package graphql

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	openbindings "github.com/openbindings/openbindings-go"
)

// FailureEvidence is source-native GraphQL-over-HTTP or
// graphql-transport-ws evidence from an unsuccessful invocation.
type FailureEvidence struct {
	HTTPResponse *HTTPFailureEvidence
	MediaType    string
	TransportWS  *TransportWSFailureEvidence
}

type HTTPFailureEvidence struct {
	Status     int
	StatusText string
	URL        string
	Headers    map[string][]string
	Body       []byte
	Truncated  bool
}

type TransportWSFailureEvidence struct {
	Type     string
	Payload  any
	Code     int
	Reason   string
	WasClean *bool
}

// FailureEvidenceFrom extracts and validates binding-native failure evidence
// after either in-process use or an invoker-frame JSON round trip.
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
			Body       *struct {
				Base64     string `json:"base64"`
				ByteLength int    `json:"byteLength"`
				Truncated  bool   `json:"truncated"`
			} `json:"body"`
		} `json:"httpResponse"`
		GraphQL *struct {
			MediaType string `json:"mediaType"`
		} `json:"graphql"`
		TransportWS *struct {
			Type     string `json:"type"`
			Payload  any    `json:"payload"`
			Code     int    `json:"code"`
			Reason   string `json:"reason"`
			WasClean *bool  `json:"wasClean"`
		} `json:"graphqlTransportWs"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return FailureEvidence{}, false
	}
	evidence := FailureEvidence{}
	if wire.HTTPResponse != nil && wire.HTTPResponse.Body != nil {
		body, decodeErr := base64.StdEncoding.DecodeString(wire.HTTPResponse.Body.Base64)
		if decodeErr != nil || len(body) != wire.HTTPResponse.Body.ByteLength {
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
		if wire.GraphQL != nil {
			evidence.MediaType = wire.GraphQL.MediaType
		}
	}
	if wire.TransportWS != nil {
		if wire.TransportWS.Type != "error" && wire.TransportWS.Type != "close" {
			return FailureEvidence{}, false
		}
		evidence.TransportWS = &TransportWSFailureEvidence{
			Type: wire.TransportWS.Type, Payload: wire.TransportWS.Payload,
			Code: wire.TransportWS.Code, Reason: wire.TransportWS.Reason,
			WasClean: wire.TransportWS.WasClean,
		}
	}
	if evidence.HTTPResponse == nil && evidence.TransportWS == nil {
		return FailureEvidence{}, false
	}
	return evidence, true
}
