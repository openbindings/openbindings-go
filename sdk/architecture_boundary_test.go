package sdk

import (
	"os"
	"strings"
	"testing"
)

func TestRuntimeContainsNoBindingImplementation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, forbidden := range []string{"/formats/", "openapi-client", "asyncapi-client"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s imports binding-specific implementation %q", entry.Name(), forbidden)
			}
		}
	}
}
