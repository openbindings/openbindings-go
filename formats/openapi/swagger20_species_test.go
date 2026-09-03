package openapi

// openbindings.openapi-2.0@1 §3.2's context-required species names a §12.1
// point "so supplying it makes the same invocation proceed". Two surfaces of
// this adapter can name a point: the advisory preflight (prepareBinding) and
// the authoritative invocation challenge. The preflight derives its answer from
// the synthesis model in this repository; the invocation's comes from the
// standalone engine. §12.1 states ONE boundary per point, so the two must state
// the same one, and this file is what keeps them from drifting apart.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

type swagger20SpeciesTransport struct{ requests []*http.Request }

func (t *swagger20SpeciesTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, r)
	return &http.Response{StatusCode: 204, Status: "204 No Content", Header: http.Header{},
		Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
}

func swagger20SpeciesArgs(document, selector string, bindCtx map[string]any) *invoke.BindingInvocationArgs {
	return &invoke.BindingInvocationArgs{
		Source: invoke.InvocationSource{
			BindingSpec: BindingSpecOpenAPI20,
			Location:    "https://api.example/swagger.json",
			Content:     openbindings.TextContent(document),
		},
		Selector: selector,
		Context:  bindCtx,
	}
}

func swagger20RequirementByPoint(details *invoke.ContextRequiredDetails, point string) *invoke.ContextRequirement {
	if details == nil {
		return nil
	}
	for _, alternative := range details.Alternatives {
		for index, requirement := range alternative.Requirements {
			if requirement.Extra["point"] == point {
				return &alternative.Requirements[index]
			}
		}
	}
	return nil
}

func swagger20AuthRequirement(details *invoke.ContextRequiredDetails, name string) *invoke.ContextRequirement {
	if details == nil {
		return nil
	}
	for _, alternative := range details.Alternatives {
		for index, requirement := range alternative.Requirements {
			if requirement.Name == name && requirement.Type != "config.value" {
				return &alternative.Requirements[index]
			}
		}
	}
	return nil
}

const swagger20SpeciesHead = `"swagger":"2.0","info":{"title":"species","version":"1"},"host":"api.example","schemes":["https"]`

