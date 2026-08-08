package grpc

import (
	"encoding/json"
	"errors"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestFailureEvidenceFromInProcessAndFrame(t *testing.T) {
	protoStatus := status.New(codes.Internal, "ledger corrupt").Proto()
	protoStatus.Details = append(protoStatus.Details, &anypb.Any{
		TypeUrl: "type.googleapis.com/demo.Failure",
		Value:   []byte{0x00, 0xff, 0x80, 0x41},
	})
	original := grpcError(status.FromProto(protoStatus).Err(), openbindings.ErrCodeStreamError)

	assertEvidence := func(t *testing.T, err error) {
		t.Helper()
		evidence, ok := FailureEvidenceFrom(err)
		if !ok {
			t.Fatal("gRPC failure evidence not found")
		}
		if evidence.Code != codes.Internal || evidence.Message != "ledger corrupt" {
			t.Errorf("evidence = %+v", evidence)
		}
		if len(evidence.Details) != 1 || evidence.Details[0].TypeURL != "type.googleapis.com/demo.Failure" || string(evidence.Details[0].Value) != string([]byte{0x00, 0xff, 0x80, 0x41}) {
			t.Errorf("details = %+v", evidence.Details)
		}
	}

	assertEvidence(t, original)
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var framed openbindings.InvocationError
	if err := json.Unmarshal(encoded, &framed); err != nil {
		t.Fatal(err)
	}
	assertEvidence(t, &framed)
}

func TestFailureEvidenceFromRejectsLocalAndMalformedEvidence(t *testing.T) {
	if _, ok := FailureEvidenceFrom(errors.New("local")); ok {
		t.Fatal("local error unexpectedly had gRPC evidence")
	}
	malformed := &openbindings.InvocationError{
		Code: openbindings.ErrCodeExecutionFailed,
		Details: map[string]any{
			"grpcStatus": map[string]any{
				"code":    13,
				"message": "bad detail",
				"details": []any{map[string]any{"typeUrl": "demo.Bad", "valueBase64": "%%%"}},
			},
		},
	}
	if _, ok := FailureEvidenceFrom(malformed); ok {
		t.Fatal("malformed base64 unexpectedly accepted")
	}
}
