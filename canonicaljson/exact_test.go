package canonicaljson

import (
	"errors"
	"testing"
)

func TestMarshal_NumbersMustBeExactlyBinary64(t *testing.T) {
	ok := map[string]string{
		`{"n":0.1}`:                   `{"n":0.1}`,
		`{"n":1.10}`:                  `{"n":1.1}`,
		`{"n":1e2}`:                   `{"n":100}`,
		`{"n":9007199254740992}`:      `{"n":9007199254740992}`,
		`{"n":0.30000000000000004}`:   `{"n":0.30000000000000004}`,
		`{"n":-0}`:                    `{"n":0}`,
		`{"n":1e21}`:                  `{"n":1e+21}`,
		`{"n":123456789012345680000}`: `{"n":123456789012345680000}`,
	}
	for in, want := range ok {
		out, err := Marshal([]byte(in))
		if err != nil {
			t.Fatalf("%s: unexpected error %v", in, err)
		}
		if string(out) != want {
			t.Fatalf("%s: got %s want %s", in, out, want)
		}
	}
	refused := []string{
		`{"n":9007199254740993}`,
		`{"n":1e400}`,
		`{"n":0.1000000000000000055511151231257827}`,
		`{"n":123456789012345678901}`,
	}
	for _, in := range refused {
		_, err := Marshal([]byte(in))
		var nr *NumberNotRepresentableError
		if !errors.As(err, &nr) {
			t.Fatalf("%s: want NumberNotRepresentableError, got %v", in, err)
		}
	}
}
