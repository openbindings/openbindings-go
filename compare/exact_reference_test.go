package compare

import (
	"encoding/json"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestCompatibilityReferenceRootPreservesExactValues(t *testing.T) {
	fixture := func(token string) *openbindings.Interface {
		return &openbindings.Interface{
			OpenBindings: "0.2.0", Schemas: map[string]openbindings.JSONSchema{"Value": map[string]any{"const": json.Number(token)}},
			Operations: map[string]openbindings.Operation{"test": {Output: map[string]any{"$ref": "#/schemas/Value"}}},
		}
	}
	for _, pair := range [][2]string{{"9007199254740992", "9007199254740993"}, {"0.10000000000000000", "0.10000000000000001"}, {"1e-400", "0"}} {
		issues := CheckInterfaceCompatibility(fixture(pair[0]), fixture(pair[1]))
		if len(issues) != 1 || issues[0].Kind != CompatibilityOutputIncompatible {
			t.Errorf("%v became compatible: %#v", pair, issues)
		}
	}
	if issues := CheckInterfaceCompatibility(fixture("0.1"), fixture("0.10")); len(issues) != 0 {
		t.Errorf("equal decimal values differ: %#v", issues)
	}
}
