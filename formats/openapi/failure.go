package openapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	openbindings "github.com/openbindings/openbindings-go"
)

// FailureEvidence is optional source-native diagnostic evidence retained when
// an OpenAPI invocation completes unsuccessfully. It is never an operation
// output or an ordinary-caller dependency.
type FailureEvidence struct {
	HTTPResponse HTTPResponseEvidence
	OpenAPI      FailureDeclaration
}

// HTTPResponseEvidence is the HTTP response information available at the
// binding boundary. BodyCaptured distinguishes an exact empty body from a body
// that the invocation did not capture.
type HTTPResponseEvidence struct {
	Status       int
	StatusText   string
	URL          string
	Headers      map[string][]string
	Body         []byte
	BodyCaptured bool
}

// FailureDeclaration identifies the OpenAPI Response Object that governed the
// native status. GoverningMedia is present only when the actual Content-Type
// selects one concrete declaration successfully.
type FailureDeclaration struct {
	Declared       bool
	ResponseKey    string
	GoverningMedia string
}

// FailureEvidenceFrom extracts and validates the OpenAPI-native evidence from
// an invocation error. It accepts diagnostics that crossed a JSON invoker frame as
// well as the in-process map produced by this package.
func FailureEvidenceFrom(err error) (FailureEvidence, bool) {
	var invocationError *openbindings.InvocationError
	if !errors.As(err, &invocationError) || invocationError == nil || invocationError.Diagnostics == nil {
		return FailureEvidence{}, false
	}
	raw, marshalErr := json.Marshal(invocationError.Diagnostics)
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
			} `json:"body"`
		} `json:"httpResponse"`
		OpenAPI *struct {
			Declared       bool   `json:"declared"`
			ResponseKey    string `json:"responseKey"`
			GoverningMedia string `json:"governingMedia"`
		} `json:"openapi"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.HTTPResponse == nil || wire.OpenAPI == nil {
		return FailureEvidence{}, false
	}

	evidence := FailureEvidence{
		HTTPResponse: HTTPResponseEvidence{
			Status:     wire.HTTPResponse.Status,
			StatusText: wire.HTTPResponse.StatusText,
			URL:        wire.HTTPResponse.URL,
			Headers:    wire.HTTPResponse.Headers,
		},
		OpenAPI: FailureDeclaration{
			Declared:       wire.OpenAPI.Declared,
			ResponseKey:    wire.OpenAPI.ResponseKey,
			GoverningMedia: wire.OpenAPI.GoverningMedia,
		},
	}
	if evidence.HTTPResponse.Headers == nil {
		evidence.HTTPResponse.Headers = map[string][]string{}
	}
	if wire.HTTPResponse.Body != nil {
		body, decodeErr := base64.StdEncoding.DecodeString(wire.HTTPResponse.Body.Base64)
		if decodeErr != nil || len(body) != wire.HTTPResponse.Body.ByteLength {
			return FailureEvidence{}, false
		}
		evidence.HTTPResponse.Body = body
		evidence.HTTPResponse.BodyCaptured = true
	}
	return evidence, true
}
