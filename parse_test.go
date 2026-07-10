package openbindings

import (
	"fmt"
	"strings"
	"testing"
)

// TestParseDocument_VersionRefusalBothDirections pins OBI-T-04 on the PARSE
// path (not only Interface.Validate): downward refusal and undeclared
// prereleases refuse at every entry point, with the messages Validate uses.
func TestParseDocument_VersionRefusalBothDirections(t *testing.T) {
	doc := func(v string) []byte {
		return []byte(fmt.Sprintf(`{"openbindings": %q, "operations": {}}`, v))
	}
	if _, err := ParseDocument(doc("0.1.0")); err == nil || !strings.Contains(err.Error(), "below this SDK's MinSupportedVersion") {
		t.Fatalf("0.1.0 should refuse downward on parse, got %v", err)
	}
	if _, err := ParseDocument(doc("0.2.0-rc.1")); err == nil || !strings.Contains(err.Error(), "pre-release this SDK does not declare support for") {
		t.Fatalf("prerelease should refuse on parse, got %v", err)
	}
	if _, err := ParseDocument(doc("0.2.9")); err != nil {
		t.Fatalf("higher patch must parse, got %v", err)
	}
}
