package graphql

import (
	"context"
	"encoding/json"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSourceUsesPinnedContentAndCanonicalSelectors(t *testing.T) {
	content, _ := json.Marshal(map[string]any{"data": map[string]any{"__schema": synthesisSchema()}})
	inspection, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Location:    "https://api.example.test/graphql",
		Content:     content,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exhaustive || len(inspection.Targets) != 3 {
		t.Fatalf("inspection = %#v", inspection)
	}
	selectors := map[string]bool{}
	for _, target := range inspection.Targets {
		selectors[target.Selector] = true
	}
	for _, selector := range []string{"query/status", "query/viewer", "mutation/status"} {
		if !selectors[selector] {
			t.Errorf("missing %q", selector)
		}
	}
	if selectors["subscription/status"] {
		t.Fatal("the first-revision candidate inspection exposed subscription/status")
	}
}
