package openapi

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// A configuration requirement names the SAME context scope a credential
// requirement names: the resolved server base. The two media configuration
// points returned details with no Target at all until 2026-09-01, which cost
// a consumer twice. The challenge could not say WHERE the choice applies, and
// a runtime that keys context by target could not offer the command that
// satisfies it: `ob` suppresses its "satisfy it with" hint on an empty
// target, so propertyMedia and requestMedia were the only requirements it
// could name but not tell you how to supply.
func TestContextRequiredConfigurationCarriesTheServerTarget(t *testing.T) {
	const server = "https://api.example"

	for _, testCase := range []struct {
		name     string
		selector string
		spec     string
	}{
		{
			name:     "a wildcard Encoding contentType",
			selector: "#/paths/~1u/post",
			spec: `{"openapi":"3.0.4","info":{"title":"t","version":"1"},
			 "servers":[{"url":"` + server + `"}],
			 "paths":{"/u":{"post":{"operationId":"upload",
			   "requestBody":{"required":true,"content":{"multipart/form-data":{
			     "schema":{"type":"object","properties":{"file":{"type":"string","format":"binary"}}},
			     "encoding":{"file":{"contentType":"image/*"}}}}},
			   "responses":{"204":{"description":"ok"}}}}}}`,
		},
		{
			// R4's cell: an array on the content lane whose item-type default
			// defines no serialization for the container.
			name:     "an urlencoded array with no governed container bytes",
			selector: "#/paths/~1f/post",
			spec: `{"openapi":"3.0.4","info":{"title":"t","version":"1"},
			 "servers":[{"url":"` + server + `"}],
			 "paths":{"/f":{"post":{"operationId":"postForm",
			   "requestBody":{"required":true,"content":{"application/x-www-form-urlencoded":{
			     "schema":{"type":"object","properties":{"ids":{"type":"array","items":{"type":"integer"}}}}}}},
			   "responses":{"204":{"description":"ok"}}}}}}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI30, Content: openbindings.TextContent(testCase.spec)},
				Selector: testCase.selector,
			})
			_, invocationErr := driveSingle(t, call, map[string]any{"body": map[string]any{"file": "QUJD", "ids": []any{float64(1)}}})
			if invocationErr == nil {
				t.Fatal("invocation succeeded, want a context requirement")
			}
			if invocationErr.Code != invoke.ErrCodeContextRequired {
				t.Fatalf("code = %s, want %s", invocationErr.Code, invoke.ErrCodeContextRequired)
			}
			details := invoke.ContextRequiredFrom(invocationErr)
			if details == nil {
				t.Fatal("CONTEXT_REQUIRED carried no challenge")
			}
			if details.Target != server {
				t.Errorf("Target = %q, want %q", details.Target, server)
			}
			if len(details.Alternatives) == 0 || len(details.Alternatives[0].Requirements) == 0 {
				t.Fatal("challenge carries no requirement")
			}
			requirement := details.Alternatives[0].Requirements[0]
			if point, _ := requirement.Extra["point"].(string); point != "propertyMedia" {
				t.Errorf("configuration point = %q, want propertyMedia", point)
			}
		})
	}
}
