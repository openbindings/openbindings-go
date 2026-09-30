package openbindings

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The published interfaces (openbindings/interfaces) are real documents:
// each validates to its conclusion, and every operation schema in them
// compiles. Set OB_INTERFACES to that repository's directory to run this.
func TestPublishedInterfaces(t *testing.T) {
	dir := os.Getenv("OB_INTERFACES")
	if dir == "" {
		t.Skip("set OB_INTERFACES to the openbindings/interfaces directory")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*", "[0-9]*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no interfaces under %s: %v", dir, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(file, dir)
		iface, report, err := ValidateDocument(data, ValidateOptions{})
		t.Logf("%s: %s", name, report.Conclusion)
		if iface == nil {
			t.Errorf("%s: not decoded: %v", name, err)
			continue
		}
		contracts := contractsFor(t, iface)
		for key, operation := range iface.Operations {
			for direction, schema := range map[string]JSONSchema{"input": operation.Input, "output": operation.Output} {
				if schema == nil {
					continue
				}
				compile := contracts.CompileInput
				if direction == "output" {
					compile = contracts.CompileOutput
				}
				contract, err := compile(context.Background(), key)
				if err == nil {
					err = contract.Err()
				}
				if err != nil {
					t.Errorf("%s: %s %s: %v", name, key, direction, err)
				}
			}
		}
	}

}
