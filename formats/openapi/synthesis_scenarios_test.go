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

	"github.com/openbindings/openbindings-go/synthesize"
	"github.com/openbindings/openbindings-go/synthesize/synthesisscenarios"
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

// synthesisFactory is the factory this family's runner uses. Unlike the six
// self-contained families it does serve companion documents, through the
// NewSynthesizerWithClient seam the package already exports.
func synthesisFactory(scenario synthesisscenarios.Scenario) (synthesize.CoverageSynthesizer, error) {
	if len(scenario.Resources) == 0 {
		return NewSynthesizer(), nil
	}
	return NewSynthesizerWithClient(synthesisResourceClient(scenario.Resources)), nil
}

var pendingSynthesisScenarioReasons = map[string]string{}

func TestSynthesisScenarios(t *testing.T) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = filepath.Join("..", "..", "..", "spec", "conformance")
	}
	for _, family := range []string{"openapi-2.0", "openapi-3.0", "openapi-3.1", "openapi-3.2"} {
		family := family
		t.Run(family, func(t *testing.T) {
			file, err := synthesisscenarios.Load(root, family)
			if err != nil {
				if os.IsNotExist(err) && os.Getenv("OB_CORPUS_REQUIRED") == "" {
					t.Skip(err)
				}
				t.Fatal(err)
			}

			// These are pre-existing engine gaps owned by later N10 nodes. They
			// remain fenced from verification, but each corpus position is a
			// visible, reasoned subtest skip rather than a silent green exclusion.
			seenPending := map[string]bool{}
			for _, scenario := range file.Scenarios {
				reason, pending := pendingSynthesisScenarioReasons[scenario.ID]
				if !pending {
					continue
				}
				seenPending[scenario.ID] = true
				t.Run(scenario.ID, func(t *testing.T) {
					t.Skip(reason)
				})
			}
			prefix := "OAPI20-"
			if family == "openapi-3.0" {
				prefix = "OAPI30-"
			} else if family == "openapi-3.1" {
				prefix = "OAPI31-"
			} else if family == "openapi-3.2" {
				prefix = "OAPI32-"
			}
			for id := range pendingSynthesisScenarioReasons {
				if strings.HasPrefix(id, prefix) && !seenPending[id] {
					t.Fatalf("pending synthesis scenario %s is absent from the %s corpus", id, family)
				}
			}

			if err := synthesisscenarios.VerifyWhere(context.Background(), root, family, synthesisFactory, func(scenario synthesisscenarios.Scenario) bool {
				_, pending := pendingSynthesisScenarioReasons[scenario.ID]
				return !pending
			}); err != nil {
				t.Fatal(err)
			}
			t.Logf("verified %d synthesis scenarios", len(file.Scenarios))
		})
	}
}

// The companion-document seam serves the declared addresses and refuses every
// other one, so a scenario can never reach the network by naming an address it
// did not declare.
func TestSynthesisResourceClientServesOnlyDeclaredAddresses(t *testing.T) {
	client := synthesisResourceClient(map[string]any{
		"https://companion.example/library.yaml": "openapi: 3.1.2\n",
	})
	declared, err := client.Get("https://companion.example/library.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer declared.Body.Close()
	if declared.StatusCode != http.StatusOK {
		t.Fatalf("declared address status = %d, want 200", declared.StatusCode)
	}
	undeclared, err := client.Get("https://companion.example/elsewhere.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer undeclared.Body.Close()
	if undeclared.StatusCode != http.StatusNotFound {
		t.Fatalf("undeclared address status = %d, want 404", undeclared.StatusCode)
	}
}
