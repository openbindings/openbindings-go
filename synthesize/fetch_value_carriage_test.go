package synthesize

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchInterfaceRetainsExactNumbers(t *testing.T) {
	for _, token := range []string{"9007199254740993", "0.10000000000000000001", "1e400", "1e-400"} {
		t.Run(token, func(t *testing.T) {
			raw := fmt.Sprintf(`{"openbindings":"0.2.0","operations":{"test":{"input":{"minimum":%s},"examples":{"exact":{"input":%s}}}},"x-exact":%s}`, token, token, token)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, raw)
			}))
			defer srv.Close()
			got, err := FetchInterface(context.Background(), srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			if got.Synthesized {
				t.Fatal("direct OBI must not invoke synthesis")
			}
			op := got.Interface.Operations["test"]
			if op.Input.(map[string]any)["minimum"] != json.Number(token) || op.Examples["exact"].Input != json.Number(token) {
				t.Fatalf("numeric fields changed: %#v", op)
			}
		})
	}
}
