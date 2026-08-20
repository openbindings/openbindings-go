package openapi

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// §9.3 (OAPI-P-05): the target URL is the resolved server joined with the
// operation's path template, so a template variable no declared path parameter
// can supply leaves no target to address, and the refusal must precede dispatch
// rather than putting a percent-encoded `%7Bname%7D` segment on a live service.
// The corpus original is spree/spree's
// `/api/v2/storefront/policies/{policy_slug}` `show-policy`, which declares no
// `parameters` at all.
//
// Invocation here delegates to the standalone engine, so this exercises the
// binding's own OpenBindings-boundary outcome; the input-model twin below
// covers this package's mirrored copy of the rule.
func TestRuntimeRefusesUnaddressablePathTemplateVariableBeforeDispatch(t *testing.T) {
	dispatched := ""
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		dispatched = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    req,
		}, nil
	})}
	call := NewRuntimeWithClient(client).Invoke(context.Background(), &RuntimeInvocationArgs{
		Source: RuntimeSource{Content: openbindings.TextContent(`{
			"openapi":"3.0.3",
			"info":{"title":"unaddressable target","version":"1"},
			"servers":[{"url":"https://api.example.test"}],
			"paths":{"/api/v2/storefront/policies/{policy_slug}":{"get":{
				"operationId":"show-policy",
				"responses":{"200":{"description":"policy","content":{"application/json":{}}}}
			}}}
		}`)},
		Ref: "#/paths/~1api~1v2~1storefront~1policies~1{policy_slug}/get",
	})
	_ = call.Close()
	if _, err := openbindings.Single(context.Background(), call.Outputs()); err == nil {
		t.Fatal("invocation succeeded, want a pre-dispatch refusal")
	} else if code := openbindings.AsInvocationError(err); code == nil || code.Code != openbindings.ErrCodeRefused {
		t.Fatalf("error = %v, want %s", err, openbindings.ErrCodeRefused)
	}
	if dispatched != "" {
		t.Fatalf("refused invocation still put %q on the wire", dispatched)
	}
}

func TestCheckPathTemplateAddressability(t *testing.T) {
	pathParam := func(name string) *openapi3.ParameterRef {
		return &openapi3.ParameterRef{Value: &openapi3.Parameter{Name: name, In: openapi3.ParameterInPath}}
	}
	queryParam := func(name string) *openapi3.ParameterRef {
		return &openapi3.ParameterRef{Value: &openapi3.Parameter{Name: name, In: openapi3.ParameterInQuery}}
	}
	for _, testCase := range []struct {
		name     string
		template string
		params   openapi3.Parameters
		want     string
	}{
		{name: "no variables", template: "/items"},
		{name: "declared variable", template: "/items/{id}", params: openapi3.Parameters{pathParam("id")}},
		{
			name:     "undeclared variable",
			template: "/api/v2/storefront/policies/{policy_slug}",
			want:     "path template variable(s) policy_slug have no declared path parameter",
		},
		{
			// A same-named parameter in another location cannot supply the
			// path template: OAS identity is name PLUS location.
			name:     "same name in another location does not address it",
			template: "/items/{id}",
			params:   openapi3.Parameters{queryParam("id")},
			want:     "path template variable(s) id have no declared path parameter",
		},
		{
			name:     "every undeclared variable is named in order",
			template: "/{tenant}/reports/{report_id}/{section}",
			params:   openapi3.Parameters{pathParam("report_id")},
			want:     "path template variable(s) section, tenant have no declared path parameter",
		},
		{name: "unpaired opening brace is a literal", template: "/items/a{b"},
		{name: "unpaired closing brace is a literal", template: "/items/a}b"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := checkPathTemplateAddressability(testCase.template, testCase.params)
			switch {
			case testCase.want == "" && err != nil:
				t.Fatalf("err = %v, want addressable", err)
			case testCase.want != "" && err == nil:
				t.Fatalf("err = nil, want %q", testCase.want)
			case testCase.want != "" && !strings.Contains(err.Error(), testCase.want):
				t.Fatalf("err = %q, want it to contain %q", err.Error(), testCase.want)
			}
		})
	}
}

func TestPathTemplateVariablesReadsBraceDelimitedExpressions(t *testing.T) {
	for _, testCase := range []struct {
		template string
		want     []string
	}{
		{template: "/items", want: nil},
		{template: "/items/{id}", want: []string{"id"}},
		{template: "/{tenant}/items/{id}", want: []string{"tenant", "id"}},
		{template: "/items/{id}.{format}", want: []string{"id", "format"}},
		{template: "/items/a{b", want: nil},
		{template: "/items/a}b", want: nil},
		{template: "/items/{{id}", want: []string{"id"}},
		{template: "/items/{}", want: []string{""}},
	} {
		if got := pathTemplateVariables(testCase.template); !reflect.DeepEqual(got, testCase.want) {
			t.Errorf("pathTemplateVariables(%q) = %#v, want %#v", testCase.template, got, testCase.want)
		}
	}
}
