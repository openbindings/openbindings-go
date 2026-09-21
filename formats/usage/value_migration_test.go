package usage

import (
	"encoding/json"
	"testing"
)

func TestCountFlagsAcceptLogicalIntegerTokens(t *testing.T) {
	for _, token := range []string{"2", "2.0", "2e0", "0.2e1"} {
		got, err := formatFlagWithDef("v", json.Number(token), Flag{Count: true})
		if err != nil || len(got) != 2 || got[0] != "-v" || got[1] != "-v" {
			t.Fatalf("%s: %v %v", token, got, err)
		}
	}
	for _, token := range []string{"2.1", "9223372036854775808", "1e10001", ""} {
		if got, err := formatFlagWithDef("v", json.Number(token), Flag{Count: true}); err == nil {
			t.Fatalf("%s accepted: %v", token, got)
		}
	}
}
