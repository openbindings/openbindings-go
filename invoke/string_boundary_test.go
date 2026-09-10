package invoke

import (
	"encoding/json"
	"testing"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

func TestInvocationErrorStringCarriage(t *testing.T) {
	var value any
	if err := jsonvalue.Unmarshal([]byte(`{"\ud800":"\udc00","n":9007199254740993}`), &value); err != nil {
		t.Fatal(err)
	}
	failure := NewInvocationErrorWithData("APPLICATION_FAILURE", value)
	if !failure.HasData() {
		t.Fatalf("lost error data: %#v", failure)
	}
	encoded, err := jsonvalue.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	var restored any
	if err := jsonvalue.Unmarshal(fields["data"], &restored); err != nil {
		t.Fatal(err)
	}
	if same, err := jsonvalue.Equal(restored, value); err != nil || !same {
		t.Fatalf("changed error data: %s (%v)", encoded, err)
	}
}
