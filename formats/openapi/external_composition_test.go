package openapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/synthesize"
)

// externalCompositionCasesDigest pins the frozen twin case table. The identical
// file is executed by openapi-client/go and by
// openbindings-ts/packages/openapi; changing it in one engine without the
// others fails here.
const externalCompositionCasesDigest = "94e8e61fd8851594de2c7b39f7f25bce8d7f2f5e95f1e1d17354f61f7d3da16f"

type externalCompositionTable struct {
	Note  string                    `json:"note"`
	Cases []externalCompositionCase `json:"cases"`
}

type externalCompositionCase struct {
	Name      string                                 `json:"name"`
	Why       string                                 `json:"why"`
	Entry     string                                 `json:"entry"`
	Documents map[string]externalCompositionDocument `json:"documents"`
	Expect    externalCompositionExpectation         `json:"expect"`
}

type externalCompositionDocument struct {
	Text string          `json:"text"`
	JSON json.RawMessage `json:"json"`
}

func (d externalCompositionDocument) body(t *testing.T) string {
	t.Helper()
	if d.JSON != nil {
		return string(d.JSON)
	}
	return d.Text
}

type externalCompositionExpectation struct {
	Outcome         string                     `json:"outcome"`
	Operations      []string                   `json:"operations"`
	Values          map[string]json.RawMessage `json:"values"`
	DefsAt          string                     `json:"defsAt"`
	Defs            []string                   `json:"defs"`
	Unretrieved     []string                   `json:"unretrieved"`
	MessageContains []string                   `json:"messageContains"`
}

func loadExternalCompositionTable(t *testing.T) externalCompositionTable {
	t.Helper()
	data, err := os.ReadFile("testdata/external-composition-cases.json")
	if err != nil {
		t.Fatalf("read case table: %v", err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != externalCompositionCasesDigest {
		t.Fatalf("case table digest = %s, want %s; the table is shared byte-for-byte with the other engines", got, externalCompositionCasesDigest)
	}
	var table externalCompositionTable
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatalf("parse case table: %v", err)
	}
	if len(table.Cases) == 0 {
		t.Fatal("case table is empty")
	}
	return table
}

// TestExternalCompositionIsPointerScoped executes every case of the shared
// table through this engine's own synthesis entry. It proves the closure by
// RUNNING the resolver over a multi-document artifact, never by reading it.
func TestExternalCompositionIsPointerScoped(t *testing.T) {
	table := loadExternalCompositionTable(t)
	for _, testCase := range table.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			retrieved := map[string]bool{}
			client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				address := req.URL.String()
				retrieved[address] = true
				document, ok := testCase.Documents[address]
				if !ok {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Header:     http.Header{},
						Body:       io.NopCloser(strings.NewReader("no such document")),
						Request:    req,
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader(document.body(t))),
					Request:    req,
				}, nil
			})}

			iface, err := NewSynthesizerWithClient(client).SynthesizeInterface(
				context.Background(),
				&synthesize.SynthesizeInput{Sources: []synthesize.SynthesizeSource{{
					BindingSpec: bindingSpecForTestDocument(testCase.Documents[testCase.Entry].body(t)),
					Location:    testCase.Entry,
				}}},
			)

			if testCase.Expect.Outcome == "refused" {
				if err == nil {
					t.Fatalf("%s: expected refusal, got an interface", testCase.Why)
				}
				for _, fragment := range testCase.Expect.MessageContains {
					if !strings.Contains(err.Error(), fragment) {
						t.Fatalf("refusal does not name %q: %v", fragment, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("%s: %v", testCase.Why, err)
			}

			encoded, marshalErr := json.Marshal(iface)
			if marshalErr != nil {
				t.Fatalf("marshal interface: %v", marshalErr)
			}
			var document any
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatalf("reparse interface: %v", err)
			}

			if testCase.Expect.Operations != nil {
				got := make([]string, 0, len(iface.Operations))
				for key := range iface.Operations {
					got = append(got, key)
				}
				sort.Strings(got)
				want := append([]string(nil), testCase.Expect.Operations...)
				sort.Strings(want)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("operations = %v, want %v", got, want)
				}
			}

			for pointer, expected := range testCase.Expect.Values {
				actual, ok := evaluateJSONPointer(document, pointer)
				if !ok {
					t.Fatalf("%s does not resolve in the synthesized interface: %s", pointer, encoded)
				}
				var want any
				if err := json.Unmarshal(expected, &want); err != nil {
					t.Fatalf("parse expected value: %v", err)
				}
				if !reflect.DeepEqual(actual, want) {
					t.Fatalf("%s = %#v, want %#v", pointer, actual, want)
				}
			}

			if testCase.Expect.DefsAt != "" {
				node, ok := evaluateJSONPointer(document, testCase.Expect.DefsAt+"/$defs")
				if !ok {
					t.Fatalf("no $defs at %s: %s", testCase.Expect.DefsAt, encoded)
				}
				object, ok := node.(map[string]any)
				if !ok {
					t.Fatalf("$defs at %s is not an object", testCase.Expect.DefsAt)
				}
				got := make([]string, 0, len(object))
				for key := range object {
					got = append(got, key)
				}
				sort.Strings(got)
				want := append([]string(nil), testCase.Expect.Defs...)
				sort.Strings(want)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("$defs at %s = %v, want %v", testCase.Expect.DefsAt, got, want)
				}
			}

			for _, address := range testCase.Expect.Unretrieved {
				if retrieved[address] {
					t.Fatalf("%s was retrieved although it is reachable only from outside the composed closure", address)
				}
			}
		})
	}
}

func evaluateJSONPointer(root any, pointer string) (any, bool) {
	current := root
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		child, present := object[token]
		if !present {
			return nil, false
		}
		current = child
	}
	return current, true
}
