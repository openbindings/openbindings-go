package openbindings

import (
	"testing"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// The stronger official carriage gate includes schema and typed SDK boundaries.
func TestStringArchitectureObservation(t *testing.T) {
	var value any
	if err := jsonvalue.Unmarshal([]byte(`"\ud800"`), &value); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"type":"string"}`, `{"const":"\ud800"}`, `{"type":"string","maxLength":1}`} {
		var schema any
		if err := jsonvalue.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatal(err)
		}
		if err := ValidateAgainstSchema(value, schema, nil); err != nil {
			t.Errorf("string schema boundary %s: %v", raw, err)
		}
	}
	var typed struct {
		Value any `json:"value"`
	}
	if err := jsonvalue.Unmarshal([]byte(`{"value":"\ud800"}`), &typed); err != nil {
		t.Fatal(err)
	}
	same, err := jsonvalue.Equal(value, typed.Value)
	if err != nil || !same {
		t.Errorf("typed string boundary changed the value: equal=%v error=%v", same, err)
	}
}
