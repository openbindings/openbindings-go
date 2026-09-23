package schemacompiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// libraryFormats is copied from the library's own list of the formats it
// checks, which it does not export. Upgrading the library means comparing
// the list with the new version's format.go first: a format the list misses
// would be asserted again under the drafts before 2019-09.
func TestLibraryFormats_CopiedFromTheRequiredVersion(t *testing.T) {
	const copiedFrom = "v6.0.3"
	manifest, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require"))
		if len(fields) >= 2 && fields[0] == "github.com/santhosh-tekuri/jsonschema/v6" {
			if fields[1] != copiedFrom {
				t.Fatalf("go.mod requires jsonschema %s; compare libraryFormats with its format.go, then update copiedFrom", fields[1])
			}
			return
		}
	}
	t.Fatal("go.mod does not require github.com/santhosh-tekuri/jsonschema/v6")
}

// A draft-07 schema asserts format in the library; New makes every format it
// checks accept every value.
func TestNew_NoFormatIsAsserted(t *testing.T) {
	for _, name := range libraryFormats {
		c := New()
		if err := c.AddResource("urn:test:format", map[string]any{"$schema": "http://json-schema.org/draft-07/schema#", "format": name}); err != nil {
			t.Fatal(err)
		}
		schema, err := c.Compile("urn:test:format")
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate("\x00 is no value of any format"); err != nil {
			t.Errorf("format %q is asserted: %v", name, err)
		}
	}
}
