package graphql

import (
	"context"
	"encoding/json"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSourceUsesPinnedContentAndCanonicalRefs(t *testing.T) {
	content, _ := json.Marshal(map[string]any{"data": map[string]any{"__schema": synthesisSchema()}})
	inspection, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
		BindingSpec: BindingSpec,
		Location:    "https://api.example.test/graphql",
		Content:     content,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Exhaustive || len(inspection.Targets) != 4 {
		t.Fatalf("inspection = %#v", inspection)
	}
	refs := map[string]bool{}
	for _, target := range inspection.Targets {
		refs[target.Ref] = true
	}
	for _, ref := range []string{"query/status", "query/viewer", "mutation/status", "subscription/status"} {
		if !refs[ref] {
			t.Errorf("missing %q", ref)
		}
	}
}
