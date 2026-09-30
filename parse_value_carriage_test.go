package openbindings

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestPublicDocumentReadersRetainNumbers(t *testing.T) {
	for _, token := range []string{"9007199254740993", "0.10000000000000000001", "1e400", "1e-400"} {
		validate := func(data []byte) (*Interface, error) {
			iface, _, err := ValidateDocument(data)
			return iface, err
		}
		for name, read := range map[string]func([]byte) (*Interface, error){"parse": ParseDocument, "validate": validate} {
			t.Run(name+"/"+token, func(t *testing.T) {
				raw := []byte(fmt.Sprintf(`{"openbindings":"0.2.0","operations":{"test":{"input":{"type":"number","minimum":%s},"examples":{"exact":{"input":%s}}}},"x-exact":%s}`, token, token, token))
				iface, err := read(raw)
				if err != nil {
					t.Fatal(err)
				}
				op := iface.Operations["test"]
				if op.Input.(map[string]any)["minimum"] != json.Number(token) || string(op.Examples["exact"].Input) != token {
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

func TestPublicValueValidationDistinguishesAdjacentExactBounds(t *testing.T) {
	for _, tc := range []struct {
		bound, input string
		verdict      string
	}{
		{"9007199254740993", "9007199254740992", "mismatch"},
		{"9007199254740993", "9007199254740993", "valid"},
		{"0.10000000000000000002", "0.10000000000000000001", "mismatch"},
		{"0.10000000000000000002", "0.10000000000000000002", "valid"},
	} {
		document := fmt.Sprintf(`{"openbindings":"0.2.0","operations":{"test":{"input":{"minimum":%s}}}}`, tc.bound)
		if got := inputVerdict(t, document, "test", json.Number(tc.input)); got != tc.verdict {
			t.Fatalf("bound=%s input=%s: %s, want %s", tc.bound, tc.input, got, tc.verdict)
		}
	}
}
