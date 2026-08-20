package synthesize

import (
	"os"
	"path/filepath"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func makeTestInterface(name string, ops ...string) *openbindings.Interface {
	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Name:         name,
		Operations:   map[string]openbindings.Operation{},
	}
	for _, op := range ops {
		iface.Operations[op] = openbindings.Operation{}
	}
	return iface
}

func selectionCorpusDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("OB_INTERFACES_CORPUS")
	if dir == "" {
		dir = filepath.Join("..", "..", "interfaces", "conformance")
	}
	if _, err := os.Stat(dir); err != nil {
		// OB_CORPUS_REQUIRED (set in CI) turns a missing corpus into a hard
		// failure so a mis-wired path turns CI red instead of silently green;
		// unset (local dev) it still skips.
		if os.Getenv("OB_CORPUS_REQUIRED") != "" {
			t.Fatalf("interfaces conformance corpus not found at %s (OB_CORPUS_REQUIRED is set; set OB_INTERFACES_CORPUS)", dir)
		}
		t.Skipf("interfaces conformance corpus not found at %s (set OB_INTERFACES_CORPUS)", dir)
	}
	return dir
}
