package asyncapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openbindings/openbindings-go/invoke"
)

// TestUnaryPublish3xxIsFailure pins ASYNC-P-06 / §9.4: a unary publish
// succeeds IFF the final status, after any redirects, is 2xx. A 3xx with no
// Location is not followed by net/http and is the reachable non-2xx window;
// before the fix, runUnaryPublish guarded failure with `>= 400`, so a 3xx read
// as success and closed the output on a publish the server plausibly did not
// accept. The classification mirrors the module's own SSE-establishment path,
// so a 3xx maps via the shared status table to ERR_EXECUTION_FAILED — the same
// code the TS SDK produces.
func TestUnaryPublish3xxIsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 3xx with no Location: fetch/net.http returns it as final.
		w.WriteHeader(http.StatusFound) // 302
		_, _ = w.Write([]byte("not accepted"))
	}))
	defer srv.Close()

	binv := NewInvoker()
	defer binv.Close()

	call := binv.InvokeBinding(bg(), &invoke.BindingInvocationArgs{
		Source:   httpSource(srv),
		Selector: "#/operations/sendOpenMessage",
	})
	if err := call.Write(bg(), map[string]any{"text": "hi"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)

	var ie *invoke.InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("a 3xx-final unary publish must FAIL (ASYNC-P-06 §9.4), got success (%v)", err)
	}
	if ie.Code != invoke.ErrCodeExecutionFailed {
		t.Fatalf("3xx classifies via the SSE path's status table to ERR_EXECUTION_FAILED, got %s: %s", ie.Code, ie.Error())
	}
	if ie.HasData() {
		t.Errorf("HTTP status leaked as abstract data: %#v", ie.Data)
	}
}
