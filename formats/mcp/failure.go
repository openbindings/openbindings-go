package mcp

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	openbindings "github.com/openbindings/openbindings-go"
)

// FailureEvidence is the source-native evidence from an unsuccessful MCP
// invocation. A protocol failure carries Result or JSONRPCError; an HTTP
// response may accompany either lane or stand alone.
type FailureEvidence struct {
	Result       map[string]any
	JSONRPCError *JSONRPCFailureEvidence
	HTTPResponse *HTTPFailureEvidence
}

// JSONRPCFailureEvidence preserves the server-authored JSON-RPC error object.
type JSONRPCFailureEvidence struct {
	Code    int
	Message string
	Data    any
}

// HTTPFailureEvidence preserves the exact Streamable HTTP failure response.
type HTTPFailureEvidence struct {
	Status     int
	StatusText string
	URL        string
	Headers    map[string][]string
	Body       []byte
}

// FailureEvidenceFrom extracts and validates MCP-native failure evidence from
// an in-process or invoker-frame-round-tripped InvocationError.
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
		MCP *struct {
			Result       map[string]any `json:"result"`
			JSONRPCError *struct {
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			} `json:"jsonrpcError"`
		} `json:"mcp"`
		HTTPResponse *struct {
			Status     int                 `json:"status"`
			StatusText string              `json:"statusText"`
			URL        string              `json:"url"`
			Headers    map[string][]string `json:"headers"`
			Body       *struct {
				Base64     string `json:"base64"`
				ByteLength int    `json:"byteLength"`
			} `json:"body"`
		} `json:"httpResponse"`
	}
	if json.Unmarshal(raw, &wire) != nil {
		return FailureEvidence{}, false
	}
	evidence := FailureEvidence{}
	if wire.MCP != nil {
		evidence.Result = wire.MCP.Result
		if wire.MCP.JSONRPCError != nil {
			var data any
			if len(wire.MCP.JSONRPCError.Data) > 0 {
				if json.Unmarshal(wire.MCP.JSONRPCError.Data, &data) != nil {
					return FailureEvidence{}, false
				}
			}
			evidence.JSONRPCError = &JSONRPCFailureEvidence{
				Code: wire.MCP.JSONRPCError.Code, Message: wire.MCP.JSONRPCError.Message, Data: data,
			}
		}
	}
	if wire.HTTPResponse != nil {
		if wire.HTTPResponse.Body == nil || wire.HTTPResponse.Body.ByteLength < 0 {
			return FailureEvidence{}, false
		}
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
		}
	}
	if evidence.Result == nil && evidence.JSONRPCError == nil && evidence.HTTPResponse == nil {
		return FailureEvidence{}, false
	}
	return evidence, true
}
