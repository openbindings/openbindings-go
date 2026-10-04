package httpdiscovery_test

import (
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/openbindings/openbindings-go/httpdiscovery"
)

func TestHandlerMethodsAndRouting(t *testing.T) {
	handler, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"HEAD", httpdiscovery.WellKnownPath, 200},
		{"POST", httpdiscovery.WellKnownPath, 405},
		{"OPTIONS", httpdiscovery.WellKnownPath, 405},
		{"GET", "/elsewhere", 404},
		{"GET", httpdiscovery.WellKnownPath + "/", 404},
		{"GET", httpdiscovery.WellKnownPath + "?unused=1", 200},
	} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s %s = %d", tc.method, tc.path, w.Code)
		}
		if tc.method == "HEAD" && (w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(len(document))) {
			t.Error("HEAD did not describe the GET representation without a body")
		}
		if tc.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
			t.Error("405 missing Allow")
		}
	}
}

func TestHandlerRejectsInvalidHeader(t *testing.T) {
	handler, err := httpdiscovery.NewHandler([]byte(document), httpdiscovery.HandlerOptions{AllowOrigin: "*\r\nInjected: value"})
	if err == nil || handler != nil {
		t.Fatal("header injection accepted")
	}
}
