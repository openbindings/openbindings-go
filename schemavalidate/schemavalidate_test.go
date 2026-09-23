package schemavalidate

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// A standalone schema is its own resolution root. Its $defs are its own, and
// a reference into an OBI's schemas map has no meaning in it.
func TestValidate_StandaloneSchemaIsItsOwnRoot(t *testing.T) {
	own := map[string]any{
		"$defs": map[string]any{"Task": map[string]any{"type": "string"}},
		"$ref":  "#/$defs/Task",
	}
	if err := Validate("hello", own); err != nil {
		t.Fatalf("own $defs entry must resolve within the schema: %v", err)
	}
	var mismatch *openbindings.SchemaValidationError
	if err := Validate(map[string]any{}, own); !errors.As(err, &mismatch) {
		t.Fatalf("own $defs entry must be enforced, got %v", err)
	}
	var unavailable *openbindings.SchemaGraphUnavailableError
	if err := Validate("hello", map[string]any{"$ref": "#/schemas/Task"}); !errors.As(err, &unavailable) {
		t.Fatalf("an OBI-position reference has no meaning in a standalone schema; want graph unavailable, got %v", err)
	}
	if err := Validate("hello", 5); !errors.As(err, &unavailable) {
		t.Fatalf("a value that is not a schema cannot be compiled; want graph unavailable, got %v", err)
	}
}

// A file: reference is never read: a verdict never depends on the validating
// machine's files.
func TestValidate_NeverReadsLocalFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "string.json")
	if err := os.WriteFile(path, []byte(`{"type":"string"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := map[string]any{"$ref": (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()}
	var unavailable *openbindings.SchemaGraphUnavailableError
	if err := Validate("text", ref); !errors.As(err, &unavailable) {
		t.Fatalf("want graph unavailable, got %v", err)
	}
}
