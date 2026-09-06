package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

type failurePresenceTransport string

func (body failurePresenceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
}

func TestNativeFailureDataPresenceAcrossEditions(t *testing.T) {
	for _, edition := range []string{"2.0", "3.0.4", "3.1.2", "3.2.0"} {
		for _, tc := range []struct {
			name, body string
			want       any
			present    bool
		}{
			{"absent", "", nil, false},
			{"null", "null", nil, true},
			{"object", `{"reason":"missing"}`, map[string]any{"reason": "missing"}, true},
			{"empty-string", `""`, "", true},
			{"false", "false", false, true},
			{"zero", "0", float64(0), true},
		} {
			t.Run(edition+"/"+tc.name, func(t *testing.T) {
				prefix := fmt.Sprintf(`"openapi":%q,"servers":[{"url":"https://api.example.test"}]`, edition)
				response := `{"description":"failure","content":{"application/json":{"schema":{}}}}`
				if edition == "2.0" {
					prefix = `"swagger":"2.0","host":"api.example.test","schemes":["https"],"produces":["application/json"]`
					response = `{"description":"failure","schema":{}}`
				}
				document := fmt.Sprintf(`{%s,"info":{"title":"Presence","version":"1"},"paths":{"/value":{"get":{"responses":{"400":%s}}}}}`, prefix, response)
				call := NewInvokerWithClient(&http.Client{Transport: failurePresenceTransport(tc.body)}).InvokeBinding(context.Background(), &invoke.BindingInvocationArgs{
					Source: invoke.InvocationSource{BindingSpec: "openbindings.openapi-" + edition[:3] + "@1", Content: json.RawMessage(document)}, Selector: "#/paths/~1value/get",
				})
				call.Close()
				_, err := call.Outputs().Read(context.Background())
				if err == nil {
					t.Fatal("failure emitted an output")
				}
				encoded, marshalErr := json.Marshal(invoke.AsInvocationError(err))
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				var wire map[string]any
				if err := json.Unmarshal(encoded, &wire); err != nil {
					t.Fatal(err)
				}
				if wire["code"] != "ERR_EXECUTION_FAILED" {
					t.Fatalf("unexpected terminal: %s", encoded)
				}
				data, present := wire["data"]
				if present != tc.present || !reflect.DeepEqual(data, tc.want) {
					t.Fatalf("wire=%s; want present=%v value=%#v", encoded, tc.present, tc.want)
				}
			})
		}
	}
}
