package openapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/synthesisscenarios"
)

// synthesisResourceClient serves a scenario's declared companion documents and
// nothing else. Every address a multi-document corpus scenario reaches must be
// answerable from its own closed resource map, so the suite never touches the
// network; an unlisted address is a 404 rather than a live retrieval.
func synthesisResourceClient(resources map[string]any) *http.Client {
	return &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		address := req.URL.String()
		resource, ok := resources[address]
		if !ok {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("no such corpus resource")),
				Request:    req,
			}, nil
		}
		body, isText := resource.(string)
		if !isText {
			encoded, err := json.Marshal(resource)
			if err != nil {
				return nil, err
			}
			body = string(encoded)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
}

func TestSynthesisScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	factory := func(scenario synthesisscenarios.Scenario) (openbindings.CoverageSynthesizer, error) {
		if len(scenario.Resources) == 0 {
			return NewSynthesizer(), nil
		}
		return NewSynthesizerWithClient(synthesisResourceClient(scenario.Resources)), nil
	}
	if err := synthesisscenarios.Verify(context.Background(), root, "openapi", factory); err != nil {
		if os.IsNotExist(err) && os.Getenv("OB_CORPUS_REQUIRED") == "" {
			t.Skip(err)
		}
		t.Fatal(err)
	}
}
