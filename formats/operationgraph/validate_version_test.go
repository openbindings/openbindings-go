package operationgraph

import (
	"strings"
	"testing"
)

// TestCheckVersion_OGT02 pins the full OG-T-02 mirror of OBI-T-04: upward
// refusal (higher major; higher minor pre-1.0), downward refusal (below the
// supported minimum), and prerelease refusal absent declared support.
func TestCheckVersion_OGT02(t *testing.T) {
	cases := []struct {
		version string
		want    string // substring of the refusal, "" = accepted
	}{
		{"0.2.0", ""},
		{"0.2.9", ""}, // higher patch within the supported minor is never a refusal
		{"0.3.0", "supports up to"},
		{"1.0.0", "supports up to"},
		{"0.1.0", "supports no lower than"},
		{"0.2.0-beta.1", "prerelease"},
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
