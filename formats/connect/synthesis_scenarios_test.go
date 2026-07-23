package connect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openbindings/openbindings-go/synthesisscenarios"
)

func TestSynthesisScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	if err := synthesisscenarios.Verify(context.Background(), root, "connect", NewSynthesizer()); err != nil {
		if os.IsNotExist(err) && os.Getenv("OB_CORPUS_REQUIRED") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
}
