package graphql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPResultRetainsExactNumberTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/graphql-response+json")
		_, _ = w.Write([]byte(`{"data":{"id":9007199254740993,"large":1e400}}`))
	}))
	defer server.Close()
	got, err := doGraphQLHTTP(context.Background(), server.Client(), server.URL, "{id large}", "", nil, nil, 4096)
	if err != nil {
		t.Fatal(err)
	}
	data := got.Body["data"].(map[string]any)
	if data["id"] != json.Number("9007199254740993") || data["large"] != json.Number("1e400") {
		t.Fatalf("rounded or lost numbers: %#v", data)
	}
}
