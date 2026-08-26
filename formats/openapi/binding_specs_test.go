package openapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

func TestRegisteredOpenAPIFamiliesIncludeUnimplementedTokens(t *testing.T) {
	want := map[string]bool{
		BindingSpecOpenAPI20: false,
		BindingSpecOpenAPI30: true,
		BindingSpecOpenAPI31: true,
		BindingSpecOpenAPI32: false,
	}
	if len(openAPIBindingSpecRegistry) != len(want) {
		t.Fatalf("registry size = %d, want %d", len(openAPIBindingSpecRegistry), len(want))
	}
	for token, implemented := range want {
		registration, present := openAPIBindingSpecRegistry[token]
		if !present || registration.implemented != implemented {
			t.Errorf("registration %q = %#v, want implemented=%v", token, registration, implemented)
		}
	}
	wantEditions := map[string]map[string]bool{
		BindingSpecOpenAPI20: nil,
		BindingSpecOpenAPI30: {"3.0.0": true, "3.0.1": true, "3.0.2": true, "3.0.3": true, "3.0.4": true},
		BindingSpecOpenAPI31: {"3.1.0": true, "3.1.1": true, "3.1.2": true},
		BindingSpecOpenAPI32: nil,
	}
	for token, editions := range wantEditions {
		if got := openAPIBindingSpecRegistry[token].editions; !reflect.DeepEqual(got, editions) {
			t.Errorf("registration %q editions = %#v, want %#v", token, got, editions)
		}
	}
}

func TestUnimplementedOpenAPIFamiliesRefuseBeforeArtifactParsing(t *testing.T) {
	for _, token := range []string{BindingSpecOpenAPI20, BindingSpecOpenAPI32} {
		t.Run(token, func(t *testing.T) {
			args := &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: token, Content: openbindings.TextContent("not an OpenAPI artifact")},
				Selector: "#/paths/~1x/get",
			}
			call := NewInvoker().InvokeBinding(context.Background(), args)
			_, invocationErr := driveSingle(t, call, nil)
			if invocationErr == nil || invocationErr.Code != ErrCodeUnsupportedBindingSpec {
				t.Fatalf("invocation error = %#v, want %s", invocationErr, ErrCodeUnsupportedBindingSpec)
			}

			_, synthesisErr := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{BindingSpec: token, Content: openbindings.TextContent("not an OpenAPI artifact")}},
			})
			if synthesisErr == nil || !strings.Contains(synthesisErr.Error(), ErrCodeUnsupportedBindingSpec) {
				t.Fatalf("synthesis error = %v, want %s", synthesisErr, ErrCodeUnsupportedBindingSpec)
			}

			_, prepareErr := NewInvoker().PrepareBinding(context.Background(), args)
			var prepareInvocationErr *invoke.InvocationError
			if !errors.As(prepareErr, &prepareInvocationErr) || prepareInvocationErr.Code != ErrCodeUnsupportedBindingSpec {
				t.Fatalf("prepare error = %#v, want %s", prepareErr, ErrCodeUnsupportedBindingSpec)
			}
		})
	}
}

func TestBindingInvokerRequiresAnExactFamilyToken(t *testing.T) {
	args := &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{Content: openbindings.TextContent("not an OpenAPI artifact")},
		Selector: "#/paths/~1x/get",
	}
	_, invocationErr := driveSingle(t, NewInvoker().InvokeBinding(context.Background(), args), nil)
	if invocationErr == nil || invocationErr.Code != ErrCodeUnsupportedBindingSpec {
		t.Fatalf("invocation error = %#v, want %s", invocationErr, ErrCodeUnsupportedBindingSpec)
	}
}

