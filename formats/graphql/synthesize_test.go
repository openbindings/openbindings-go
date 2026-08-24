package graphql

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"
)

func synthesisSchema() *introspectionSchema {
	return &introspectionSchema{
		QueryType:        &typeRef{Kind: "OBJECT", Name: "ReadRoot"},
		MutationType:     &typeRef{Kind: "OBJECT", Name: "WriteRoot"},
		SubscriptionType: &typeRef{Kind: "OBJECT", Name: "StreamRoot"},
		Types: []fullType{
			{Kind: "OBJECT", Name: "ReadRoot", Fields: []field{
				{Name: "status", Type: typeRef{Kind: "SCALAR", Name: "String"}},
				{Name: "viewer", Description: "Current viewer", Type: typeRef{Kind: "OBJECT", Name: "User"}},
			}},
			{Kind: "OBJECT", Name: "WriteRoot", Fields: []field{{Name: "status", Type: typeRef{Kind: "SCALAR", Name: "String"}}}},
			{Kind: "OBJECT", Name: "StreamRoot", Fields: []field{{Name: "status", Type: typeRef{Kind: "SCALAR", Name: "String"}}}},
			{Kind: "OBJECT", Name: "User"},
			{Kind: "SCALAR", Name: "String"},
		},
	}
}

func TestConvertToInterfaceInventoriesRootFields(t *testing.T) {
	iface, err := convertToInterface(synthesisSchema(), "https://api.example.test/graphql")
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(iface.Operations))
	for key := range iface.Operations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	wantKeys := []string{"mutation_status", "status", "viewer"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("operation keys = %#v, want %#v", keys, wantKeys)
	}
	var selectors []string
	for _, binding := range iface.Bindings {
		selectors = append(selectors, binding.Selector)
	}
	sort.Strings(selectors)
	wantSelectors := []string{"mutation/status", "query/status", "query/viewer"}
	if !reflect.DeepEqual(selectors, wantSelectors) {
		t.Fatalf("selectors = %#v, want %#v", selectors, wantSelectors)
	}
	source := iface.Sources[DefaultSourceName]
	if source.BindingSpec != BindingSpec {
		t.Fatalf("bindingSpec = %q", source.BindingSpec)
	}
}

func TestSynthesisUsesApplicationRootValueSchemas(t *testing.T) {
	iface, _ := convertToInterface(synthesisSchema(), "")
	op := iface.Operations["viewer"]
	if !reflect.DeepEqual(op.Input, map[string]any{"type": "object"}) {
		t.Fatalf("input = %#v", op.Input)
	}
	raw, _ := json.Marshal(op)
	if string(raw) == "" || containsJSONText(raw, "_query") || containsJSONText(raw, `"viewer"`) {
		t.Fatalf("operation invents document projection: %s", raw)
	}
	if !reflect.DeepEqual(op.Output, map[string]any{
		"anyOf": []any{map[string]any{"type": "object"}, map[string]any{"type": "null"}},
	}) {
		t.Fatalf("output = %#v", op.Output)
	}
}

func containsJSONText(raw []byte, value string) bool {
	for i := 0; i+len(value) <= len(raw); i++ {
		if string(raw[i:i+len(value)]) == value {
			return true
		}
	}
	return false
}

func TestSynthesisCoverageIsExhaustive(t *testing.T) {
	content, _ := json.Marshal(map[string]any{"data": map[string]any{"__schema": synthesisSchema()}})
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    "https://api.example.test/graphql",
			Content:     content,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Coverage.Exhaustive || result.Coverage.FullyRepresented || len(result.Coverage.Entries) != 4 {
		t.Fatalf("coverage = %#v", result.Coverage)
	}
	for _, entry := range result.Coverage.Entries {
		if entry.SourceRef == "subscription/status" {
			if entry.Status != synthesize.SynthesisExcluded || entry.ReasonCode != "graphql.subscription_lifecycle_not_representable" || entry.Rule != "GQL-P-04" || len(entry.Requirements) != 0 {
				t.Fatalf("subscription coverage = %#v", entry)
			}
			continue
		}
		if entry.Status != synthesize.SynthesisRepresented || !reflect.DeepEqual(entry.Requirements, []string{"document"}) {
			t.Fatalf("represented coverage = %#v", entry)
		}
	}
}

func TestFirstCandidateProjectsRootFieldSchemas(t *testing.T) {
	iface, err := convertToInterface(synthesisSchema(), "", BindingSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := iface.Operations["subscription_status"]; ok {
		t.Fatal("the first-revision candidate synthesized a subscription operation")
	}
	if got := iface.Operations["status"].Output; !reflect.DeepEqual(got, map[string]any{
		"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
	}) {
		t.Fatalf("query/status output = %#v", got)
	}
}
