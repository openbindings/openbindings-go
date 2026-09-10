package invoke

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestTypedGenericBridgePreservesNumbers(t *testing.T) {
	for _, input := range []any{json.Number("9007199254740993"), map[string]any{"nested": json.Number("1e400")}, struct {
		ID int64 `json:"id"`
	}{9223372036854775807}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		inner := NewInvocationImpl[any, any](ctx)
		call := NewTypedInvocation[any, any](inner)
		if err := call.Write(ctx, input); err != nil {
			cancel()
			t.Fatal(err)
		}
		got, err := inner.ReadInput(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		wantBytes, _ := json.Marshal(input)
		gotBytes, _ := json.Marshal(got)
		if string(wantBytes) != string(gotBytes) {
			t.Fatalf("changed %s to %s", wantBytes, gotBytes)
		}
		cancel()
	}
}

func TestLocalGenericBridgePreservesNumbers(t *testing.T) {
	v, ok := localGenericValue(struct {
		ID uint64 `json:"id"`
	}{18446744073709551615})
	if !ok {
		t.Fatal("rejected typed number")
	}
	if v.(map[string]any)["id"] != json.Number("18446744073709551615") {
		t.Fatalf("lost number: %#v", v)
	}
	if ValidInvocationData(json.Number("")) {
		t.Fatal("accepted empty numeric token")
	}
}

func TestGenericBridgesRejectInvalidNumberCarriers(t *testing.T) {
	for _, input := range []any{json.Number(""), json.Number("01"), []any{json.Number("")}, map[string]any{"typed": int64(7), "nested": []any{json.Number("")}}, struct {
		Number json.Number `json:"number"`
	}{json.Number("")}} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		inner := NewInvocationImpl[any, any](ctx)
		call := NewTypedInvocation[any, any](inner)
		if err := call.Write(ctx, input); err == nil {
			t.Errorf("typed bridge accepted invalid carrier: %#v", input)
		}
		if value, ok := localGenericValue(input); ok {
			t.Errorf("local bridge accepted invalid carrier: %#v -> %#v", input, value)
		}
		cancel()
	}
}
