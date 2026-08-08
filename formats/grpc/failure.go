package grpc

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc/codes"
)

// FailureEvidence is the source-native final status preserved when a gRPC
// invocation completes unsuccessfully. Response messages emitted before the
// status remain outputs; this evidence is optional diagnostics.
type FailureEvidence struct {
	Code    codes.Code
	Message string
	Details []StatusDetailEvidence
}

// StatusDetailEvidence is one google.protobuf.Any value from the native
// google.rpc.Status details list. Value contains the exact Any payload bytes.
type StatusDetailEvidence struct {
	TypeURL string
	Value   []byte
}

// FailureEvidenceFrom extracts and validates gRPC-native status evidence from
// an invocation error. It accepts details produced in process or round-tripped
// through a JSON invoker frame.
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
		GRPCStatus *struct {
			Code    int32  `json:"code"`
			Message string `json:"message"`
			Details []struct {
				TypeURL     string `json:"typeUrl"`
				ValueBase64 string `json:"valueBase64"`
			} `json:"details"`
		} `json:"grpcStatus"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.GRPCStatus == nil || wire.GRPCStatus.Code < 0 || wire.GRPCStatus.Code > 16 {
		return FailureEvidence{}, false
	}
	evidence := FailureEvidence{
		Code:    codes.Code(wire.GRPCStatus.Code),
		Message: wire.GRPCStatus.Message,
		Details: make([]StatusDetailEvidence, 0, len(wire.GRPCStatus.Details)),
	}
	for _, detail := range wire.GRPCStatus.Details {
		value, decodeErr := base64.StdEncoding.DecodeString(detail.ValueBase64)
		if decodeErr != nil {
			return FailureEvidence{}, false
		}
		evidence.Details = append(evidence.Details, StatusDetailEvidence{
			TypeURL: detail.TypeURL,
			Value:   value,
		})
	}
	return evidence, true
}
