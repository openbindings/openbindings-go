package connect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openbindings/openbindings-go/synthesisscenarios"
)

// synthesisFactory is the factory this family's runner uses. Its corpus
// sources are self-contained, so it is Fixed: a scenario declaring companion
// documents is refused loudly rather than executed against a resolver that
// would never see them. TestSynthesisScenarioResourcesRefused executes that
// refusal, so the guard is proven wired rather than merely present.
func synthesisFactory() synthesisscenarios.SynthesizerFactory {
	return synthesisscenarios.Fixed(NewSynthesizer())
}

func TestSynthesisScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	if err := synthesisscenarios.Verify(context.Background(), root, "connect", synthesisFactory()); err != nil {
		if os.IsNotExist(err) && os.Getenv("OB_CORPUS_REQUIRED") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
}

func TestSynthesisScenarioResourcesRefused(t *testing.T) {
	_, err := synthesisFactory()(synthesisscenarios.Scenario{
		ID:        "PROBE-SS-99",
		Resources: map[string]any{"https://companion.example/library.yaml": map[string]any{}},
	})
	if err == nil {
		t.Fatal("a scenario declaring companion resources must be refused by a runner that does not serve them")
	}
}
