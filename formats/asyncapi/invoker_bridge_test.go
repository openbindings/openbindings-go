package asyncapi

import (
	"encoding/json"
	"errors"
	"testing"

	asyncapiclient "github.com/openbindings/asyncapi-client/go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestBridgeExecutionErrorPreservesAbsentAndExplicitNullData(t *testing.T) {
	tests := []struct {
		name      string
		execution *asyncapiclient.ExecutionError
		wantJSON  string
		wantData  bool
	}{
		{
			name:      "absent",
			execution: &asyncapiclient.ExecutionError{Code: "APPLICATION_FAILURE"},
			wantJSON:  `{"code":"APPLICATION_FAILURE"}`,
		},
		{
			name: "native details are not portable by presence alone",
			execution: &asyncapiclient.ExecutionError{
				Code: "DRIVER_FAILED", Details: map[string]any{"reasonCode": 7},
			},
			wantJSON: `{"code":"DRIVER_FAILED"}`,
		},
		{
			name: "explicit null",
			execution: &asyncapiclient.ExecutionError{
				Code: "APPLICATION_FAILURE", Details: nil, DetailsPresent: true,
			},
			wantJSON: `{"code":"APPLICATION_FAILURE","data":null}`,
			wantData: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bridgeExecutionError(tt.execution)
			var invocation *invoke.InvocationError
			if !errors.As(got, &invocation) {
				t.Fatalf("bridge result = %T, want *InvocationError", got)
			}
			if invocation.HasData() != tt.wantData {
				t.Fatalf("HasData() = %v, want %v", invocation.HasData(), tt.wantData)
			}
			encoded, err := json.Marshal(invocation)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tt.wantJSON {
				t.Fatalf("wire error = %s, want %s", encoded, tt.wantJSON)
			}
		})
	}
}
