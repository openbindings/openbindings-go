package openapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	openapiclient "github.com/openbindings/openapi-client/go"
	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

func TestNativeCacheRetrievesChangingDocumentClosure(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			var revision atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				n := revision.Add(1)
				path := fmt.Sprintf(`{"servers":[{"url":"https://revision%d.example.test"}],"get":{"responses":{"204":{"description":"ok"}}}}`, n)
				if external {
					fmt.Fprint(w, path)
				} else {
					fmt.Fprintf(w, `{"openapi":"3.1.2","info":{"title":"cache","version":"1"},"paths":{"/ping":%s}}`, path)
				}
			}))
			defer server.Close()
			args := &invoke.BindingInvocationArgs{Source: invoke.InvocationSource{BindingSpec: BindingSpecOpenAPI31, Location: server.URL}, Selector: "#/paths/~1ping/get"}
			if external {
				args.Source.Content = openbindings.TextContent(fmt.Sprintf(`{"openapi":"3.1.2","info":{"title":"cache","version":"1"},"paths":{"/ping":{"$ref":%q}}}`, server.URL+"/path.json"))
			}
			invoker := NewInvoker()
			for n := 1; n <= 2; n++ {
				client, err := invoker.runtime.loadNativeClient(t.Context(), args, true)
				if err != nil {
					t.Fatal(err)
				}
				analysis, err := client.AnalyzeOperation(openapiclient.OperationRef(args.Selector))
				if err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("https://revision%d.example.test", n)
				if len(analysis.Servers) != 1 || analysis.Servers[0].URL != want {
					t.Fatalf("stale closure: servers = %#v; want %s", analysis.Servers, want)
				}
			}
		})
	}
}