func TestOpenAPIFamilyTokenMustMatchArtifactEdition(t *testing.T) {
	tests := []struct {
		name, token, edition string
	}{
		{"3.0 token with 3.1 artifact", BindingSpecOpenAPI30, "3.1.2"},
		{"3.1 token with 3.0 artifact", BindingSpecOpenAPI31, "3.0.4"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			artifact := fmt.Sprintf(
				`{"openapi":%q,"info":{"title":"edition gate","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
				testCase.edition,
			)
			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: testCase.token, Content: openbindings.TextContent(artifact)},
				Selector: "#/paths/~1x/get",
			})
			_, invocationErr := driveSingle(t, call, nil)
			if invocationErr == nil || invocationErr.Code != invoke.ErrCodeSourceLoadFailed {
				t.Fatalf("invocation error = %#v, want %s", invocationErr, invoke.ErrCodeSourceLoadFailed)
			}

			_, synthesisErr := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{BindingSpec: testCase.token, Content: openbindings.TextContent(artifact)}},
			})
			if synthesisErr == nil || !strings.Contains(synthesisErr.Error(), "not admitted") {
				t.Fatalf("synthesis error = %v, want token/edition refusal", synthesisErr)
			}

			_, prepareErr := NewInvoker().PrepareBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: testCase.token, Content: openbindings.TextContent(artifact)},
				Selector: "#/paths/~1x/get",
			})
			var prepareInvocationErr *invoke.InvocationError
			if !errors.As(prepareErr, &prepareInvocationErr) || prepareInvocationErr.Code != invoke.ErrCodeSourceLoadFailed {
				t.Fatalf("prepare error = %#v, want %s", prepareErr, invoke.ErrCodeSourceLoadFailed)
			}
		})
	}
}

func TestRequestBodyMethodDispositionIsFamilySpecific(t *testing.T) {
	for _, method := range []string{"get", "head", "delete", "options", "trace"} {
		if !requestBodyIgnoredForBindingSpec(BindingSpecOpenAPI30, method) {
			t.Errorf("3.0 %s requestBody was not ignored", method)
		}
	}
	for _, method := range []string{"get", "head", "delete", "options", "post", "put", "patch"} {
		if requestBodyIgnoredForBindingSpec(BindingSpecOpenAPI31, method) {
			t.Errorf("3.1 %s requestBody was ignored", method)
		}
	}
	if !requestBodyIgnoredForBindingSpec(BindingSpecOpenAPI31, "trace") {
		t.Error("3.1 TRACE requestBody was not ignored")
	}

	for _, testCase := range []struct {
		token, edition string
		wantInput      bool
	}{{BindingSpecOpenAPI30, "3.0.4", false}, {BindingSpecOpenAPI31, "3.1.2", true}} {
		artifact := fmt.Sprintf(
			`{"openapi":%q,"info":{"title":"method body","version":"1"},"paths":{"/x":{"get":{"operationId":"read","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"name":{"type":"string"}}}}}},"responses":{"204":{"description":"ok"}}}}}}`,
			testCase.edition,
		)
		iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
			Sources: []synthesize.SynthesizeSource{{BindingSpec: testCase.token, Content: openbindings.TextContent(artifact)}},
		})
		if err != nil {
			t.Fatalf("synthesize %s: %v", testCase.token, err)
		}
		operation := iface.Operations["read"]
		if got := operation.Input != nil; got != testCase.wantInput {
			t.Errorf("%s GET input present = %v, want %v", testCase.token, got, testCase.wantInput)
		}
		binding := iface.Bindings["read."+DefaultSourceName]
		if got := binding.InputTransform != nil; got != testCase.wantInput {
			t.Errorf("%s GET inputTransform present = %v, want %v", testCase.token, got, testCase.wantInput)
		}
	}
}

func TestDuplicateEffectiveParameterIdentityRefusesTheOperation(t *testing.T) {
	for _, testCase := range []struct{ token, edition string }{
		{BindingSpecOpenAPI30, "3.0.4"},
		{BindingSpecOpenAPI31, "3.1.2"},
	} {
		t.Run(testCase.edition, func(t *testing.T) {
			artifact := fmt.Sprintf(
				`{"openapi":%q,"info":{"title":"duplicate parameters","version":"1"},"servers":[{"url":"https://example.test"}],"paths":{"/x":{"get":{"operationId":"read","parameters":[{"name":"q","in":"query","schema":{"type":"string"}},{"name":"q","in":"query","schema":{"type":"string"}}],"responses":{"204":{"description":"ok"}}}}}}`,
				testCase.edition,
			)
			call := NewInvoker().InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
				Source:   invoke.InvocationSource{BindingSpec: testCase.token, Content: openbindings.TextContent(artifact)},
				Selector: "#/paths/~1x/get",
			})
			_, invocationErr := driveSingle(t, call, nil)
			if invocationErr == nil || invocationErr.Code != invoke.ErrCodeRefused {
				t.Fatalf("invocation error = %#v, want %s", invocationErr, invoke.ErrCodeRefused)
			}

			_, synthesisErr := NewSynthesizer().SynthesizeInterface(context.Background(), &synthesize.SynthesizeInput{
				Sources: []synthesize.SynthesizeSource{{BindingSpec: testCase.token, Content: openbindings.TextContent(artifact)}},
			})
			if synthesisErr == nil || !strings.Contains(synthesisErr.Error(), "declared more than once") {
				t.Fatalf("synthesis error = %v, want duplicate-identity exclusion", synthesisErr)
			}
		})
	}
}
