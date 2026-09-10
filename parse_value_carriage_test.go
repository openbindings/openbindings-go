package openbindings

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestPublicDocumentReadersRetainNumbers(t *testing.T) {
	for _, token := range []string{"9007199254740993", "0.10000000000000000001", "1e400", "1e-400"} {
		for name, read := range map[string]func([]byte) (*Interface, error){"parse": ParseDocument, "validate": ValidateDocument} {
			t.Run(name+"/"+token, func(t *testing.T) {
				raw := []byte(fmt.Sprintf(`{"openbindings":"0.2.0","operations":{"test":{"input":{"type":"number","minimum":%s},"examples":{"exact":{"input":%s}}}},"x-exact":%s}`, token, token, token))
				iface, err := read(raw)
				if err != nil {
					t.Fatal(err)
				}
				op := iface.Operations["test"]
				if op.Input.(map[string]any)["minimum"] != json.Number(token) || op.Examples["exact"].Input != json.Number(token) {
					t.Fatalf("numeric fields changed: %#v", op)
				}
				output, err := json.Marshal(iface)
				if err != nil {
					t.Fatal(err)
				}
				var members map[string]json.RawMessage
				if err := json.Unmarshal(output, &members); err != nil {
					t.Fatal(err)
				}
				if string(members["x-exact"]) != token {
					t.Fatalf("extension changed: %s", output)
				}
			})
		}
	}
}

func TestPublicDocumentValidationDistinguishesAdjacentExactBounds(t *testing.T) {
	for _, tc := range []struct {
		bound, input string
		valid        bool
	}{
		{"9007199254740993", "9007199254740992", false},
		{"9007199254740993", "9007199254740993", true},
		{"0.10000000000000000002", "0.10000000000000000001", false},
		{"0.10000000000000000002", "0.10000000000000000002", true},
	} {
		raw := []byte(fmt.Sprintf(`{"openbindings":"0.2.0","operations":{"test":{"input":{"minimum":%s},"examples":{"test":{"input":%s}}}}}`, tc.bound, tc.input))
		if _, err := ValidateDocument(raw); (err == nil) != tc.valid {
			t.Fatalf("bound=%s input=%s expectedValid=%v err=%v", tc.bound, tc.input, tc.valid, err)
		}
	}
}
