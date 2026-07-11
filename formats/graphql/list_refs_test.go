package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// TestInspectSource_OperationKey verifies InspectSource suggests the same
// operationKey SynthesizeInterface would assign, so an inspection previews
// exactly what synthesis names (B3.5 format-parity batch: TS reportedly
// omitted this field entirely).
func TestInspectSource_OperationKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"__schema": testSchema},
		})
	}))
	defer srv.Close()

	insp, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{Location: srv.URL})
	if err != nil {
		t.Fatalf("InspectSource: %v", err)
	}

	byRef := make(map[string]openbindings.BindableTarget, len(insp.Targets))
	for _, tgt := range insp.Targets {
		byRef[tgt.Ref] = tgt
	}

	userTarget, ok := byRef["Query/user"]
	if !ok {
		t.Fatal("expected a Query/user target")
	}
	if userTarget.OperationKey != "user" {
		t.Errorf("Query/user operationKey = %q, want %q", userTarget.OperationKey, "user")
	}

	mutationTarget, ok := byRef["Mutation/createUser"]
	if !ok {
		t.Fatal("expected a Mutation/createUser target")
	}
	if mutationTarget.OperationKey != "createUser" {
		t.Errorf("Mutation/createUser operationKey = %q, want %q", mutationTarget.OperationKey, "createUser")
	}
}
