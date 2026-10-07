package openbindings

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestParseDocument_VersionRefusalBothDirections pins this SDK's version
// refusal on the PARSE path (not only Document.Validate): downward refusal
// and undeclared prereleases refuse at every entry point, with the messages
// Validate uses.
func TestParseDocument_VersionRefusalBothDirections(t *testing.T) {
	doc := func(v string) []byte {
		return []byte(fmt.Sprintf(`{"openbindings": %q, "operations": {}}`, v))
	}
	if _, err := ParseDocument(doc("0.1.0")); err == nil || !strings.Contains(err.Error(), "older than the release line this implementation supports (0.2.x)") {
		t.Fatalf("0.1.0 should refuse downward on parse, got %v", err)
	}
	if _, err := ParseDocument(doc("0.2.0-rc.1")); err == nil || !strings.Contains(err.Error(), "a pre-release this implementation does not support") {
		t.Fatalf("prerelease should refuse on parse, got %v", err)
	}
	if _, err := ParseDocument(doc("0.2.9")); err != nil {
		t.Fatalf("higher patch must parse, got %v", err)
	}
}

// ParseDocument's error is a version refusal, a violation, or, when this SDK
// cannot read the document in full, inconclusive: exactly one of the three.
func TestParseDocument_ErrorClasses(t *testing.T) {
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	for name, tc := range map[string]struct{ input, want string }{
		"an unsupported version":  {`{"openbindings":"0.3.0","operations":{}}`, "refusal"},
		"an unknown member":       {`{"openbindings":"0.2.0","operations":{},"unknown":1}`, "violation"},
		"not JSON":                {`{"openbindings":`, "violation"},
		"a repeated member name":  {`{"openbindings":"0.2.0","operations":{},"operations":{}}`, "violation"},
		"nested past the decoder": {`{"openbindings":"0.2.0","operations":{},"x-deep":` + deep + `}`, "inconclusive"},
		"a lone surrogate":        {`{"openbindings":"0.2.0","operations":{},"x-note":"\ud800"}`, "inconclusive"},
	} {
		_, err := ParseDocument([]byte(tc.input))
		var classes []string
		if errors.As(err, new(*VersionRefusalError)) {
			classes = append(classes, "refusal")
		}
		if errors.As(err, new(*ValidationError)) {
			classes = append(classes, "violation")
		}
		if errors.Is(err, ErrInconclusive) {
			classes = append(classes, "inconclusive")
		}
		if !slices.Equal(classes, []string{tc.want}) {
			t.Errorf("%s: classes %v, want %s: %v", name, classes, tc.want, err)
		}
	}
}
