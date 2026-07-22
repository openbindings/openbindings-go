package operationgraph

import (
	"strings"
	"testing"
)

// TestCheckVersion_OGT02 pins this identifier's exact graph-unit edition.
func TestCheckVersion_OGT02(t *testing.T) {
	cases := []struct {
		version string
		want    string // substring of the refusal, "" = accepted
	}{
		{"0.2.0", ""},
		{"0.2.9", "supports exactly"},
		{"0.3.0", "supports exactly"},
		{"1.0.0", "supports exactly"},
		{"0.1.0", "supports exactly"},
		{"0.2.0-beta.1", "supports exactly"},
	}
	for _, c := range cases {
		err := checkVersion(c.version)
		if c.want == "" {
			if err != nil {
				t.Errorf("checkVersion(%q) = %v, want accept", c.version, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("checkVersion(%q) = %v, want refusal containing %q", c.version, err, c.want)
		}
	}
}
