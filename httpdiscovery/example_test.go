package httpdiscovery_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/openbindings/openbindings-go/httpdiscovery"
)

func ExampleClient_Discover() {
	// A service constructs a validated snapshot at startup. Public browser
	// discovery opts into CORS; authentication can wrap this handler as usual.
	handler, err := httpdiscovery.NewHandler(
		[]byte(`{"openbindings":"0.2.0","operations":{"ping":{}}}`),
		httpdiscovery.HandlerOptions{AllowOrigin: "*"},
	)
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle(httpdiscovery.WellKnownPath, handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := httpdiscovery.Client{}
	result, err := client.Discover(ctx, server.URL)
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Report.Conclusion)
	_, present := result.Document.Operations["ping"]
	fmt.Println(present)
	// Output:
	// conformant
	// true
}
