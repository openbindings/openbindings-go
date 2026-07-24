package graphql

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
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
	wantKeys := []string{"mutation_status", "status", "subscription_status", "viewer"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("operation keys = %#v, want %#v", keys, wantKeys)
	}
	var refs []string
	for _, binding := range iface.Bindings {
		refs = append(refs, binding.Ref)
	}
	sort.Strings(refs)
	wantRefs := []string{"mutation/status", "query/status", "query/viewer", "subscription/status"}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Fatalf("refs = %#v, want %#v", refs, wantRefs)
	}
	source := iface.Sources[DefaultSourceName]
	if source.BindingSpec != BindingSpec {
		t.Fatalf("bindingSpec = %q", source.BindingSpec)
	}
}

func TestSynthesisUsesBroadBoundarySchemas(t *testing.T) {
	iface, _ := convertToInterface(synthesisSchema(), "")
	op := iface.Operations["viewer"]
	if !reflect.DeepEqual(op.Input, map[string]any{"type": "object"}) {
		t.Fatalf("input = %#v", op.Input)
	}
	raw, _ := json.Marshal(op)
	if string(raw) == "" || containsJSONText(raw, "_query") || containsJSONText(raw, `"viewer"`) {
		t.Fatalf("operation invents document projection: %s", raw)
	}
	output := op.Output.(map[string]any)
	if output["type"] != "object" {
		t.Fatalf("output = %#v", output)
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
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Location:    "https://api.example.test/graphql",
			Content:     content,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Coverage.Exhaustive || !result.Coverage.FullyRepresented || len(result.Coverage.Entries) != 4 {
		t.Fatalf("coverage = %#v", result.Coverage)
	}
	for _, entry := range result.Coverage.Entries {
		if len(entry.Requirements) == 0 || entry.Requirements[0] != "document" {
			t.Fatalf("requirements = %#v", entry.Requirements)
		}
		if entry.SourceRef == "subscription/status" && !reflect.DeepEqual(entry.Requirements, []string{"document", "subscriptionTarget"}) {
			t.Fatalf("subscription requirements = %#v", entry.Requirements)
		}
	}
}
