package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestScalarHookDelegationPreservesNativeDecoder(t *testing.T) {
	for _, kind := range []string{"integer", "number", "boolean", "string"} {
		for _, mode := range []string{"direct", "empty", "ordinary", "decline", "override", "error"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				body := "9223372036854775807"
				if kind == "boolean" {
					body = "true"
				}
				artifact := fmt.Sprintf(`{"openapi":"3.2.0","info":{"title":"Scalar","version":"1"},"servers":[{"url":"https://api.example"}],"paths":{"/scalar":{"get":{"responses":{"200":{"description":"ok","content":{"text/plain":{"schema":{"type":%q}}}}}}}}}`, kind)
				dispatches := 0
				adapter := NewInvokerWithClient(&http.Client{Transport: placementTransport(func(r *http.Request) (*http.Response, error) {
					dispatches++
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})})
				args := &invoke.BindingInvocationArgs{Source: invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI32, Content: openbindings.TextContent(artifact)}, Selector: "#/paths/~1scalar/get"}
				operation := invoke.NewOperationInvoker(adapter)
				switch mode {
				case "empty":
					args.Hooks = &invoke.InvokeHooks{}
				case "decline", "override", "error":
					args.Hooks = operation.SnapshotHooks(func(invoke.InvokeSite, invoke.RawResult) (any, error) {
						switch mode {
						case "override":
							return "elected", nil
						case "error":
							return nil, fmt.Errorf("decoder failure")
						default:
							return nil, invoke.ErrUseDefault
						}
					}, nil, nil)
				}
				ctx := context.Background()
				var call invoke.Invocation[any, any]
				if mode == "ordinary" {
					call = operation.InvokeBinding(ctx, args)
				} else {
					call = adapter.InvokeBinding(ctx, args)
				}
				if err := call.Close(); err != nil {
					t.Fatal(err)
				}
				value, err := invoke.Single(ctx, call.Outputs())
				if dispatches != 1 {
					t.Fatalf("dispatches=%d", dispatches)
				}
				if mode == "error" {
					if err == nil {
						t.Fatal("decoder error was ignored")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				want := body
				if mode == "override" {
					want = `"elected"`
				} else if kind == "string" {
					want = `"` + body + `"`
				}
				if string(encoded) != want {
					t.Fatalf("%T marshaled as %s, want %s", value, encoded, want)
				}
			})
		}
	}
}
