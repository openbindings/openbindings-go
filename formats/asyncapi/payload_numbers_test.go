package asyncapi_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/openbindings/openbindings-go/formats/asyncapi"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

func TestP03BuiltinPayloadNumbers(t *testing.T) {
	decode, _ := asyncapi.NewInvoker().BuiltinHooks()
	for _, token := range []string{"9007199254740993", "-9007199254740993", "1e400", "1e-400", "0.12345678901234567890123456789", "42", "-0", `{"a":[1e400,1e-400],"n":9007199254740993}`, "null", "true", `"text"`} {
		for _, ct := range []string{"application/json", "application/problem+json"} {
			t.Run(ct+"/"+token, func(t *testing.T) {
				got, err := decode(invoke.InvokeSite{}, invoke.RawResult{Body: []byte(" \n" + token + "\t"), Meta: invoke.Metadata{"content-type": {ct}}})
				if err != nil {
					t.Fatalf("P03_EXACT: valid payload refused: %v", err)
				}
				var want any
				if err = jsonvalue.Unmarshal([]byte(token), &want); err != nil {
					t.Fatal(err)
				}
				equal, err := jsonvalue.Equal(got, want)
				if err != nil || !equal {
					t.Fatalf("P03_EXACT: %s became %#v (%T): %v", token, got, got, err)
				}
				encoded, err := jsonvalue.Marshal(got)
				if err != nil || string(encoded) != token {
					t.Fatalf("P03_SERIALIZE: %s became %s: %v", token, encoded, err)
				}
			})
		}
	}
	for _, body := range []string{"1 2", "1x", "1e", "[1,]", "NaN", "Infinity", " "} {
		t.Run("invalid/"+body, func(t *testing.T) {
			_, err := decode(invoke.InvokeSite{}, invoke.RawResult{Body: []byte(body), Meta: invoke.Metadata{"content-type": {"application/json"}}})
			var failure *invoke.InvocationError
			if !errors.As(err, &failure) || failure.Code != invoke.ErrCodeResponseError {
				t.Fatalf("P03_INVALID: %#v", err)
			}
		})
	}
	got, err := decode(invoke.InvokeSite{}, invoke.RawResult{Body: []byte("1e400"), Meta: invoke.Metadata{"content-type": {"text/plain"}}})
	if err != nil || got != "1e400" {
		t.Fatalf("text: %#v %v", got, err)
	}
	got, err = decode(invoke.InvokeSite{}, invoke.RawResult{Meta: invoke.Metadata{"content-type": {"application/json"}}})
	if err != nil || got != nil {
		t.Fatalf("empty: %#v %v", got, err)
	}
	// The selected representation is the existing standard JSON number carrier.
	got, err = decode(invoke.InvokeSite{}, invoke.RawResult{Body: []byte("9007199254740993"), Meta: invoke.Metadata{"content-type": {"application/json"}}})
	if err != nil || got != json.Number("9007199254740993") {
		t.Fatalf("P03_TYPE: %#v %v", got, err)
	}
}

func TestP03BuiltinPreservesExistingStringAndDuplicateBehavior(t *testing.T) {
	decode, _ := asyncapi.NewInvoker().BuiltinHooks()
	for _, tc := range []struct{ body, want string }{{`"\ud800"`, `"�"`}, {`{"x":1,"x":2}`, `{"x":2}`}} {
		got, err := decode(invoke.InvokeSite{}, invoke.RawResult{Body: []byte(tc.body), Meta: invoke.Metadata{"content-type": {"application/json"}}})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(got)
		if err != nil || string(encoded) != tc.want {
			t.Fatalf("unrelated payload policy changed: %s, %v", encoded, err)
		}
	}
}