// TestSwagger20PreflightAndChallengeStateOneBoundary invokes and preflights the
// same document and compares, for the point each names, the whole requirement:
// its point, its pointer, its description, its engine-asserted schema, and its
// durability. A difference in any of them would mean the two surfaces disagree
// about what §12.1 says.
func TestSwagger20PreflightAndChallengeStateOneBoundary(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		document string
		selector string
		input    any
		point    string
		auth     string
	}{
		{
			name:     "emptyValueForm",
			document: `{` + swagger20SpeciesHead + `,"paths":{"/p":{"get":{"parameters":[{"name":"q","in":"query","type":"string","allowEmptyValue":true}],"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1p/get",
			input:    map[string]any{"parameters": map[string]any{"q": ""}},
			point:    "emptyValueForm",
		},
		{
			name:     "requestMedia",
			document: `{` + swagger20SpeciesHead + `,"consumes":["application/json","text/plain"],"paths":{"/p":{"post":{"parameters":[{"name":"b","in":"body","required":true,"schema":{"type":"string"}}],"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1p/post",
			input:    map[string]any{"body": "x"},
			point:    "requestMedia",
		},
		{
			name:     "propertyMedia",
			document: `{` + swagger20SpeciesHead + `,"consumes":["multipart/form-data"],"paths":{"/p":{"post":{"parameters":[{"name":"f","in":"formData","required":true,"type":"file"}],"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1p/post",
			input:    map[string]any{"parameters": map[string]any{"f": "QUFB"}},
			point:    "propertyMedia",
		},
		{
			name:     "security selection",
			document: `{` + swagger20SpeciesHead + `,"securityDefinitions":{"k":{"type":"apiKey","name":"X-Key","in":"header"},"b":{"type":"basic"}},"security":[{"k":[]},{"b":[]}],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1p/get",
			point:    "security",
		},
		{
			name:     "apiKey credential",
			document: `{` + swagger20SpeciesHead + `,"securityDefinitions":{"k":{"type":"apiKey","name":"X-Key","in":"header"}},"security":[{"k":[]}],"paths":{"/p":{"get":{"responses":{"204":{"description":"ok"}}}}}}`,
			selector: "#/paths/~1p/get",
			auth:     "k",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &swagger20SpeciesTransport{}
			args := swagger20SpeciesArgs(testCase.document, testCase.selector, nil)
			call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(), args)
			_, ierr := driveOutputs(context.Background(), call, testCase.input)
			if ierr == nil || ierr.Code != invoke.ErrCodeContextRequired {
				t.Fatalf("invocation error = %v, want %s", ierr, invoke.ErrCodeContextRequired)
			}
			if len(transport.requests) != 0 {
				t.Fatalf("dispatched %d requests, want 0", len(transport.requests))
			}
			challenge := invoke.ContextRequiredFrom(ierr)
			preflight, err := NewInvoker().PrepareBinding(context.Background(), swagger20SpeciesArgs(testCase.document, testCase.selector, nil))
			if err != nil {
				t.Fatalf("preflight error: %v", err)
			}
			// Both surfaces assert one scope: the resolved §10 server base
			// (never the source location once the server resolves), as the
			// 3.x lane's two surfaces do.
			if challenge == nil || challenge.Target != "https://api.example" || preflight == nil || preflight.Target != challenge.Target {
				t.Fatalf("targets differ or are not the resolved server base: challenge=%q preflight=%q", challenge.Target, preflight.Target)
			}
			var fromChallenge, fromPreflight *invoke.ContextRequirement
			if testCase.point != "" {
				fromChallenge = swagger20RequirementByPoint(challenge, testCase.point)
				fromPreflight = swagger20RequirementByPoint(preflight, testCase.point)
			} else {
				fromChallenge = swagger20AuthRequirement(challenge, testCase.auth)
				fromPreflight = swagger20AuthRequirement(preflight, testCase.auth)
			}
			if fromChallenge == nil || fromPreflight == nil {
				t.Fatalf("challenge=%+v preflight=%+v, want both to name the requirement", challenge, preflight)
			}
			left, _ := json.Marshal(fromChallenge)
			right, _ := json.Marshal(fromPreflight)
			if string(left) != string(right) {
				t.Fatalf("the two surfaces state different boundaries:\n  invocation: %s\n  preflight : %s", left, right)
			}
		})
	}
}

// TestSwagger20SpeciesDoesNotMoveAdmissibility is the guard the block's own
// refusal condition asks for: naming a point must change what a refusal
// CARRIES, never whether the invocation refuses at all.
func TestSwagger20SpeciesDoesNotMoveAdmissibility(t *testing.T) {
	document := `{` + swagger20SpeciesHead + `,"paths":{"/p":{"get":{"parameters":[{"name":"q","in":"query","type":"string","allowEmptyValue":true}],"responses":{"204":{"description":"ok"}}}}}}`
	for _, testCase := range []struct {
		name     string
		input    any
		bindCtx  map[string]any
		dispatch bool
		code     string
	}{
		{name: "empty value, no choice", input: map[string]any{"parameters": map[string]any{"q": ""}}, code: invoke.ErrCodeContextRequired},
		{name: "non-empty value, no choice", input: map[string]any{"parameters": map[string]any{"q": "x"}}, dispatch: true},
		{name: "value absent, no choice", input: map[string]any{"parameters": map[string]any{}}, dispatch: true},
		{name: "no envelope at all", dispatch: true},
		{name: "empty value, name-only", input: map[string]any{"parameters": map[string]any{"q": ""}},
			bindCtx: map[string]any{"configuration": map[string]any{"emptyValueForm": "name-only"}}, dispatch: true},
		{name: "empty value, empty", input: map[string]any{"parameters": map[string]any{"q": ""}},
			bindCtx: map[string]any{"configuration": map[string]any{"emptyValueForm": "empty"}}, dispatch: true},
		{name: "empty value, value the point does not admit", input: map[string]any{"parameters": map[string]any{"q": ""}},
			bindCtx: map[string]any{"configuration": map[string]any{"emptyValueForm": "sometimes"}}, code: invoke.ErrCodeRefused},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &swagger20SpeciesTransport{}
			call := NewInvokerWithClient(&http.Client{Transport: transport}).InvokeBinding(context.Background(),
				swagger20SpeciesArgs(document, "#/paths/~1p/get", testCase.bindCtx))
			_, ierr := driveOutputs(context.Background(), call, testCase.input)
			if testCase.dispatch {
				if ierr != nil {
					t.Fatalf("error = %v, want dispatch", ierr)
				}
				if len(transport.requests) != 1 {
					t.Fatalf("dispatched %d requests, want 1", len(transport.requests))
				}
				return
			}
			if ierr == nil || string(ierr.Code) != testCase.code {
				t.Fatalf("error = %v, want %s", ierr, testCase.code)
			}
			if len(transport.requests) != 0 {
				t.Fatalf("dispatched %d requests, want 0", len(transport.requests))
			}
		})
	}
}
