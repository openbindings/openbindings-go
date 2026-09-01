package openapi

import (
	"context"
	"fmt"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// Inspection reaches every edition the family implements. Until 2026-09-01 it
// reached three of four: listSelectors had no 3.2 branch, so it loaded through
// the shared 3.0/3.1 loader whose union gate refuses 3.2 before the
// per-binding-spec gate -- which admits it -- is consulted. `ob inspect` was
// the one command in the CLI that could not read a 3.2 document while
// synthesis, invocation, codegen and the rest all could, and the TypeScript
// twin's inspectSource had been reading them the whole time.
func TestInspectSourceReachesEveryImplementedEdition(t *testing.T) {
	for _, testCase := range []struct {
		bindingSpec string
		document    string
	}{
		{BindingSpecOpenAPI20, `{"swagger":"2.0","info":{"title":"t","version":"1"},"host":"api.example","schemes":["https"],
		  "paths":{"/p":{"get":{"operationId":"g","responses":{"200":{"description":"ok","schema":{"type":"object"}}}}}}}`},
		{BindingSpecOpenAPI30, `{"openapi":"3.0.4","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],
		  "paths":{"/p":{"get":{"operationId":"g","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}}`},
		{BindingSpecOpenAPI31, `{"openapi":"3.1.2","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],
		  "paths":{"/p":{"get":{"operationId":"g","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}}`},
		{BindingSpecOpenAPI32, `{"openapi":"3.2.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],
		  "paths":{"/p":{"get":{"operationId":"g","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}}`},
	} {
		t.Run(testCase.bindingSpec, func(t *testing.T) {
			inspection, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
				BindingSpec: testCase.bindingSpec,
				Content:     openbindings.TextContent(testCase.document),
			})
			if err != nil {
				t.Fatalf("inspect: %v", err)
			}
			if len(inspection.Targets) != 1 {
				t.Fatalf("targets = %d, want 1", len(inspection.Targets))
			}
			if got := inspection.Targets[0].Selector; got != "#/paths/~1p/get" {
				t.Errorf("selector = %q, want #/paths/~1p/get", got)
			}
			if !inspection.Exhaustive {
				t.Error("inspection should be exhaustive")
			}
		})
	}
}

// A sibling still refuses an edition it does not admit: the union gate was
// doing real work for 3.0/3.1 and the branch must not have relaxed it.
func TestInspectSourceRefusesAnEditionTheBindingSpecDoesNotAdmit(t *testing.T) {
	for _, testCase := range []struct{ bindingSpec, edition string }{
		{BindingSpecOpenAPI31, "3.2.0"},
		{BindingSpecOpenAPI30, "3.1.2"},
		{BindingSpecOpenAPI32, "3.1.2"},
	} {
		t.Run(fmt.Sprintf("%s/%s", testCase.bindingSpec, testCase.edition), func(t *testing.T) {
			document := fmt.Sprintf(`{"openapi":%q,"info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example"}],
			 "paths":{"/p":{"get":{"operationId":"g","responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"type":"object"}}}}}}}}}`, testCase.edition)
			if _, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
				BindingSpec: testCase.bindingSpec,
				Content:     openbindings.TextContent(document),
			}); err == nil {
				t.Fatalf("%s inspected a %s document, want a refusal", testCase.bindingSpec, testCase.edition)
			}
		})
	}
}
